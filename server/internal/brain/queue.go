package brain

// Pending-answer queue adapter (单 S1). THE WHOLE WIRE SHAPE OF THE QUEUE CALLS
// LIVES IN THIS FILE, so that any later change of the brain's contract touches
// this one file. Source of truth: 大脑《练习站 ↔ 如意大脑「待解答队列」站点接口契约 v1.0》
// (site-interface-contract.md). Nothing here has run against a deployed brain yet:
// it is exercised against internal/mockqueue only.
//
//	B1  POST {base}{pendingPath}                       write token   (契约 #1)
//	    {"brain","client_attempt_id":<cid>,"session_ref":<cid>,"session_total":n,
//	     "items":[{"qref","answer","rev"?,"attempt_kind"?,"answered_at"?}]}   (no images in S1)
//	    201|200 {"written","items":[{"qref","ok":true,"item_id","state","version","created","attempt_id",
//	                                  "new_attempt_written","daily_remaining","question_kind","deadline_at"}]}
//	    422|429 whole batch NOT written: {"error":{"code","details":{"retry_after_s"}},"written":false,
//	                                  "items":[{"qref","ok","reason","queueable_reason","reasons"?}]}
//	B2  GET  {base}/v1/brains/{slug}/pending/{item_id}[?include=result]    write token   (#2)
//	B3  GET  {base}/v1/brains/{slug}/pending?session_ref=&limit=100[&include=result]  write token  (#3)
//	B4  GET  {base}/v1/queue/capabilities?brain=<slug>                     write token   (#4)
//	B5  GET  {base}/v1/queue/events?after=<id>&limit=<n>                   NOTIFY token  (#5)
//	B6  POST {base}/v1/queue/events/ack {"up_to_id":n}                     NOTIFY token  (#6)
//
// Tokens: B1–B4 use the write token (the same one as grade); B5/B6 use the
// notify token ONLY. A 401/403 is never retried with another token. The site
// never calls stats, void, claim/heartbeat/proposals, reanalyze or answer-media
// (the last two are S3/S2).
//
// ---- 仍待大脑确认/可能与终稿不同处（契约 v1.0 之外的假设，只影响本文件）---------
//  C1  入队成功项的 `ok`、`new_attempt_written`、`daily_remaining` 缺省时：站点按
//      grade 状态推断“新写作答”(unsupported)，并退回本地估算“约还剩N次”。
//  C2  批量读/详情里的 `result` 只在 include=result 时出现；`review.verdict` 在 ①b 前恒 none。
//  C3  `kinds.<kind>.deadline_hours` 只用于文案兜底（入队响应的 deadline_at 优先）。
//  C4  事件/详情未知 state 值 → 页面“状态更新中”；未知 reason → 视为该条永久不可入队。
//  C5  item_id 契约示例为整数；两种（整数/字符串）都收，统一为字符串。
//  C6  session_total=本次批内条数（剔除坏条目后重提时同步改小），后台重试同值。
// ---------------------------------------------------------------------------------

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Paths and limits of the queue contract.
const (
	DefaultPendingPath = "/v1/bank/pending-answers"
	queueEventsPath    = "/v1/queue/events"
	queueAckPath       = "/v1/queue/events/ack"
	queueCapsPath      = "/v1/queue/capabilities"
	// MaxEventsPage is the page size asked of the events endpoint.
	MaxEventsPage = 100
	// MaxBySession is the n≤100 of the batch read (and of session_total).
	MaxBySession = 100

	maxQueueBytes  = 4 << 20 // a batch read with include=result can carry 100 texts
	maxQueueErrLen = 16 << 10
	// EnqueueTimeout bounds one enqueue call: the grade request that triggers it
	// must not hang on the queue.
	EnqueueTimeout = 3 * time.Second
	readTimeout    = 8 * time.Second
	maxTextLen     = 4000
)

