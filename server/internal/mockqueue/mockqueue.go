// Package mockqueue is a TEST-ONLY stand-in for the brain's pending-answer queue
// (契约 v1.0 endpoints #1–#6, #9). It is used by the server unit tests and by
// cmd/mockbrain (acceptance / browser tests). The production binary does not
// import it. It holds no real data: answers are kept only to compare idempotent
// replays, texts are synthetic.
package mockqueue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Env is what the mock needs from the grade side of the (mock) brain.
type Env interface {
	// Lookup: does the question exist, and what would grade answer for the answer stored under it.
	Lookup(qref string) (status string, ok bool)
	// Attempt returns the recorded attempt of an idempotency key (ps:<cid>:<qref>).
	Attempt(key string) (id int64, ok bool)
	// DayCount is today's counted writes of the question.
	DayCount(qref string) int
	// Write records a NEW attempt for key (counted against the day) and returns its id.
	Write(key, qref, canonAnswer string, answeredAt time.Time) int64
	Now() time.Time
}

// SeenEnv is optional: an Env that also remembers what the grade call stored under a key,
// so an enqueue replay of a grade-recorded answer is compared on answer + answered_at too.
type SeenEnv interface {
	Seen(key string) (canonAnswer string, answeredAt time.Time, ok bool)
}

// Config configures a Mock.
type Config struct {
	Brain       string
	WriteToken  string
	NotifyToken string
	Env         Env
}

type item struct {
	ID          int64
	Key         string
	CID         string
	QRef        string
	Kind        string
	State       string
	Created     time.Time
	Deadline    time.Time
	Purged      bool
	Compare     string
	NeedsManual bool
	Proposed    string
	Rationale   string
	Verdict     string // review verdict
	Review      string // none|confirmed|overridden
	AttemptID   int64
	SessionRef  string
	Answer      string    // canonical JSON, only to compare replays
	AnsweredAt  time.Time // the idempotency key also fixes answered_at, rev and attempt_kind
	Rev         int
	AttKind     string
}

type event struct {
	ID      int64
	Type    string
	ItemID  int64
	Payload map[string]any
	Created time.Time
}

type sess struct {
	Total int
	Fired bool
}

// Mock is the queue mock. All methods are safe for concurrent use.
type Mock struct {
	cfg Config

	cfgMu    sync.Mutex // kinds / judged only; never held while another lock is taken (grade hints call Queueable)
	mu       sync.Mutex
	items    map[int64]*item
	byKey    map[string]*item
	seq      int64
	events   []event
	evSeq    int64
	acked    int64
	sessions map[string]*sess
	kinds    map[string]string // qref -> absolute|judged
	Stats    map[string]int
	reqs     [][]byte // every enqueue body (secrets never in it; the answers are synthetic)

	judged bool
	images bool // switch "images": accept items[].images (大脑 ② PR-I2); off = ①a (images_not_enabled)
	// LastImages: the images[] of the last enqueued item that carried some (tests).
	LastImages   []string
	mismatch422  bool   // session_total_mismatch as the contract's 422 instead of a 400
	enqueueMode  string // "" | "500" | "slow" | "off" | "auth" | "queue_full"
	batchOff     bool
	detailOff    bool
	includeAcked bool // events ignore acks (the other possible brain behaviour)
	ackFail      int  // fail this many acks with 500
	eventsFail   int  // fail this many event reads with 500
	openCap      int
	slow         time.Duration
	noSession    bool // events carry no session_* fields
}

// New builds a Mock (judged kind OFF, like ①a at deploy time).
func New(c Config) *Mock {
	return &Mock{cfg: c, items: map[int64]*item{}, byKey: map[string]*item{}, sessions: map[string]*sess{}, kinds: map[string]string{},
		Stats: map[string]int{}, openCap: 50, slow: 4 * time.Second}
}

var cidRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)

// Queueable is the additive static hint grade carries (契约 §2).
func (m *Mock) Queueable(qref, status string) (bool, string) {
	if status == "graded" {
		return false, "not_needed"
	}
	if m.kindOf(qref) == "judged" && !m.judgedOn() {
		return false, "kind_not_enabled"
	}
	return true, ""
}

func (m *Mock) kindOf(q string) string {
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	return m.kinds[strings.ToLower(q)]
}
func (m *Mock) imagesOn() bool { m.cfgMu.Lock(); defer m.cfgMu.Unlock(); return m.images }

// Images returns the images[] of the last enqueued item that carried some.
func (m *Mock) Images() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.LastImages...)
}

func (m *Mock) judgedOn() bool { m.cfgMu.Lock(); defer m.cfgMu.Unlock(); return m.judged }

