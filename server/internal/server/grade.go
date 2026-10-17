package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/practice"
)

const (
	maxGradeBody   = 64 << 10
	maxBlankLen    = 200
	maxFreeLen     = 2000
	statusUnanswer = "unanswered"
	modeGraded     = "graded"
	modeArchive    = "archive_only"
	idemCap        = 5000
	ntfyMsgMax     = 3500
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type gradeResult struct {
	QRef    string `json:"qref"`
	Status  string `json:"status"`
	Correct *bool  `json:"correct"`
}

type gradeOut struct {
	ClientAttemptID string           `json:"client_attempt_id"`
	SetID           string           `json:"set_id"`
	Mode            string           `json:"mode"`
	BrainRecorded   bool             `json:"brain_recorded"`
	Results         []gradeResult    `json:"results"`
	Summary         attempts.Summary `json:"summary"`
	Archived        bool             `json:"archived"`
	Notify          string           `json:"notify,omitempty"` // sent | retrying | off | unknown
	Replayed        bool             `json:"replayed,omitempty"`
}

// ---- idempotency by client_attempt_id --------------------------------------

type idemEntry struct {
	done chan struct{}
	rec  *attempts.Record
}

type idemStore struct {
	mu    sync.Mutex
	m     map[string]*idemEntry
	order []string
}

func newIdemStore(st *attempts.Store) *idemStore {
	d := &idemStore{m: map[string]*idemEntry{}}
	if st != nil {
		for _, r := range st.Load(idemCap) {
			e := &idemEntry{done: make(chan struct{}), rec: r}
			close(e.done)
			d.m[r.ClientAttemptID] = e
			d.order = append(d.order, r.ClientAttemptID)
		}
	}
	return d
}

// begin returns the entry for id and whether the caller owns it (must call finish/abort).
func (d *idemStore) begin(id string) (*idemEntry, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.m[id]; ok {
		return e, false
	}
	e := &idemEntry{done: make(chan struct{})}
	d.m[id] = e
	d.order = append(d.order, id)
	for len(d.order) > idemCap {
		old := d.order[0]
		d.order = d.order[1:]
		if oe := d.m[old]; oe != nil && oe.rec != nil {
			delete(d.m, old)
		}
	}
	return e, true
}

func (d *idemStore) finish(e *idemEntry, rec *attempts.Record) {
	e.rec = rec
	close(e.done)
}

// abort drops an unfinished entry (validation/limit failure after begin).
func (d *idemStore) abort(id string, e *idemEntry) {
	d.mu.Lock()
	delete(d.m, id)
	d.mu.Unlock()
	close(e.done)
}

// ---- request validation ------------------------------------------------------

type gradeReq struct {
	ClientAttemptID string `json:"client_attempt_id"`
	Answers         []struct {
		QRef   string          `json:"qref"`
		Answer json.RawMessage `json:"answer"`
	} `json:"answers"`
}

type qshape struct {
	kind    string // single | multi | fill | free
	keys    map[string]bool
	blanks  int
	present bool
}

func shapeOf(q brain.Question) qshape {
	sh := qshape{present: true}
	var opts []optionOut
	if len(q.Options) > 0 && string(q.Options) != "null" {
		_ = json.Unmarshal(q.Options, &opts)
	}
	keys := map[string]bool{}
	for _, o := range opts {
		if o.Key != "" {
			keys[o.Key] = true
		}
	}
	switch {
	case len(keys) > 0:
		sh.keys = keys
		sh.kind = "single"
		if q.QType == "multi_choice" {
			sh.kind = "multi"
		}
	case practice.CountBlanks(q.Stem) > 0:
		sh.kind, sh.blanks = "fill", practice.CountBlanks(q.Stem)
	default:
		sh.kind = "free"
	}
	return sh
}

func cleanText(s string, max int) (string, bool) {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// parseAnswer checks the shape against the question type. It returns the
// normalized answer (string or []string); empty=true means "left blank".
func parseAnswer(sh qshape, raw json.RawMessage) (ans any, empty bool, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true, true
	}
	var str string
	var arr []string
	isStr := json.Unmarshal(raw, &str) == nil
	isArr := !isStr && json.Unmarshal(raw, &arr) == nil
	switch sh.kind {
	case "single":
		if !isStr {
			return nil, false, false
		}
		if str == "" {
			return nil, true, true
		}
		return str, false, sh.keys[str]
	case "multi":
		if !isArr {
			return nil, false, false
		}
		seen := map[string]bool{}
		for _, k := range arr {
			if !sh.keys[k] || seen[k] {
				return nil, false, false
			}
			seen[k] = true
		}
		if len(arr) == 0 {
			return nil, true, true
		}
		sort.Strings(arr)
		return arr, false, true
	case "fill":
		if !isArr || len(arr) != sh.blanks {
			return nil, false, false
		}
		any := false
		out := make([]string, len(arr))
		for i, a := range arr {
			c, ok := cleanText(a, maxBlankLen)
			if !ok {
				return nil, false, false
			}
			out[i] = c
			if strings.TrimSpace(c) != "" {
				any = true
			}
		}
		if !any {
			return nil, true, true
		}
		return out, false, true
	default: // free
		if !isStr {
			return nil, false, false
		}
		c, ok := cleanText(str, maxFreeLen)
		if !ok {
			return nil, false, false
		}
		if strings.TrimSpace(c) == "" {
			return nil, true, true
		}
		return c, false, true
	}
}

// ---- handler ------------------------------------------------------------------

func (s *Server) grade(w http.ResponseWriter, r *http.Request, id string) {
	if mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])); mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return
	}
	set, ok := s.getSet(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	var req gradeReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGradeBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "提交内容格式不对")
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "bad_request", "提交内容格式不对")
		return
	}
	if !uuidRe.MatchString(req.ClientAttemptID) {
		writeErr(w, http.StatusBadRequest, "bad_client_attempt_id", "提交编号不对")
		return
	}
	cid := strings.ToLower(req.ClientAttemptID)
	if len(req.Answers) == 0 || len(req.Answers) > len(set.QRefs) {
		writeErr(w, http.StatusBadRequest, "bad_answers", "提交的题数不对")
		return
	}

	found, _, err := s.Bank.LookupByQrefs(r.Context(), set.Source, set.QRefs)
	if err != nil {
		s.brainErr(w, "grade-lookup", err)
		return
	}
	inSet := map[string]bool{}
	for _, q := range set.QRefs {
		inSet[q] = true
	}
	type given struct {
		qref string
		ans  any
	}
	answered := map[string]given{}
	for _, a := range req.Answers {
		q, ok := found[a.QRef]
		if !inSet[a.QRef] || !ok {
			writeErr(w, http.StatusBadRequest, "qref_not_in_set", "有题目不属于这套练习")
			return
		}
		if _, dup := answered[a.QRef]; dup {
			writeErr(w, http.StatusBadRequest, "duplicate_qref", "同一题提交了两次")
			return
		}
		ans, empty, ok := parseAnswer(shapeOf(q), a.Answer)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_answer_shape", "作答格式和题型不匹配")
			return
		}
		if !empty {
			answered[a.QRef] = given{a.QRef, ans}
		} else {
			answered[a.QRef] = given{a.QRef, nil}
		}
	}
	nAnswered := 0
	for _, g := range answered {
		if g.ans != nil {
			nAnswered++
		}
	}
	if nAnswered == 0 {
		writeErr(w, http.StatusBadRequest, "nothing_answered", "还没有作答")
		return
	}

	// idempotency: the same client_attempt_id returns the stored result
	e, owner := s.idem.begin(cid)
	if !owner {
		select {
		case <-e.done:
		case <-r.Context().Done():
			return
		}
		if e.rec == nil { // the first request failed before storing anything: ask the client to retry
			writeErr(w, http.StatusConflict, "attempt_in_progress", "上一次提交没有完成，请再点一次")
			return
		}
		if e.rec.SetID != set.ID {
			writeErr(w, http.StatusConflict, "attempt_id_conflict", "提交编号已用于别的套")
			return
		}
		out := outFromRecord(e.rec)
		out.Replayed = true
		writeJSON(w, http.StatusOK, out)
		return
	}
	finished := false
	defer func() {
		if !finished {
			s.idem.abort(cid, e)
		}
	}()

	// rate limit: per session and per IP, per set, fresh submissions only
	sk := sessionKey(r) + "|" + set.ID
	ik := clientIP(r) + "|" + set.ID
	for _, l := range []*auth.Limiter{s.gradeSess, s.gradeIP} {
		key := sk
		if l == s.gradeIP {
			key = ik
		}
		if blocked, wait := l.Blocked(key); blocked {
			w.Header().Set("Retry-After", fmt.Sprint(int(wait/time.Second)+1))
			s.Log.Warn("grade rate limited", "set", set.ID)
			writeErr(w, http.StatusTooManyRequests, "too_many_submissions", "这套题一小时内提交次数用完了，请稍后再试")
			return
		}
	}
	s.gradeSess.Fail(sk)
	s.gradeIP.Fail(ik)

	// grade at the brain; any failure degrades to archive-only
	var items []brain.GradeItem
	for _, q := range set.QRefs {
		if g, ok := answered[q]; ok && g.ans != nil {
			items = append(items, brain.GradeItem{QRef: q, Answer: g.ans})
		}
	}
	mode := modeArchive
	verdict := map[string]gradeResult{}
	brainID := ""
	gresp, gerr := s.Bank.Grade(r.Context(), brain.GradeRequest{ClientAttemptID: cid, SetID: set.ID, Source: set.Source, Items: items})
	switch {
	case gerr == nil:
		mode, brainID = modeGraded, gresp.BrainAttemptID
		for _, g := range gresp.Results {
			verdict[g.QRef] = gradeResult{QRef: g.QRef, Status: g.Status, Correct: g.Correct}
		}
	case errors.Is(gerr, brain.ErrGradeDisabled):
		// configured off: nothing to log per request
	default:
		var be *brain.Error
		kind := "unknown"
		if errors.As(gerr, &be) {
			kind = be.Error() // fixed text + status + upstream error code only
		}
		s.Log.Warn("grading unavailable; archive-only", "set", set.ID, "err", kind)
	}

	rec := &attempts.Record{
		V: 1, ClientAttemptID: cid, SetID: set.ID, SetTitle: set.Title, Source: set.Source,
		SubmittedAt: s.now().UTC(), Mode: mode, BrainAttemptID: brainID,
		Client: attempts.Client{UA: clip(r.UserAgent(), 160), IP: clientIP(r)},
	}
	out := &gradeOut{ClientAttemptID: cid, SetID: set.ID, Mode: mode, BrainRecorded: mode == modeGraded, Results: []gradeResult{}}
	for _, q := range set.QRefs {
		g, asked := answered[q]
		var res gradeResult
		switch {
		case !asked || g.ans == nil:
			res = gradeResult{QRef: q, Status: statusUnanswer}
		case mode == modeGraded:
			if v, ok := verdict[q]; ok {
				res = v
			} else {
				res = gradeResult{QRef: q, Status: brain.StatusPendingKey}
			}
		default:
			res = gradeResult{QRef: q, Status: brain.StatusPendingKey}
		}
		out.Results = append(out.Results, res)
		it := attempts.Item{QRef: q, Status: res.Status, Correct: res.Correct}
		if asked && g.ans != nil {
			it.Answer = g.ans
		}
		rec.Items = append(rec.Items, it)
		out.Summary.Total++
		switch res.Status {
		case brain.StatusGraded:
			out.Summary.Graded++
			if res.Correct != nil && *res.Correct {
				out.Summary.Correct++
			}
		case statusUnanswer:
			out.Summary.Unanswered++
		default:
			out.Summary.Pending++
		}
	}
	rec.Summary = out.Summary

	rec.Archived = s.Attempts != nil && s.Attempts.Append(rec) == nil
	out.Archived = rec.Archived

	rec.Notify = s.notify(rec)
	out.Notify = rec.Notify

	s.idem.finish(e, rec)
	finished = true
	writeJSON(w, http.StatusOK, out)
}

