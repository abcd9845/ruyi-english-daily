package server

import (
	"bytes"
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
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

const (
	maxGradeBody   = 64 << 10
	maxBlankLen    = 200
	maxFreeLen     = brain.MaxAnswerLen // the free-text cap IS the brain's cap: one constant
	statusUnanswer = "unanswered"
	modeGraded     = "graded"
	modeArchive    = "archive_only"
	idemCap        = 5000
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type gradeResult struct {
	QRef    string `json:"qref"`
	Status  string `json:"status"`
	Correct *bool  `json:"correct"`
	// Recorded is true only if the brain itself confirmed it recorded this
	// answer. AttemptID is the brain's integer id; Duplicate marks a re-delivery
	// of an answer the brain already had.
	Recorded  bool  `json:"recorded"`
	AttemptID int64 `json:"attempt_id,omitempty"`
	Duplicate bool  `json:"duplicate,omitempty"`
}

type gradeOut struct {
	ClientAttemptID string `json:"client_attempt_id"`
	SetID           string `json:"set_id"`
	// Mode is "graded" when the brain replied (verdicts may be shown) and
	// "archive_only" when it was not used (BrainNote says why).
	Mode string `json:"mode"`
	// BrainRecorded is true only if the brain recorded at least one answer;
	// RecordedCount says how many. The page says "submitted to the brain" only then.
	BrainRecorded bool             `json:"brain_recorded"`
	RecordedCount int              `json:"recorded_count"`
	BrainNote     string           `json:"brain_note,omitempty"` // not_configured | not_ready | unavailable | not_sent
	Results       []gradeResult    `json:"results"`
	Summary       attempts.Summary `json:"summary"`
	Archived      bool             `json:"archived"`
	Notify        string           `json:"notify,omitempty"` // sent | retrying | off | unknown
	Replayed      bool             `json:"replayed,omitempty"`
	// Test is set for a local test set: archive_only, judged on the server,
	// never recorded in the brain. Absent for real sets.
	Test bool `json:"test,omitempty"`
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

type gradeAnswer struct {
	QRef   string          `json:"qref"`
	Answer json.RawMessage `json:"answer"`
	// Rev: the question revision the page was served (practice item "rev").
	Rev *int `json:"rev"`
	// AttemptKind: optional "correction" | "variant", per answer.
	AttemptKind string `json:"attempt_kind"`
}

type gradeReq struct {
	ClientAttemptID string `json:"client_attempt_id"`
	// AttemptKind is the default for answers that do not carry their own.
	AttemptKind string `json:"attempt_kind"`
	// AnsweredAt (RFC 3339) is fixed by the page together with the
	// client_attempt_id, so a retry of the same id carries the same value (the
	// brain compares it on idempotent replays). Out-of-window values are not forwarded.
	AnsweredAt string        `json:"answered_at"`
	Answers    []gradeAnswer `json:"answers"`
}

const (
	maxRev = 1_000_000_000
)

// Page-facing messages for the brain's error replies.
const (
	msgDailyCap     = "今天这题提交次数到上限了（同一题每天最多记 3 次），明天再试。答案还在。"
	msgRevMismatch  = "题目已更新，请刷新页面后再提交。你的答案还在。"
	msgIdemConflict = "这次提交的编号和上一次对不上，请再点一次「提交」（会换新编号）。"
	msgUnknownQref  = "有题目在大脑里找不到了，请刷新页面；还不行请告诉家长。"
)

// answerFitsBrain: the brain accepts answer strings of at most brain.MaxAnswerLen
// characters (Unicode code points, counted with utf8.RuneCountInString, like the
// brain does). A longer one is not sent (archived as "unsupported" by this site),
// never truncated, never sent to be refused. (Free text is already capped at the
// same number by parseAnswer, so this is a second guard that should never trigger.)
//
// (A 404 from the grade URL is told apart by its details, not by an error code:
// details.qrefs = the brain's "question not found"; any other 404 = the route is
// not deployed there yet and the submission degrades to archive-only.)
func answerFitsBrain(ans any) bool {
	ok := func(s string) bool { return utf8.RuneCountInString(s) <= brain.MaxAnswerLen }
	switch a := ans.(type) {
	case string:
		return ok(a)
	case []string:
		for _, x := range a {
			if !ok(x) {
				return false
			}
		}
	}
	return true
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
	if len(req.Answers) > brain.MaxGradeItems {
		writeErr(w, http.StatusBadRequest, "too_many_items", "一次最多提交 50 题")
		return
	}
	if !validKind(req.AttemptKind) {
		writeErr(w, http.StatusBadRequest, "bad_attempt_kind", "提交类型不对")
		return
	}
	var answeredAt time.Time
	if req.AnsweredAt != "" {
		t, err := time.Parse(time.RFC3339, req.AnsweredAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_answered_at", "作答时间格式不对")
			return
		}
		// outside the brain's window (now-7d .. now+1min) the field is simply not sent
		if brain.AnsweredAtSendable(t, s.now()) {
			answeredAt = t.UTC()
		}
	}

	// a local test set has its question inline: the brain is not asked for it
	var found map[string]brain.Question
	if !set.Local {
		var err error
		found, _, err = s.Bank.LookupByQrefs(r.Context(), set.Source, set.QRefs)
		if err != nil {
			s.brainErr(w, "grade-lookup", err)
			return
		}
	}
	inSet := map[string]bool{}
	for _, q := range set.QRefs {
		inSet[q] = true
	}
	type given struct {
		qref string
		ans  any
		rev  int
		kind string
		skip bool // not sent to the brain (contract limit); archived as unsupported
	}
	answered := map[string]given{}
	for _, a := range req.Answers {
		if !inSet[a.QRef] {
			writeErr(w, http.StatusBadRequest, "qref_not_in_set", "有题目不属于这套练习")
			return
		}
		var sh qshape
		var lq *sets.Question
		if set.Local {
			lq, _ = set.Question(a.QRef)
			sh = shapeOfLocal(lq)
		} else {
			q, ok := found[a.QRef]
			if !ok {
				writeErr(w, http.StatusNotFound, "unknown_qref", msgUnknownQref)
				return
			}
			sh = shapeOf(q)
		}
		if _, dup := answered[a.QRef]; dup {
			writeErr(w, http.StatusBadRequest, "duplicate_qref", "同一题提交了两次")
			return
		}
		ans, empty, ok := parseAnswer(sh, a.Answer)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_answer_shape", "作答格式和题型不匹配")
			return
		}
		g := given{qref: a.QRef}
		if !empty {
			g.ans = ans
			// (a local set never goes to the brain, so the brain's contract limits do not apply)
			g.skip = !set.Local && (sh.kind == "fill" && sh.blanks > brain.MaxBlanks || !answerFitsBrain(ans))
		}
		if a.Rev != nil {
			if *a.Rev < 1 || *a.Rev > maxRev {
				writeErr(w, http.StatusBadRequest, "bad_rev", "题目版本号不对")
				return
			}
			g.rev = *a.Rev
		}
		if !validKind(a.AttemptKind) {
			writeErr(w, http.StatusBadRequest, "bad_attempt_kind", "提交类型不对")
			return
		}
		g.kind = a.AttemptKind
		if g.kind == "" {
			g.kind = req.AttemptKind
		}
		answered[a.QRef] = g
	}
	nAnswered, nSend := 0, 0
	for _, g := range answered {
		if g.ans != nil {
			nAnswered++
			if !g.skip {
				nSend++
			}
		}
	}
	if nAnswered == 0 {
		writeErr(w, http.StatusBadRequest, "nothing_answered", "还没有作答")
		return
	}
	// fingerprint of what is being submitted (answers, revs, kinds; not the time)
	type sigItem struct {
		Q string
		A any
		R int
		K string
	}
	var sigItems []sigItem
	for _, q := range set.QRefs {
		if g, ok := answered[q]; ok {
			sigItems = append(sigItems, sigItem{q, g.ans, g.rev, g.kind})
		}
	}
	sigRaw, _ := json.Marshal(struct {
		Set   string
		Items []sigItem
	}{set.ID, sigItems})
	sigSum := sha256.Sum256(sigRaw)
	sig := hex.EncodeToString(sigSum[:12])

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
		if e.rec.Sig != "" && e.rec.Sig != sig {
			// same id, different answers: the page must use a new id (contract: idempotency_conflict)
			s.Log.Warn("same client_attempt_id with different answers refused", "set", set.ID)
			writeIdemConflict(w)
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

	// grade at the brain; the brain's refusals map to page states, anything else
	// (not configured, endpoint not deployed, down) degrades to archive-only
	var items []brain.GradeItem
	for _, q := range set.QRefs {
		if g, ok := answered[q]; ok && g.ans != nil && !g.skip {
			items = append(items, brain.GradeItem{QRef: q, Answer: g.ans, Rev: g.rev, AttemptKind: g.kind, AnsweredAt: answeredAt, SessionRef: set.ID})
		}
	}
	mode := modeArchive
	note := "not_configured"
	verdict := map[string]gradeResult{}
	var gresp *brain.GradeResponse
	var gerr error
	switch {
	case set.Local:
		// a local test set NEVER calls the brain, whatever is configured
		note = noteTestSet
	case nSend == 0:
		gerr = errors.New("nothing to send") // every answer exceeds a contract limit: archive only
		note = "not_sent"
	default:
		gresp, gerr = s.Bank.Grade(r.Context(), brain.GradeRequest{ClientAttemptID: cid, Items: items})
	}
	switch {
	case set.Local:
		// archive_only / test_set, decided above
	case gerr == nil:
		mode, note = modeGraded, ""
		for _, g := range gresp.Results {
			verdict[g.QRef] = gradeResult{QRef: g.QRef, Status: g.Status, Correct: g.Correct, Recorded: g.Recorded, AttemptID: g.AttemptID, Duplicate: g.Duplicate}
		}
	case errors.Is(gerr, brain.ErrGradeDisabled):
		// configured off: nothing to log per request
	default:
		var be *brain.Error
		desc := "unknown"
		if errors.As(gerr, &be) {
			desc = be.Error() // fixed text + status + upstream error code only
		}
		note = "unavailable"
		if nSend == 0 {
			note = "not_sent"
		}
		if be != nil {
			switch {
			case be.Kind == brain.KindRateLimited:
				s.Log.Warn("brain refused the batch: rate limited", "set", set.ID, "err", desc)
				if be.RetryAfter > 0 { // passed on as the brain gave it (seconds)
					w.Header().Set("Retry-After", fmt.Sprint(int(be.RetryAfter/time.Second)))
				}
				writeErr(w, http.StatusTooManyRequests, "daily_cap", msgDailyCap)
				return
			case be.Kind == brain.KindConflict && be.Code == "rev_mismatch":
				s.Log.Warn("brain refused the batch: question revision changed", "set", set.ID, "err", desc)
				writeErr(w, http.StatusConflict, "rev_mismatch", msgRevMismatch)
				return
			case be.Kind == brain.KindConflict && be.Code == "idempotency_conflict":
				s.Log.Warn("brain refused the batch: idempotency conflict (client bug path)", "set", set.ID, "err", desc)
				writeIdemConflict(w)
				return
			case be.HasQrefs():
				s.Log.Warn("brain refused the batch: unknown qref", "set", set.ID, "err", desc)
				writeErr(w, http.StatusNotFound, "unknown_qref", msgUnknownQref)
				return
			case be.Kind == brain.KindNotReady || be.Kind == brain.KindNotFound:
				// a 404 without details.qrefs is the catch-all of a brain that does not have the route yet
				note = "not_ready"
			}
		}
		s.Log.Warn("grading unavailable; archive-only", "set", set.ID, "note", note, "err", desc)
	}

	rec := &attempts.Record{
		V: 2, ClientAttemptID: cid, SetID: set.ID, SetTitle: set.Title, Source: set.Source,
		SubmittedAt: s.now().UTC(), Mode: mode, BrainNote: note, Sig: sig, Test: set.Local,
		Client: attempts.Client{UA: clip(r.UserAgent(), 160), IP: clientIP(r)},
	}
	out := &gradeOut{ClientAttemptID: cid, SetID: set.ID, Mode: mode, BrainNote: note, Results: []gradeResult{}, Test: set.Local}
	for _, q := range set.QRefs {
		g, asked := answered[q]
		var res gradeResult
		switch {
		case !asked || g.ans == nil:
			res = gradeResult{QRef: q, Status: statusUnanswer}
		case g.skip:
			res = gradeResult{QRef: q, Status: brain.StatusUnsupport} // too many blanks for the contract
		case set.Local:
			// judged here, from the question's server-side answer; recorded stays false
			lq, _ := set.Question(q)
			c := judgeLocal(lq, g.ans)
			res = gradeResult{QRef: q, Status: brain.StatusGraded, Correct: &c}
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
		it := attempts.Item{QRef: q, Status: res.Status, Correct: res.Correct, Recorded: res.Recorded, AttemptID: res.AttemptID, Duplicate: res.Duplicate, Rev: g.rev}
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
		case brain.StatusUnsupport:
			out.Summary.Unsupport++
		default: // pending_key, needs_review, never sent
			out.Summary.Pending++
		}
		if res.Recorded {
			out.Summary.Recorded++
		}
	}
	out.RecordedCount = out.Summary.Recorded
	out.BrainRecorded = out.RecordedCount > 0
	rec.Summary = out.Summary

	rec.Archived = s.Attempts != nil && s.Attempts.Append(rec) == nil
	out.Archived = rec.Archived

	// The parent notification is sent in the background (notify.go). It is
	// started before the attempt is published to the idempotency store, which
	// is finished right away, so a concurrent duplicate request is never held
	// up by ntfy; only THIS response waits (at most ntfyRequestWait) to report
	// how the first send went. A replay of this attempt never notifies again.
	job, nstatus := s.startNotify(rec)
	s.idem.finish(e, rec)
	finished = true
	out.Notify = job.wait(nstatus, s.ntfyWait)
	writeJSON(w, http.StatusOK, out)
}

func validKind(k string) bool {
	return k == "" || k == brain.AttemptCorrect || k == brain.AttemptVariant
}

// writeIdemConflict is the answer to "this client_attempt_id was already used
// for different answers" (our own bug path, or a changed page that kept an old
// id). The page reacts to retry_new_id by dropping its id and resubmitting once.
func writeIdemConflict(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]any{"error": "idempotency_conflict", "message": msgIdemConflict, "retry_new_id": true})
}

func outFromRecord(r *attempts.Record) *gradeOut {
	out := &gradeOut{ClientAttemptID: r.ClientAttemptID, SetID: r.SetID, Mode: r.Mode, BrainNote: r.BrainNote,
		Results: []gradeResult{}, Summary: r.Summary, Archived: r.Archived, Notify: r.Notify, Test: r.Test}
	for _, it := range r.Items {
		rec := it.Recorded
		if r.V < 2 && r.Mode == modeGraded && it.Status != statusUnanswer && it.Status != brain.StatusUnsupport {
			rec = true // v1 archives: "graded" meant the brain confirmed the whole attempt
		}
		out.Results = append(out.Results, gradeResult{QRef: it.QRef, Status: it.Status, Correct: it.Correct, Recorded: rec, AttemptID: it.AttemptID, Duplicate: it.Duplicate})
		if rec {
			out.RecordedCount++
		}
	}
	out.BrainRecorded = out.RecordedCount > 0
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