// Item states of the brain's queue (契约 §3; purged is derived by the brain's API layer).
const (
	StateQueued   = "queued"
	StateClaimed  = "claimed"
	StateProposed = "proposed"
	StateCompared = "compared"
	StateJudged   = "judged"
	StateReviewed = "reviewed"
	StateFailed   = "failed"
	StateExpired  = "expired"
	StatePurged   = "purged"
)

// Event types of the outbox.
const (
	EventDone       = "done"
	EventFailed     = "failed"
	EventExpired    = "expired"
	EventReanalyzed = "reanalyzed"
	EventStaleRev   = "stale_rev"
)

// Question kinds.
const (
	KindAbsolute = "absolute" // math with a computable answer: 24 h
	KindJudged   = "judged"   // open question: 48 h
)

// Reasons of a refused item / batch (契约 §5), plus the static ones grade reports.
const (
	ReasonAnswerRequired       = "answer_required"
	ReasonImagesNotEnabled     = "images_not_enabled"
	ReasonKindNotEnabled       = "kind_not_enabled"
	ReasonQuestionNotFound     = "question_not_found"
	ReasonRevMismatch          = "rev_mismatch"
	ReasonMediaNotFound        = "media_not_found"
	ReasonSessionTotalMismatch = "session_total_mismatch"
	ReasonDailyCap             = "daily_cap"
	ReasonQueueFull            = "queue_full"
	ReasonNotNeeded            = "not_needed" // grade: already graded, nothing to queue
	// site-side reasons (never sent by the brain)
	ReasonNotReady = "not_ready"
)

var (
	// ErrNotifyDisabled: no notify token configured.
	ErrNotifyDisabled = errors.New("notify token not configured")

	itemIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reasonRe2 = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

// Queue is the site's view of the queue calls (implemented by *Client, faked in tests).
type Queue interface {
	Enqueue(ctx context.Context, req EnqueueRequest) (*EnqueueResponse, error)
	PendingDetail(ctx context.Context, itemID string, includeResult bool) (*Pending, error)
	PendingBySession(ctx context.Context, sessionRef string, includeResult bool) (*SessionRead, error)
	Capabilities(ctx context.Context) (*Capabilities, error)
	QueueEvents(ctx context.Context, after int64, limit int) ([]QueueEvent, error)
	AckEvents(ctx context.Context, upTo int64) error
}

var _ Queue = (*Client)(nil)

// EnqueueItem is one answer to queue: the SAME qref/answer/rev/kind/answered_at
// that grade got, so the shared key ps:<cid>:<qref> converges.
type EnqueueItem struct {
	QRef        string
	Answer      any
	Rev         int
	AttemptKind string
	AnsweredAt  time.Time
	// Images: sha256 of answer images already PUT to the brain (≤6). Empty = not sent.
	Images []string
}

// EnqueueRequest is one batch (all or nothing at the brain). SessionRef is
// sent at the top level only; SessionTotal (1..100) must be the same for every
// batch of one session_ref.
type EnqueueRequest struct {
	ClientAttemptID string
	SessionRef      string
	SessionTotal    int
	Items           []EnqueueItem
}

// EnqueuedItem is the brain's confirmation of one queued answer.
type EnqueuedItem struct {
	QRef              string
	ItemID            string
	State             string
	Version           int
	Created           bool
	AttemptID         int64
	NewAttemptWritten bool
	DailyRemaining    int // -1 = the brain did not say
	DeadlineAt        time.Time
	Kind              string // question_kind
}

// EnqueueResponse is the confirmed reply (every item we sent is answered).
type EnqueueResponse struct{ Items []EnqueuedItem }

// Reject is the brain's verdict on one item of a refused batch.
type Reject struct {
	QRef            string
	OK              bool
	Reason          string
	QueueableReason string
}

// Permanent: this one item can never be queued as it is (drop it and resubmit
// the rest). A refused item without queueable_reason is a batch-level or
// transient matter (queue_full, session_total_mismatch) UNLESS its reason is
// one of the per-item content reasons below, or the daily cap of its question.
func (r Reject) Permanent() bool {
	if r.OK {
		return false
	}
	if r.QueueableReason != "" {
		return true
	}
	switch r.Reason {
	case ReasonAnswerRequired, ReasonImagesNotEnabled, ReasonKindNotEnabled, ReasonQuestionNotFound,
		ReasonRevMismatch, ReasonMediaNotFound, ReasonDailyCap:
		return true
	}
	return false
}

// Why is the reason to show/store for a permanent rejection.
func (r Reject) Why() string {
	if r.QueueableReason != "" {
		return r.QueueableReason
	}
	return r.Reason
}

// QueueError is an *Error plus what the queue endpoints add to a refusal.
type QueueError struct {
	Base        *Error
	Reason      string   // error.code ([a-z_]), e.g. images_not_enabled, queue_full, daily_cap
	Items       []Reject // per-item verdicts of a refused batch
	RetryAfterS int      // error.details.retry_after_s, or the Retry-After header; 0 = absent
}

func (e *QueueError) Unwrap() error { return e.Base }

// Error is the fixed, sanitized text of the underlying *Error.
func (e *QueueError) Error() string { return e.Base.Error() }

func (c *Client) pendingPath() string {
	if c.pendingPth == "" {
		return DefaultPendingPath
	}
	return c.pendingPth
}

// queueDo performs one request and returns the bounded JSON body of a 200/201/202.
// Any other status becomes a *QueueError (body parsed with sanitizing only).
func (c *Client) queueDo(ctx context.Context, tok, method, rawURL string, body []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad upstream request"}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ruyi-site/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxQueueErrLen))
		resp.Body.Close()
		return nil, parseQueueError(resp.StatusCode, resp.Header, raw)
	}
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxQueueBytes+1))
	resp.Body.Close()
	if rerr != nil || len(b) > maxQueueBytes {
		return nil, &Error{Kind: KindUnavailable, What: "bad queue reply body"}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil, &Error{Kind: KindUnavailable, What: "queue reply is not JSON"}
	}
	return b, nil
}