// ---- helpers -------------------------------------------------------------------------

func (m *Mock) bump(k string) { m.mu.Lock(); m.Stats[k]++; m.mu.Unlock() }

func jerr(w http.ResponseWriter, status int, code, msg string, details map[string]any, hdr map[string]string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	if details == nil {
		details = map[string]any{}
	}
	body := map[string]any{"error": map[string]any{"code": code, "message": msg, "request_id": "mock", "details": details}}
	for k, v := range extra {
		body[k] = v
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func jok(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// auth: which token the request carries: "write" | "notify" | "" (unknown/missing -> 401 sent)
func (m *Mock) auth(w http.ResponseWriter, r *http.Request) string {
	h := r.Header.Get("Authorization")
	switch h {
	case "Bearer " + m.cfg.WriteToken:
		return "write"
	case "Bearer " + m.cfg.NotifyToken:
		return "notify"
	}
	if strings.HasPrefix(h, "Bearer ") {
		m.bump("auth_other_token")
		jerr(w, 403, "forbidden", "forbidden", nil, nil, nil) // a valid-looking token without these grants (e.g. the read token)
		return ""
	}
	jerr(w, 401, "unauthorized", "unauthorized", nil, nil, nil)
	return ""
}

func (m *Mock) needWrite(w http.ResponseWriter, r *http.Request) bool {
	switch m.auth(w, r) {
	case "write":
		return true
	case "notify":
		m.bump("notify_token_on_write_endpoint")
		jerr(w, 403, "forbidden", "forbidden", nil, nil, nil)
	}
	return false
}

func (m *Mock) needNotify(w http.ResponseWriter, r *http.Request) bool {
	switch m.auth(w, r) {
	case "notify":
		return true
	case "write":
		m.bump("write_token_on_events_endpoint") // must stay 0: the site never falls back
		jerr(w, 403, "forbidden", "forbidden", nil, nil, nil)
	}
	return false
}

func deadlineFor(kind string, now time.Time) time.Time {
	if kind == "judged" {
		return now.Add(48 * time.Hour)
	}
	return now.Add(24 * time.Hour)
}

func processed(state string) bool {
	switch state {
	case "compared", "judged", "reviewed", "failed", "expired", "purged":
		return true
	}
	return false
}

func (m *Mock) apiState(it *item) string {
	if it.Purged {
		return "purged"
	}
	return it.State
}

// ---- #1 enqueue ------------------------------------------------------------------------

var reasonPriority = []string{"answer_required", "images_not_enabled", "kind_not_enabled", "question_not_found", "rev_mismatch",
	"media_not_found", "session_total_mismatch", "daily_cap", "queue_full"}

// lostWriter swallows the reply: the brain did the work, the answer never arrives.
type lostWriter struct {
	h    http.Header
	code int
}

func (l *lostWriter) Header() http.Header         { return l.h }
func (l *lostWriter) Write(b []byte) (int, error) { return len(b), nil }
func (l *lostWriter) WriteHeader(c int)           { l.code = c }

func (m *Mock) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	lost := m.enqueueMode == "lost"
	m.mu.Unlock()
	if !lost {
		m.enqueueInner(w, r)
		return
	}
	m.enqueueInner(&lostWriter{h: http.Header{}}, r) // processed (written!) ...
	if hj, ok := w.(http.Hijacker); ok {             // ... but the connection dies before the reply
		if c, _, err := hj.Hijack(); err == nil {
			_ = c.Close()
		}
	}
}

func (m *Mock) enqueueInner(w http.ResponseWriter, r *http.Request) {
	m.bump("enqueue_calls")
	if !m.needWrite(w, r) {
		return
	}
	m.mu.Lock()
	mode, slow := m.enqueueMode, m.slow
	m.mu.Unlock()
	switch mode {
	case "lost":
		m.mu.Lock()
		m.enqueueMode = "" // only the first call loses its reply
		m.mu.Unlock()
	case "500":
		m.bump("enqueue_500")
		jerr(w, 500, "internal", "boom", nil, nil, nil)
		return
	case "off":
		jerr(w, 404, "not_found", "not found", nil, nil, nil)
		return
	case "400":
		m.bump("enqueue_400")
		jerr(w, 400, "validation_failed", "invalid request", map[string]any{"field": "items"}, nil, nil)
		return
	case "auth":
		jerr(w, 403, "forbidden", "forbidden", nil, nil, nil)
		return
	case "slow":
		time.Sleep(slow)
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	m.mu.Unlock()
	var req struct {
		Brain        string  `json:"brain"`
		CID          string  `json:"client_attempt_id"`
		SessionRef   *string `json:"session_ref"`
		SessionTotal *int    `json:"session_total"`
		Items        []struct {
			QRef        string          `json:"qref"`
			Answer      any             `json:"answer"`
			Images      json.RawMessage `json:"images"`
			Rev         *int            `json:"rev"`
			AttemptKind string          `json:"attempt_kind"`
			AnsweredAt  *string         `json:"answered_at"`
		} `json:"items"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		m.bump("enqueue_400")
		jerr(w, 400, "invalid_json", "invalid json", nil, nil, nil)
		return
	}
	bad := func(field string) {
		m.bump("enqueue_400")
		jerr(w, 400, "validation_failed", "invalid "+field, map[string]any{"field": field}, nil, nil)
	}
	if req.Brain != m.cfg.Brain || !cidRe.MatchString(req.CID) || len(req.Items) == 0 || len(req.Items) > 100 {
		bad("request")
		return
	}
	sref := ""
	if req.SessionRef != nil {
		sref = *req.SessionRef
		if !cidRe.MatchString(sref) {
			bad("session_ref")
			return
		}
	}
	if req.SessionTotal != nil && (*req.SessionTotal < 1 || *req.SessionTotal > 100) {
		bad("session_total")
		return
	}
	now := m.cfg.Env.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	type verdict struct {
		qref    string
		reasons []string
	}
	vs := make([]verdict, len(req.Items))
	seen := map[string]bool{}
	canon := make([]string, len(req.Items))
	answered := make([]time.Time, len(req.Items))
	for i, it := range req.Items {
		q := strings.ToLower(it.QRef)
		if q == "" || seen[q] {
			m.Stats["enqueue_400"]++
			jerr(w, 400, "validation_failed", "duplicate or empty qref", nil, nil, nil)
			return
		}
		seen[q] = true
		vs[i].qref = q
		hasAns := it.Answer != nil
		if s, ok := it.Answer.(string); ok && s == "" {
			hasAns = false
		}
		hasImg := len(it.Images) > 0 && string(it.Images) != "null" && string(it.Images) != "[]"
		if !hasAns && !hasImg {
			vs[i].reasons = append(vs[i].reasons, "answer_required")
		}
		if hasImg && !m.imagesOn() {
			vs[i].reasons = append(vs[i].reasons, "images_not_enabled")
		}
		if hasImg && m.imagesOn() {
			var refs []struct {
				SHA256 string `json:"sha256"`
			}
			_ = json.Unmarshal(it.Images, &refs)
			var shas []string
			for _, r := range refs {
				shas = append(shas, r.SHA256)
			}
			m.LastImages = shas // m.mu is held by enqueue
		}
		status, known := m.cfg.Env.Lookup(q)
		if !known {
			vs[i].reasons = append(vs[i].reasons, "question_not_found")
		}
		if known && m.kindOf(q) == "judged" && !m.judgedOn() {
			vs[i].reasons = append(vs[i].reasons, "kind_not_enabled")
		}
		_ = status
		if it.AnsweredAt != nil {
			t, err := time.Parse(time.RFC3339, *it.AnsweredAt)
			if err != nil || t.Before(now.Add(-7*24*time.Hour)) || t.After(now.Add(time.Minute)) {
				m.Stats["enqueue_400"]++
				jerr(w, 400, "validation_failed", "invalid answered_at", map[string]any{"field": "answered_at"}, nil, nil)
				return
			}
			answered[i] = t
		}
		c, _ := json.Marshal(it.Answer)
		canon[i] = string(c)
	}
	if sref != "" && req.SessionTotal != nil {
		// a session_ref has ONE session_total. Default: a 400 for the whole request and nothing is
		// written. With Set("mismatch_status","422") the contract's wording instead: a batch-level
		// 422 whose every item carries reason session_total_mismatch (not a permanent reason).
		if s := m.sessions[sref]; s != nil && s.Total != 0 && s.Total != *req.SessionTotal {
			m.Stats["enqueue_total_mismatch"]++
			if m.mismatch422 {
				for i := range vs {
					vs[i].reasons = append(vs[i].reasons, "session_total_mismatch")
				}
			} else {
				m.Stats["enqueue_400"]++
				jerr(w, 400, "session_total_mismatch", "session_total differs from the one already registered for this session_ref",
					map[string]any{"field": "session_total", "registered": s.Total}, nil, nil)
				return
			}
		}
	}
	// idempotency conflict: the key ps:<cid>:<qref> fixes answer, rev, attempt_kind AND answered_at
	// (a replay that differs in any of them is a 409; a changed answered_at is the classic site bug)
	for i := range req.Items {
		key := "ps:" + req.CID + ":" + vs[i].qref
		rev := 0
		if req.Items[i].Rev != nil {
			rev = *req.Items[i].Rev
		}
		conflict := false
		if prev := m.byKey[key]; prev != nil {
			conflict = prev.Answer != canon[i] || !prev.AnsweredAt.Equal(answered[i]) || prev.Rev != rev || prev.AttKind != req.Items[i].AttemptKind
		} else if se, ok := m.cfg.Env.(SeenEnv); ok {
			if c, at, has := se.Seen(key); has {
				conflict = (c != "" && c != canon[i]) || !at.Equal(answered[i])
			}
		}
		if conflict {
			m.Stats["enqueue_conflict"]++
			jerr(w, 409, "idempotency_conflict", "idempotency conflict (answer, rev, attempt_kind or answered_at differs)", nil, nil, nil)
			return
		}
	}
	// daily cap: a NEW write is needed when no recorded attempt exists for the key
	for i := range req.Items {
		key := "ps:" + req.CID + ":" + vs[i].qref
		if m.byKey[key] != nil {
			continue
		}
		if _, has := m.cfg.Env.Attempt(key); !has && m.cfg.Env.DayCount(vs[i].qref) >= 3 {
			vs[i].reasons = append(vs[i].reasons, "daily_cap")
		}
	}
	open := 0
	for _, it := range m.items {
		if it.State == "queued" || it.State == "claimed" {
			open++
		}
	}
	newCount := 0
	for i := range req.Items {
		if m.byKey["ps:"+req.CID+":"+vs[i].qref] == nil {
			newCount++
		}
	}
	if mode == "queue_full" || open+newCount > m.openCap {
		for i := range vs {
			vs[i].reasons = append(vs[i].reasons, "queue_full")
		}
	}
	anyBad := false
	for _, v := range vs {
		if len(v.reasons) > 0 {
			anyBad = true
		}
	}
	if anyBad {
		m.Stats["enqueue_rejected"]++
		top := ""
		for _, p := range reasonPriority {
			for _, v := range vs {
				for _, rs := range v.reasons {
					if rs == p && top == "" {
						top = p
					}
				}
			}
		}
		limit := false
		retry := 0
		for _, v := range vs {
			for _, rs := range v.reasons {
				if rs == "queue_full" {
					limit, retry = true, 60
				}
				if rs == "daily_cap" {
					limit = true
					if retry == 0 {
						nowSh := now.In(time.FixedZone("CST", 8*3600))
						next := time.Date(nowSh.Year(), nowSh.Month(), nowSh.Day()+1, 0, 0, 0, 0, nowSh.Location())
						retry = int(next.Sub(nowSh).Seconds()) + 1
					}
				}
			}
		}
		if limit { // 429: the code is the highest limit-type reason
			for _, p := range []string{"daily_cap", "queue_full"} {
				for _, v := range vs {
					for _, rs := range v.reasons {
						if rs == p {
							top = p
							goto picked
						}
					}
				}
			}
		}
	picked:
		static := map[string]bool{"answer_required": true, "images_not_enabled": true, "kind_not_enabled": true, "question_not_found": true}
		items := make([]map[string]any, 0, len(vs))
		for _, v := range vs {
			o := map[string]any{"qref": v.qref, "ok": len(v.reasons) == 0, "reason": nil, "queueable_reason": nil, "retry_after_s": nil}
			if len(v.reasons) == 0 {
				o["would_accept"] = true
			} else {
				best := v.reasons[0]
				for _, p := range reasonPriority {
					for _, rs := range v.reasons {
						if rs == p {
							best = p
							goto got
						}
					}
				}
			got:
				o["reason"], o["reasons"] = best, v.reasons
				if static[best] {
					o["queueable_reason"] = best
				}
				if best == "queue_full" || best == "daily_cap" {
					o["retry_after_s"] = retry
				}
			}
			items = append(items, o)
		}
		status, hdr := 422, map[string]string(nil)
		var det map[string]any
		if limit {
			status, hdr = 429, map[string]string{"Retry-After": strconv.Itoa(retry)}
			det = map[string]any{"retry_after_s": retry}
		} else {
			det = map[string]any{"retry_after_s": nil}
		}
		jerr(w, status, top, "batch not written", det, hdr, map[string]any{"written": false, "items": items})
		return
	}
	// write
	if sref != "" && req.SessionTotal != nil {
		s := m.sessions[sref]
		if s == nil {
			s = &sess{}
			m.sessions[sref] = s
		}
		s.Total = *req.SessionTotal
	}
	out := make([]map[string]any, 0, len(req.Items))
	wrote := false
	for i, it := range req.Items {
		q := vs[i].qref
		key := "ps:" + req.CID + ":" + q
		o := map[string]any{"qref": it.QRef, "ok": true}
		if ex := m.byKey[key]; ex != nil {
			o["item_id"], o["state"], o["version"], o["created"] = ex.ID, m.apiState(ex), 0, false
			o["attempt_id"], o["new_attempt_written"] = ex.AttemptID, false
			o["question_kind"], o["deadline_at"] = ex.Kind, ex.Deadline.UTC().Format(time.RFC3339)
			o["daily_remaining"] = max(0, 3-m.cfg.Env.DayCount(q))
			out = append(out, o)
			m.Stats["enqueue_replay_item"]++
			continue
		}
		aid, has := m.cfg.Env.Attempt(key)
		newWritten := false
		if !has {
			aid = m.cfg.Env.Write(key, q, canon[i], answered[i])
			newWritten = true
			m.Stats["enqueue_new_attempt"]++
		}
		m.seq++
		kind := m.kindOf(q)
		if kind == "" {
			kind = "absolute"
		}
		ni := &item{ID: m.seq, Key: key, CID: req.CID, QRef: q, Kind: kind, State: "queued", Created: now, Deadline: deadlineFor(kind, now),
			AttemptID: aid, SessionRef: sref, Answer: canon[i], Review: "none", AnsweredAt: answered[i], AttKind: req.Items[i].AttemptKind}
		if req.Items[i].Rev != nil {
			ni.Rev = *req.Items[i].Rev
		}
		m.items[ni.ID] = ni
		m.byKey[key] = ni
		wrote = true
		m.Stats["enqueue_new_item"]++
		o["item_id"], o["state"], o["version"], o["created"] = ni.ID, "queued", 0, true
		o["attempt_id"], o["new_attempt_written"] = aid, newWritten
		o["daily_remaining"] = max(0, 3-m.cfg.Env.DayCount(q))
		o["question_kind"], o["deadline_at"] = kind, ni.Deadline.UTC().Format(time.RFC3339)
		out = append(out, o)
	}
	st := 200
	if wrote {
		st = 201
	}
	resp := map[string]any{"written": wrote, "client_attempt_id": req.CID, "items": out}
	if sref != "" {
		resp["session_ref"] = sref
		if req.SessionTotal != nil {
			resp["session_total"] = *req.SessionTotal
		}
	}
	jok(w, st, resp)
}

// ---- #2/#3 reads ------------------------------------------------------------------------

func (m *Mock) sessionSummary(sref string) map[string]any {
	by := map[string]int{}
	total := 0
	proc := 0
	for _, it := range m.items {
		if it.SessionRef != sref {
			continue
		}
		total++
		by[m.apiState(it)]++
		if processed(m.apiState(it)) {
			proc++
		}
	}
	s := m.sessions[sref]
	var tot any
	done := false
	if s != nil && s.Total > 0 {
		tot = s.Total
		done = proc >= s.Total
	}
	return map[string]any{"total": tot, "processed": proc, "done": done, "by_state": by}
}

func (m *Mock) detail(it *item, include bool, summary bool) map[string]any {
	st := m.apiState(it)
	d := map[string]any{"item_id": it.ID, "qref": it.QRef, "state": st, "question_kind": it.Kind, "subject": "math",
		"session_ref": nilIfEmpty(it.SessionRef), "created_at": it.Created.UTC().Format(time.RFC3339),
		"deadline_at": it.Deadline.UTC().Format(time.RFC3339), "needs_manual": it.NeedsManual, "independent_candidate": it.Compare == "match",
		"compare_outcome": nilIfEmpty(it.Compare), "purged": it.Purged, "reanalyze_enabled": false,
		"review":          map[string]any{"status": it.Review, "verdict": nilIfEmpty(it.Verdict), "reviewer_kind": nil, "reviewed_at": nil},
		"display_version": 0, "adopted_version": nil}
	if it.Proposed != "" && !it.Purged {
		d["display_version"] = 1
	}
	if !summary {
		d["remaining"] = map[string]any{"item_versions": 2, "question_today": max(0, 3-m.cfg.Env.DayCount(it.QRef)), "brain_ai_today": 18}
		d["images"] = []any{}
		d["versions"] = []any{}
	} else {
		d["version_count"], d["image_count"] = 0, 0
	}
	if include && !it.Purged && it.Proposed != "" {
		d["result"] = map[string]any{"version": 1, "proposed_answer": it.Proposed, "rationale": it.Rationale, "verdict": "unclear", "score": nil, "confidence": nil}
	}
	return d
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *Mock) handleDetail(w http.ResponseWriter, r *http.Request) {
	m.bump("detail_calls")
	if !m.needWrite(w, r) {
		return
	}
	m.mu.Lock()
	off := m.detailOff
	m.mu.Unlock()
	if off || r.PathValue("slug") != m.cfg.Brain {
		jerr(w, 404, "not_found", "not found", nil, nil, nil)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.items[id]
	if it == nil {
		jerr(w, 404, "not_found", "item not found", nil, nil, nil)
		return
	}
	inc := r.URL.Query().Get("include") == "result"
	if inc {
		m.Stats["detail_with_result"]++
	}
	jok(w, 200, m.detail(it, inc, false))
}

func (m *Mock) handleBatch(w http.ResponseWriter, r *http.Request) {
	m.bump("batch_calls")
	if !m.needWrite(w, r) {
		return
	}
	m.mu.Lock()
	off := m.batchOff
	m.mu.Unlock()
	if off || r.PathValue("slug") != m.cfg.Brain {
		jerr(w, 404, "not_found", "not found", nil, nil, nil)
		return
	}
	sref := r.URL.Query().Get("session_ref")
	if sref == "" {
		jerr(w, 422, "filter_required", "session_ref required", nil, nil, nil)
		return
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err != nil || n < 1 || n > 100 {
			jerr(w, 400, "validation_failed", "limit", nil, nil, nil)
			return
		}
	}
	inc := r.URL.Query().Get("include") == "result"
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for id, it := range m.items {
		if it.SessionRef == sref {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, m.detail(m.items[id], inc, true))
	}
	jok(w, 200, map[string]any{"session_ref": sref, "session": m.sessionSummary(sref), "items": items, "next_after": nil})
}

func (m *Mock) handleCaps(w http.ResponseWriter, r *http.Request) {
	m.bump("caps_calls")
	if !m.needWrite(w, r) {
		return
	}
	j := m.judgedOn()
	jok(w, 200, map[string]any{"brain": m.cfg.Brain,
		"kinds":     map[string]any{"absolute": map[string]any{"enabled": true, "deadline_hours": 24}, "judged": map[string]any{"enabled": j, "deadline_hours": 48}},
		"images":    map[string]any{"enabled": m.imagesOn(), "max_per_item": 6},
		"limits":    map[string]any{"enqueue_items_max": 100, "session_total_max": 100, "open_cap": m.openCap, "daily_writes_per_question": 3, "batch_read_max": 100, "events_page_max": 200, "events_poll_min_s": 30},
		"reanalyze": map[string]any{"enabled": false}})
}

// ---- #5/#6 events -------------------------------------------------------------------------

func (m *Mock) handleEvents(w http.ResponseWriter, r *http.Request) {
	m.bump("events_calls")
	if !m.needNotify(w, r) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.eventsFail > 0 {
		m.eventsFail--
		jerr(w, 500, "internal", "boom", nil, nil, nil)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 200 {
			limit = n
		}
	}
	incAcked := m.includeAcked || r.URL.Query().Get("include_acked") == "true"
	var out []map[string]any
	var last int64
	more := false
	for _, e := range m.events {
		if e.ID <= after || (!incAcked && e.ID <= m.acked) {
			continue
		}
		if len(out) >= limit {
			more = true
			break
		}
		last = e.ID
		out = append(out, map[string]any{"id": e.ID, "event_type": e.Type, "item_id": e.ItemID, "created_at": e.Created.UTC().Format(time.RFC3339), "payload": e.Payload})
	}
	var next any
	if last > 0 {
		next = last
	}
	if out == nil {
		out = []map[string]any{}
	}
	jok(w, 200, map[string]any{"events": out, "next_after": next, "has_more": more})
}

func (m *Mock) handleAck(w http.ResponseWriter, r *http.Request) {
	m.bump("ack_calls")
	if !m.needNotify(w, r) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	// up_to_id must be a JSON integer: not a string, not a float, not ids[] (parameter errors are 400)
	var req struct {
		UpTo json.RawMessage `json:"up_to_id"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		m.bump("ack_400")
		jerr(w, 400, "validation_failed", "invalid body", nil, nil, nil)
		return
	}
	raw := strings.TrimSpace(string(req.UpTo))
	up, err := strconv.ParseInt(raw, 10, 64) // a quoted string, a float, null or a missing field never parse
	if err != nil {
		m.bump("ack_400")
		jerr(w, 400, "validation_failed", "up_to_id must be an integer", map[string]any{"field": "up_to_id"}, nil, nil)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ackFail > 0 {
		m.ackFail--
		m.Stats["ack_failed"]++
		jerr(w, 500, "internal", "boom", nil, nil, nil)
		return
	}
	if up <= 0 {
		jerr(w, 422, "invalid_ack", "invalid up_to_id", nil, nil, nil)
		return
	}
	var head int64
	for _, e := range m.events {
		if e.ID > head {
			head = e.ID
		}
	}
	if m.evSeq > head {
		head = m.evSeq
	}
	if up > head {
		jerr(w, 422, "ack_beyond_head", "ack beyond head", nil, nil, nil)
		return
	}
	n := 0
	if up > m.acked {
		for _, e := range m.events {
			if e.ID > m.acked && e.ID <= up {
				n++
			}
		}
		m.acked = up
	}
	m.Stats["ack_ok"]++
	jok(w, 200, map[string]any{"acked": n, "up_to_id": up, "head_id": head})
}

// ---- control (tests / acceptance scripts) -------------------------------------------------

func (m *Mock) emit(typ string, it *item, errCode string) {
	m.evSeq++
	st := m.apiState(it)
	pl := map[string]any{"qref": it.QRef, "attempt_id": it.AttemptID, "state": st, "session_ref": nilIfEmpty(it.SessionRef),
		"question_kind": it.Kind, "compare_outcome": nilIfEmpty(it.Compare), "needs_manual": it.NeedsManual,
		"independent_candidate": it.Compare == "match", "error_code": nilIfEmpty(errCode)}
	if !m.noSession {
		s := m.sessions[it.SessionRef]
		sum := m.sessionSummary(it.SessionRef)
		if s != nil && s.Total > 0 {
			pl["session_total"] = s.Total
			pl["session_processed"] = sum["processed"]
			done := false
			if sum["processed"].(int) >= s.Total && !s.Fired {
				done, s.Fired = true, true
			}
			pl["session_done"] = done
		} else {
			pl["session_total"], pl["session_done"] = nil, nil
			pl["session_processed"] = sum["processed"]
		}
	}
	m.events = append(m.events, event{ID: m.evSeq, Type: typ, ItemID: it.ID, Payload: pl, Created: m.cfg.Env.Now()})
}

// Advance moves an item (by id, or by qref when id=0) to a state and emits the
// matching event: compared/judged -> done, failed -> failed, expired -> expired,
// purged -> a silent expired event (only if the item was not final yet).
func (m *Mock) Advance(id int64, qref, to string, opt map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var it *item
	if id > 0 {
		it = m.items[id]
	} else {
		var best *item
		for _, x := range m.items {
			if x.QRef == strings.ToLower(qref) && (best == nil || x.ID > best.ID) {
				best = x
			}
		}
		it = best
	}
	if it == nil {
		return fmt.Errorf("no such item")
	}
	if v := opt["answer"]; v != "" {
		it.Proposed = v
	}
	if v := opt["rationale"]; v != "" {
		it.Rationale = v
	}
	if v := opt["compare"]; v != "" {
		it.Compare = v
	}
	if v := opt["verdict"]; v != "" {
		it.Verdict, it.Review = v, "confirmed"
	}
	was := m.apiState(it)
	switch to {
	case "claimed", "proposed", "queued":
		it.State = to
	case "compared", "judged":
		it.State = to
		// the real brain proposes nothing when the assistants disagree (mismatch) or there are fewer than two (n/a);
		// no_answer=1 also strips a match (the abnormal "match without text" case)
		if it.Proposed == "" && opt["no_answer"] == "" && it.Compare != "mismatch" && it.Compare != "n/a" {
			it.Proposed, it.Rationale = "（模拟）AI 拟答案", "（模拟）AI 解析"
		}
		if to == "compared" && it.Compare == "" {
			it.Compare = "match"
		}
		it.NeedsManual = it.Compare == "mismatch" || it.Compare == "n/a"
		m.emit("done", it, "")
	case "reviewed":
		it.State = "reviewed"
		it.Review = "confirmed"
	case "failed":
		it.State, it.NeedsManual = "failed", true
		m.emit("failed", it, "")
	case "expired":
		it.State, it.NeedsManual = "expired", true
		m.emit("expired", it, "")
	case "purged":
		it.Purged = true
		if !processed(was) {
			it.State = "expired"
			m.emit("expired", it, "purged")
		}
	default:
		return fmt.Errorf("bad state")
	}
	return nil
}

// ItemID returns the newest item id of a qref (0 = none).
func (m *Mock) ItemID(qref string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var id int64
	for _, x := range m.items {
		if x.QRef == strings.ToLower(qref) && x.ID > id {
			id = x.ID
		}
	}
	return id
}

// Set changes a mock switch (see the control endpoint for the names).
func (m *Mock) Set(k, v string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := v == "1" || v == "true" || v == "on"
	switch k {
	case "judged":
		m.cfgMu.Lock()
		m.judged = b
		m.cfgMu.Unlock()
	case "images":
		m.cfgMu.Lock()
		m.images = b
		m.cfgMu.Unlock()
	case "enqueue_mode":
		m.enqueueMode = v
	case "mismatch_status":
		m.mismatch422 = v == "422"
	case "batch_off":
		m.batchOff = b
	case "detail_off":
		m.detailOff = b
	case "include_acked":
		m.includeAcked = b
	case "ack_fail":
		m.ackFail, _ = strconv.Atoi(v)
	case "events_fail":
		m.eventsFail, _ = strconv.Atoi(v)
	case "open_cap":
		m.openCap, _ = strconv.Atoi(v)
	case "no_session_fields":
		m.noSession = b
	case "slow_ms":
		n, _ := strconv.Atoi(v)
		m.slow = time.Duration(n) * time.Millisecond
	}
}

// SetKind makes a question judged (open) or absolute (math).
func (m *Mock) SetKind(qref, kind string) {
	m.cfgMu.Lock()
	m.kinds[strings.ToLower(qref)] = kind
	m.cfgMu.Unlock()
}

// DropEvents removes every event the consumer has not acked (an outbox that lost them).
func (m *Mock) DropEvents() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keep []event
	n := 0
	for _, e := range m.events {
		if e.ID <= m.acked {
			keep = append(keep, e)
		} else {
			n++
		}
	}
	m.events = keep
	return n
}

// Counter reads one statistic.
func (m *Mock) Counter(k string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Stats[k]
}

// Acked is the consumer's ack cursor.
func (m *Mock) Acked() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked
}

// OpenCount of items by state (for assertions).
func (m *Mock) StateOf(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it := m.items[id]; it != nil {
		return m.apiState(it)
	}
	return ""
}

// Register adds the queue routes (and /_mock/queue/*) to mux.
func (m *Mock) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/bank/pending-answers", m.handleEnqueue)
	mux.HandleFunc("GET /v1/brains/{slug}/pending/{id}", m.handleDetail)
	mux.HandleFunc("GET /v1/brains/{slug}/pending", m.handleBatch)
	mux.HandleFunc("GET /v1/queue/capabilities", m.handleCaps)
	mux.HandleFunc("GET /v1/queue/events", m.handleEvents)
	mux.HandleFunc("POST /v1/queue/events/ack", m.handleAck)
	mux.HandleFunc("GET /v1/queue/stats", func(w http.ResponseWriter, r *http.Request) { // no site token may read it
		m.bump("stats_calls")
		m.auth(w, r)
		if w.Header().Get("Content-Type") == "" {
			jerr(w, 403, "forbidden", "forbidden", nil, nil, nil)
		}
	})
	mux.HandleFunc("POST /_mock/queue/advance", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("item"), 10, 64)
		opt := map[string]string{"answer": q.Get("answer"), "rationale": q.Get("rationale"), "compare": q.Get("compare"), "verdict": q.Get("verdict")}
		if err := m.Advance(id, q.Get("qref"), q.Get("to"), opt); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /_mock/queue/config", func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.URL.Query() {
			m.Set(k, v[0])
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /_mock/queue/kind", func(w http.ResponseWriter, r *http.Request) {
		m.SetKind(r.URL.Query().Get("qref"), r.URL.Query().Get("kind"))
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /_mock/queue/drop-events", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d\n", m.DropEvents())
	})
	mux.HandleFunc("GET /_mock/queue/stats", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		jok(w, 200, m.Stats)
	})
	mux.HandleFunc("GET /_mock/queue/items", func(w http.ResponseWriter, r *http.Request) { // ids/states only
		m.mu.Lock()
		defer m.mu.Unlock()
		var list []map[string]any
		for _, it := range m.items {
			list = append(list, map[string]any{"item_id": it.ID, "qref": it.QRef, "state": m.apiState(it), "session_ref": it.SessionRef})
		}
		sort.Slice(list, func(i, j int) bool { return list[i]["item_id"].(int64) < list[j]["item_id"].(int64) })
		jok(w, 200, list)
	})
}

// Requests returns every enqueue body received so far.
func (m *Mock) Requests() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.reqs...)
}

// ItemsOf lists item ids of a session_ref in id order.
func (m *Mock) ItemsOf(sref string) []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []int64
	for id, it := range m.items {
		if it.SessionRef == sref {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