func outFromRecord(r *attempts.Record) *gradeOut {
	out := &gradeOut{ClientAttemptID: r.ClientAttemptID, SetID: r.SetID, Mode: r.Mode, BrainRecorded: r.Mode == modeGraded,
		Results: []gradeResult{}, Summary: r.Summary, Archived: r.Archived, Notify: r.Notify}
	for _, it := range r.Items {
		out.Results = append(out.Results, gradeResult{QRef: it.QRef, Status: it.Status, Correct: it.Correct})
	}
	return out
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func sessionKey(r *http.Request) string {
	c, err := r.Cookie(auth.CookieName)
	if err != nil {
		return "-"
	}
	h := sha256.Sum256([]byte(c.Value))
	return hex.EncodeToString(h[:8])
}

func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// ---- parent notification (a heads-up, not an approval step) ---------------------

func summaryLine(r *attempts.Record) string {
	answered := r.Summary.Total - r.Summary.Unanswered
	wrong := r.Summary.Graded - r.Summary.Correct
	var b strings.Builder
	fmt.Fprintf(&b, "如意练习：%s %d 题，", r.SetTitle, answered)
	if r.Mode == modeGraded {
		fmt.Fprintf(&b, "对 %d，", r.Summary.Correct)
		if wrong > 0 {
			fmt.Fprintf(&b, "错 %d，", wrong)
		}
	}
	if r.Summary.Pending > 0 || r.Mode != modeGraded {
		fmt.Fprintf(&b, "待核对 %d，", r.Summary.Pending)
	}
	if r.Summary.Unanswered > 0 {
		fmt.Fprintf(&b, "未答 %d，", r.Summary.Unanswered)
	}
	if r.Mode == modeGraded {
		b.WriteString("已提交。")
	} else {
		b.WriteString("已提交（未判分）。")
	}
	fmt.Fprintf(&b, "attempt %s。", r.ClientAttemptID[:8])
	return b.String()
}

func ntfyBody(r *attempts.Record) string {
	head := summaryLine(r)
	full, _ := json.Marshal(map[string]any{"client_attempt_id": r.ClientAttemptID, "set_id": r.SetID, "mode": r.Mode, "brain_attempt_id": r.BrainAttemptID, "items": r.Items})
	if len(head)+2+len(full) <= ntfyMsgMax {
		return head + "\n\n" + string(full)
	}
	type slim struct {
		QRef    string `json:"q"`
		Answer  any    `json:"a,omitempty"`
		Correct *bool  `json:"c"`
	}
	var sl []slim
	for _, it := range r.Items {
		sl = append(sl, slim{it.QRef, it.Answer, it.Correct})
	}
	small, _ := json.Marshal(map[string]any{"client_attempt_id": r.ClientAttemptID, "set_id": r.SetID, "mode": r.Mode, "items": sl})
	if len(head)+2+len(small) <= ntfyMsgMax {
		return head + "\n\n" + string(small)
	}
	return head + "\n\n（作答较长，完整内容见服务器 attempts 存档）"
}

// notify posts the summary once; on failure one asynchronous retry follows.
// It returns "sent", "retrying" or "off". Failures never affect the grade.
func (s *Server) notify(r *attempts.Record) string {
	if s.Topic == "" {
		return "off"
	}
	body := ntfyBody(r)
	if s.postNtfy(body) {
		return "sent"
	}
	go func() {
		time.Sleep(s.NtfyRetryDelay)
		if !s.postNtfy(body) {
			s.Log.Warn("ntfy notification failed after retry", "attempt", r.ClientAttemptID[:8])
		}
	}()
	return "retrying"
}

func (s *Server) postNtfy(body string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ntfyURL(), strings.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", "Ruyi practice")
	req.Header.Set("Tags", "memo")
	resp, err := s.ntfyClient.Do(req)
	if err != nil {
		// err's text contains the URL (topic): log only that it failed
		s.Log.Warn("ntfy unreachable")
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		s.Log.Warn("ntfy rejected the message", "status", resp.StatusCode)
		return false
	}
	return true
}