type rejectWire struct {
	QRef            string  `json:"qref"`
	OK              bool    `json:"ok"`
	Reason          *string `json:"reason"`
	QueueableReason *string `json:"queueable_reason"`
}

func (w rejectWire) reject() (Reject, bool) {
	r := Reject{QRef: strings.ToLower(strings.TrimSpace(w.QRef)), OK: w.OK}
	if w.Reason != nil && reasonRe2.MatchString(*w.Reason) {
		r.Reason = *w.Reason
	}
	if w.QueueableReason != nil && reasonRe2.MatchString(*w.QueueableReason) {
		r.QueueableReason = *w.QueueableReason
	}
	return r, r.QRef != "" && len(r.QRef) <= maxQRefLen
}

// parseQueueError reuses upstreamErr for the classified *Error (it understands
// the {"error":{"code","message","details"}} envelope) and adds the queue parts.
// Only [a-z_] codes and bounded numbers are kept; no text of the reply is ever
// put into the error.
func parseQueueError(status int, h http.Header, raw []byte) error {
	base := upstreamErr(&http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(raw))})
	qe := &QueueError{Base: base}
	var v struct {
		Items []rejectWire `json:"items"`
		Error struct {
			Code    string `json:"code"`
			Details struct {
				RetryAfterS json.RawMessage `json:"retry_after_s"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &v) == nil {
		if reasonRe2.MatchString(v.Error.Code) {
			qe.Reason = v.Error.Code
		}
		for _, it := range v.Items {
			if len(qe.Items) >= MaxBySession {
				break
			}
			if r, ok := it.reject(); ok {
				qe.Items = append(qe.Items, r)
			}
		}
		var n int
		if raw := v.Error.Details.RetryAfterS; len(raw) > 0 && json.Unmarshal(raw, &n) == nil && n > 0 && n <= 86400 {
			qe.RetryAfterS = n
		}
	}
	if qe.RetryAfterS == 0 && base.RetryAfter > 0 {
		qe.RetryAfterS = int(base.RetryAfter / time.Second)
	}
	return qe
}

// ValidateEnqueue enforces the local limits (same as grade) so that a request
// the brain would refuse is never sent.
func ValidateEnqueue(r EnqueueRequest) error {
	if r.SessionRef != "" && !clientIDRe.MatchString(r.SessionRef) {
		return &Error{Kind: KindBadRequest, What: "bad session_ref"}
	}
	if r.SessionTotal < 0 || r.SessionTotal > MaxBySession {
		return &Error{Kind: KindBadRequest, What: "bad session_total"}
	}
	g := GradeRequest{ClientAttemptID: r.ClientAttemptID}
	for _, it := range r.Items {
		if len(it.Images) > MaxAnswerImages {
			return &Error{Kind: KindBadRequest, What: "too many images"}
		}
		for _, h := range it.Images {
			if !ValidSHA256(h) {
				return &Error{Kind: KindBadRequest, What: "bad image sha"}
			}
		}
		g.Items = append(g.Items, GradeItem{QRef: it.QRef, Answer: it.Answer, Rev: it.Rev, AttemptKind: it.AttemptKind})
	}
	return ValidateGradeRequest(g)
}

type imageRef struct {
	SHA256 string `json:"sha256"`
}

func encodeEnqueue(r EnqueueRequest, brainID string, now time.Time) ([]byte, error) {
	type item struct {
		QRef        string `json:"qref"`
		Answer      any    `json:"answer"`
		Rev         int    `json:"rev,omitempty"`
		AttemptKind string `json:"attempt_kind,omitempty"`
		AnsweredAt  string `json:"answered_at,omitempty"`
		// docs/media-api.md (大脑 PR #82): images[] = [{"sha256"}] (id or sha256; the site sends sha256 only).
		Images []imageRef `json:"images,omitempty"`
	}
	items := make([]item, 0, len(r.Items))
	for _, it := range r.Items {
		o := item{QRef: WireQRef(it.QRef), Answer: it.Answer, AttemptKind: it.AttemptKind}
		for _, h := range it.Images {
			o.Images = append(o.Images, imageRef{SHA256: h})
		}
		if it.Rev >= 1 {
			o.Rev = it.Rev
		}
		if AnsweredAtSendable(it.AnsweredAt, now) {
			o.AnsweredAt = it.AnsweredAt.UTC().Format(time.RFC3339)
		}
		items = append(items, o)
	}
	m := map[string]any{"brain": brainID, "client_attempt_id": r.ClientAttemptID, "items": items}
	if r.SessionRef != "" { // session_ref only at the top level; null/absent for a one-off answer
		m["session_ref"] = r.SessionRef
		if r.SessionTotal >= 1 {
			m["session_total"] = r.SessionTotal
		}
	}
	return json.Marshal(m)
}

// idString accepts a JSON number or string and returns it as a string id.
func idString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if itemIDRe.MatchString(s) {
			return s
		}
		return ""
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return strconv.FormatInt(n, 10)
	}
	return ""
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Enqueue queues the answers (B1). It is called ONCE per attempt right after
// grade, with a hard 3 s budget and no internal retry: a failure is returned to
// the caller, which records it and retries in the background. A refused batch
// returns a *QueueError (nothing was written at the brain).
func (c *Client) Enqueue(ctx context.Context, req EnqueueRequest) (*EnqueueResponse, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	if err := ValidateEnqueue(req); err != nil {
		return nil, err
	}
	body, err := encodeEnqueue(req, c.id(), c.now())
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, What: "bad enqueue request"}
	}
	b, err := c.queueDo(ctx, c.gradeToken, http.MethodPost, c.endpoint(c.pendingPath(), nil), body, EnqueueTimeout)
	if err != nil {
		return nil, err
	}
	var v struct {
		Items []struct {
			QRef              string          `json:"qref"`
			OK                *bool           `json:"ok"`
			ItemID            json.RawMessage `json:"item_id"`
			State             string          `json:"state"`
			Version           int             `json:"version"`
			Created           bool            `json:"created"`
			AttemptID         json.RawMessage `json:"attempt_id"`
			NewAttemptWritten bool            `json:"new_attempt_written"`
			DailyRemaining    *int            `json:"daily_remaining"`
			DeadlineAt        string          `json:"deadline_at"`
			Kind              string          `json:"question_kind"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed enqueue reply"}
	}
	orig := map[string]string{}
	for _, it := range req.Items {
		orig[WireQRef(it.QRef)] = it.QRef
	}
	out := &EnqueueResponse{}
	seen := map[string]bool{}
	for _, it := range v.Items {
		o, ok := orig[strings.ToLower(it.QRef)]
		id := idString(it.ItemID)
		if !ok || seen[o] || id == "" || (it.OK != nil && !*it.OK) { // never trust qrefs we did not send, nor an item without an id
			continue
		}
		seen[o] = true
		var aid int64
		if json.Unmarshal(it.AttemptID, &aid) != nil || aid < 1 {
			aid = 0
		}
		e := EnqueuedItem{QRef: o, ItemID: id, State: it.State, Version: it.Version, Created: it.Created, AttemptID: aid,
			NewAttemptWritten: it.NewAttemptWritten, DailyRemaining: -1, DeadlineAt: parseTime(it.DeadlineAt)}
		if it.DailyRemaining != nil && *it.DailyRemaining >= 0 && *it.DailyRemaining <= 3 {
			e.DailyRemaining = *it.DailyRemaining
		}
		if it.Kind == KindAbsolute || it.Kind == KindJudged {
			e.Kind = it.Kind
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// Pending is what the page may show about one queued answer.
type Pending struct {
	ItemID               string
	QRef                 string
	State                string
	Kind                 string
	DeadlineAt           time.Time
	NeedsManual          bool
	IndependentCandidate bool
	SessionRef           string
	DisplayVersion       int
	AdoptedVersion       int
	Purged               bool
	ReanalyzeEnabled     bool
	CompareOutcome       string // match | mismatch | n/a | "" (not compared)
	ReviewStatus         string // none | confirmed | overridden
	ReviewVerdict        string // correct | partial | wrong | unclear | ""
	ProposedAnswer       string // AI first-pass answer as text (only with include=result)
	Rationale            string
	HasResult            bool // the reply carried a result object
	QuestionToday        int  // remaining.question_today, -1 = absent
}

type pendingWire struct {
	ItemID               json.RawMessage `json:"item_id"`
	QRef                 string          `json:"qref"`
	State                string          `json:"state"`
	Kind                 string          `json:"question_kind"`
	DeadlineAt           string          `json:"deadline_at"`
	NeedsManual          bool            `json:"needs_manual"`
	IndependentCandidate bool            `json:"independent_candidate"`
	SessionRef           *string         `json:"session_ref"`
	DisplayVersion       int             `json:"display_version"`
	AdoptedVersion       int             `json:"adopted_version"`
	Purged               bool            `json:"purged"`
	ReanalyzeEnabled     bool            `json:"reanalyze_enabled"`
	CompareOutcome       *string         `json:"compare_outcome"`
	Review               *struct {
		Status  string  `json:"status"`
		Verdict *string `json:"verdict"`
	} `json:"review"`
	Remaining *struct {
		QuestionToday *int `json:"question_today"`
	} `json:"remaining"`
	Result *struct {
		ProposedAnswer json.RawMessage `json:"proposed_answer"`
		Rationale      string          `json:"rationale"`
	} `json:"result"`
}

func (w pendingWire) pending() Pending {
	p := Pending{ItemID: idString(w.ItemID), QRef: strings.ToLower(w.QRef), State: w.State, DeadlineAt: parseTime(w.DeadlineAt),
		NeedsManual: w.NeedsManual, IndependentCandidate: w.IndependentCandidate, DisplayVersion: w.DisplayVersion,
		AdoptedVersion: w.AdoptedVersion, Purged: w.Purged, ReanalyzeEnabled: w.ReanalyzeEnabled, QuestionToday: -1}
	if w.State == StatePurged {
		p.Purged = true
	}
	if w.Kind == KindAbsolute || w.Kind == KindJudged {
		p.Kind = w.Kind
	}
	if w.SessionRef != nil && clientIDRe.MatchString(*w.SessionRef) {
		p.SessionRef = *w.SessionRef
	}
	if w.CompareOutcome != nil {
		switch *w.CompareOutcome {
		case "match", "mismatch", "n/a":
			p.CompareOutcome = *w.CompareOutcome
		}
	}
	if w.Review != nil {
		switch w.Review.Status {
		case "none", "confirmed", "overridden":
			p.ReviewStatus = w.Review.Status
		}
		if w.Review.Verdict != nil {
			switch *w.Review.Verdict {
			case "correct", "partial", "wrong", "unclear":
				p.ReviewVerdict = *w.Review.Verdict
			}
		}
	}
	if w.Remaining != nil && w.Remaining.QuestionToday != nil && *w.Remaining.QuestionToday >= 0 && *w.Remaining.QuestionToday <= 3 {
		p.QuestionToday = *w.Remaining.QuestionToday
	}
	if w.Result != nil && !p.Purged {
		p.HasResult = true
		p.ProposedAnswer = AnswerText(w.Result.ProposedAnswer)
		p.Rationale = clipText(w.Result.Rationale)
	}
	return p
}

func clipText(s string) string {
	s = strings.ToValidUTF8(s, "")
	if r := []rune(s); len(r) > maxTextLen {
		s = string(r[:maxTextLen])
	}
	return s
}

// AnswerText renders a proposed answer (a string, a list of blanks, a number or
// any other JSON) as short plain text for the page.
func AnswerText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clipText(s)
	}
	var arr []any
	if json.Unmarshal(raw, &arr) == nil {
		parts := make([]string, 0, len(arr))
		for _, x := range arr {
			switch t := x.(type) {
			case string:
				parts = append(parts, t)
			default:
				b, _ := json.Marshal(t)
				parts = append(parts, string(b))
			}
		}
		return clipText(strings.Join(parts, "，"))
	}
	return clipText(string(raw))
}

// PendingDetail reads one item (B2): pure read, latest version. The reply has no
// text unless includeResult.
func (c *Client) PendingDetail(ctx context.Context, itemID string, includeResult bool) (*Pending, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	if !itemIDRe.MatchString(itemID) {
		return nil, &Error{Kind: KindBadRequest, What: "bad item id"}
	}
	var q url.Values
	if includeResult {
		q = url.Values{"include": {"result"}}
	}
	u := c.endpoint("/v1/brains/"+url.PathEscape(c.id())+"/pending/"+itemID, q)
	b, err := c.queueDo(ctx, c.gradeToken, http.MethodGet, u, nil, readTimeout)
	if err != nil {
		return nil, err
	}
	var w pendingWire
	if json.Unmarshal(b, &w) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed pending reply"}
	}
	p := w.pending()
	if p.ItemID == "" {
		p.ItemID = itemID
	}
	return &p, nil
}

// SessionRead is the batch read of one session_ref.
type SessionRead struct {
	Items []Pending
	// Total/Processed/Done are the brain's own session summary (Total 0 = absent).
	Total, Processed int
	Done             bool
}

// PendingBySession reads every item of one session_ref in one call (B3, n≤100).
// A route that is not deployed is reported as such (NotReady) so the caller can
// fall back to B2 per item.
func (c *Client) PendingBySession(ctx context.Context, sessionRef string, includeResult bool) (*SessionRead, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	if !clientIDRe.MatchString(sessionRef) {
		return nil, &Error{Kind: KindBadRequest, What: "bad session_ref"}
	}
	q := url.Values{"session_ref": {sessionRef}, "limit": {strconv.Itoa(MaxBySession)}}
	if includeResult {
		q.Set("include", "result")
	}
	u := c.endpoint("/v1/brains/"+url.PathEscape(c.id())+"/pending", q)
	b, err := c.queueDo(ctx, c.gradeToken, http.MethodGet, u, nil, readTimeout)
	if err != nil {
		return nil, err
	}
	var v struct {
		Session *struct {
			Total     *int `json:"total"`
			Processed int  `json:"processed"`
			Done      bool `json:"done"`
		} `json:"session"`
		Items []pendingWire `json:"items"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed pending list"}
	}
	out := &SessionRead{Items: make([]Pending, 0, len(v.Items))}
	if v.Session != nil {
		if v.Session.Total != nil && *v.Session.Total >= 1 && *v.Session.Total <= MaxBySession {
			out.Total = *v.Session.Total
		}
		out.Processed, out.Done = v.Session.Processed, v.Session.Done
	}
	for _, w := range v.Items {
		p := w.pending()
		if p.ItemID != "" && (p.SessionRef == "" || p.SessionRef == sessionRef) && len(out.Items) < MaxBySession {
			out.Items = append(out.Items, p)
		}
	}
	return out, nil
}

// Capabilities is what the brain has switched on (B4). Cache ≤60 s on the caller's side.
type Capabilities struct {
	AbsoluteEnabled bool
	AbsoluteHours   int
	JudgedEnabled   bool
	JudgedHours     int
	ImagesEnabled   bool
	EventsPollMinS  int
	OpenCap         int
}

// Hours is the deadline of a kind in hours (0 = unknown).
func (c *Capabilities) Hours(kind string) int {
	if c == nil {
		return 0
	}
	if kind == KindJudged {
		return c.JudgedHours
	}
	if kind == KindAbsolute {
		return c.AbsoluteHours
	}
	return 0
}

// Capabilities reads GET /v1/queue/capabilities?brain=<slug> (B4).
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	q := url.Values{"brain": {c.id()}}
	b, err := c.queueDo(ctx, c.gradeToken, http.MethodGet, c.endpoint(queueCapsPath, q), nil, readTimeout)
	if err != nil {
		return nil, err
	}
	type kind struct {
		Enabled bool `json:"enabled"`
		Hours   int  `json:"deadline_hours"`
	}
	var v struct {
		Kinds struct {
			Absolute kind `json:"absolute"`
			Judged   kind `json:"judged"`
		} `json:"kinds"`
		Images struct {
			Enabled bool `json:"enabled"`
		} `json:"images"`
		Limits struct {
			EventsPollMinS int `json:"events_poll_min_s"`
			OpenCap        int `json:"open_cap"`
		} `json:"limits"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed capabilities reply"}
	}
	hours := func(h int) int {
		if h < 1 || h > 24*30 {
			return 0
		}
		return h
	}
	return &Capabilities{AbsoluteEnabled: v.Kinds.Absolute.Enabled, AbsoluteHours: hours(v.Kinds.Absolute.Hours),
		JudgedEnabled: v.Kinds.Judged.Enabled, JudgedHours: hours(v.Kinds.Judged.Hours), ImagesEnabled: v.Images.Enabled,
		EventsPollMinS: v.Limits.EventsPollMinS, OpenCap: v.Limits.OpenCap}, nil
}

// QueueEvent is one outbox event (never carries an answer).
type QueueEvent struct {
	ID               int64
	Type             string
	ItemID           string
	QRef             string
	AttemptID        int64
	State            string
	SessionRef       string
	Kind             string
	Compare          string
	NeedsManual      bool
	ErrorCode        string
	SessionTotal     int  // 0 = absent (null)
	SessionProcessed int  // -1 = absent
	SessionDone      bool // true only on the first completion of the whole session
}

// QueueEvents reads events after the cursor (B5), notify token only. By default
// the brain leaves out events this consumer already acknowledged.
func (c *Client) QueueEvents(ctx context.Context, after int64, limit int) ([]QueueEvent, error) {
	if c.notifyTok == "" {
		return nil, ErrNotifyDisabled
	}
	if after < 0 {
		after = 0
	}
	if limit < 1 || limit > MaxEventsPage {
		limit = MaxEventsPage
	}
	q := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
	b, err := c.queueDo(ctx, c.notifyTok, http.MethodGet, c.endpoint(queueEventsPath, q), nil, readTimeout)
	if err != nil {
		return nil, err
	}
	var v struct {
		Events []struct {
			ID      int64           `json:"id"`
			Type    string          `json:"event_type"`
			ItemID  json.RawMessage `json:"item_id"`
			Payload struct {
				QRef             string          `json:"qref"`
				AttemptID        json.RawMessage `json:"attempt_id"`
				State            string          `json:"state"`
				SessionRef       *string         `json:"session_ref"`
				Kind             string          `json:"question_kind"`
				Compare          *string         `json:"compare_outcome"`
				NeedsManual      bool            `json:"needs_manual"`
				ErrorCode        *string         `json:"error_code"`
				SessionTotal     *int            `json:"session_total"`
				SessionProcessed *int            `json:"session_processed"`
				SessionDone      *bool           `json:"session_done"`
			} `json:"payload"`
		} `json:"events"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed events reply"}
	}
	out := make([]QueueEvent, 0, len(v.Events))
	for _, w := range v.Events {
		if w.ID <= 0 {
			continue
		}
		pl := w.Payload
		e := QueueEvent{ID: w.ID, ItemID: idString(w.ItemID), QRef: strings.ToLower(pl.QRef), NeedsManual: pl.NeedsManual, SessionProcessed: -1}
		if reasonRe2.MatchString(w.Type) {
			e.Type = w.Type
		}
		if reasonRe2.MatchString(pl.State) {
			e.State = pl.State
		}
		if pl.SessionRef != nil && clientIDRe.MatchString(*pl.SessionRef) {
			e.SessionRef = *pl.SessionRef
		}
		if pl.Kind == KindAbsolute || pl.Kind == KindJudged {
			e.Kind = pl.Kind
		}
		if pl.ErrorCode != nil && reasonRe2.MatchString(*pl.ErrorCode) {
			e.ErrorCode = *pl.ErrorCode
		}
		if pl.Compare != nil {
			switch *pl.Compare {
			case "match", "mismatch", "n/a":
				e.Compare = *pl.Compare
			}
		}
		var aid int64
		if json.Unmarshal(pl.AttemptID, &aid) == nil && aid > 0 {
			e.AttemptID = aid
		}
		if t := pl.SessionTotal; t != nil && *t >= 1 && *t <= MaxBySession {
			e.SessionTotal = *t
		}
		if d := pl.SessionProcessed; d != nil && *d >= 0 && *d <= MaxBySession {
			e.SessionProcessed = *d
		}
		e.SessionDone = pl.SessionDone != nil && *pl.SessionDone
		out = append(out, e)
	}
	return out, nil
}

// AckEvents acknowledges every event up to and including upTo (B6), idempotent.
// The caller acks only ids it has durably recorded.
func (c *Client) AckEvents(ctx context.Context, upTo int64) error {
	if c.notifyTok == "" {
		return ErrNotifyDisabled
	}
	if upTo < 1 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"up_to_id": upTo})
	_, err := c.queueDo(ctx, c.notifyTok, http.MethodPost, c.endpoint(queueAckPath, nil), body, readTimeout)
	return err
}
