package brain

// Grade adapter. THE WHOLE WIRE CONTRACT OF THE GRADING CALL LIVES IN THIS FILE.
//
// Contract (brain "练习站自动方案接口", POST /v1/bank/grade). The brain is strict:
// an unknown body field, a bad value or a query it does not know is a 400, so
// everything below is checked HERE before sending and nothing else is ever sent.
//
//	POST {base}{gradePath}                 Authorization: Bearer <grade token>
//	{"brain":"ruyi","client_attempt_id":"^[A-Za-z0-9._:-]{1,160}$",
//	 "items":[{"qref"(lower-case),"answer"(string ≤500 chars | [string…≤10, each ≤500]),
//	           "rev"?(int ≥1),"attempt_kind"?:"correction|variant",
//	           "answered_at"?:RFC3339 within [now-7d, now+1min],
//	           "session_ref"?(1..200 chars),"payload"?(JSON, ≤4096 bytes)}, …1..50,
//	          qrefs unique]}                  no query string, no other field
//	-> 201 (something new was written) | 200 (all replays / nothing written)
//	   {"client_attempt_id":<echo>,
//	    "items":[{"qref"(= the string we sent),"status":"graded|pending_key|needs_review|unsupported",
//	              "correct"?:bool (graded only),"attempt_id":<int>,"recorded":bool,"duplicate":bool}, …]}
//	   unsupported => recorded:false, nothing written, no attempt_id.
//	   pending_key / needs_review => the answer is recorded, no right/wrong.
//	-> 404 not_found, message "question not found", details.qrefs: an unknown qref
//	       anywhere in the batch (nothing written). Any 404 WITHOUT details.qrefs
//	       is the catch-all of a brain that does not have the route yet.
//	-> 409 rev_mismatch (details qref/rev/current_rev) | idempotency_conflict
//	       (same key, other answer / grading result / answered_at)
//	-> 429 rate_limited "daily grade limit reached" + Retry-After, details.qrefs
//	       (same question >3 writes per Shanghai day per brain; unsupported does not
//	       count, pending_key/needs_review do; the whole batch is refused)
//	-> 400 invalid_json etc.: never expected from this client; degrades to archive-only.
//
// Idempotency key on the brain side: ps:<client_attempt_id>:<canonical qref>
// (≤198 chars in total). The same id with the same answer replies with
// duplicate:true and the same attempt_id; attempt_kind / session_ref / payload do
// not take part in the comparison, answered_at does when it is sent again with
// another value. Network retries therefore reuse the id (Grade retries once
// itself); a user re-submitting with changed answers must use a NEW id.
//
// The token: the grade token is a separate env value (RUYI_BRAIN_GRADE_TOKEN),
// never equal to the read token, server-side only. Its scope is what the brain
// issued for the practice site (bank:read + write to the single brain "ruyi"
// today; a narrower bank:grade may come later); this code never inspects or
// names a scope.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultGradePath is the contract endpoint path (override with RUYI_BRAIN_GRADE_PATH).
const DefaultGradePath = "/v1/bank/grade"

// Contract limits.
const (
	MaxGradeItems  = 50
	MaxBlanks      = 10
	MaxClientIDLen = 160
	gradeTimeout   = 10 * time.Second
	maxGradeBytes  = 1 << 20
	AttemptCorrect = "correction"
	AttemptVariant = "variant"
	maxSessionRef  = 200
	maxQRefLen     = 300
	MaxAnswerLen   = 500  // characters per answer string / blank
	MaxPayload     = 4096 // bytes of the encoded payload
	maxKeyLen      = 198  // "ps:" + id + ":" + canonical qref

	// answered_at must lie in [now-7d, now+1min] at the brain. Keep a margin on
	// both sides (clock skew, time in flight): 30 s into the future, and one
	// minute short of the 7 days.
	answeredAtMaxAge = 7*24*time.Hour - time.Minute
	answeredAtAhead  = 30 * time.Second
)

// AnsweredAtSendable reports whether t may be sent as answered_at at time now.
// Outside the window the field is simply left out (the brain stamps its own time).
func AnsweredAtSendable(t, now time.Time) bool {
	return !t.IsZero() && !t.After(now.Add(answeredAtAhead)) && t.After(now.Add(-answeredAtMaxAge))
}

// WireQRef is the form of a qref that goes on the wire (qrefs are lower-case).
func WireQRef(q string) string { return strings.ToLower(q) }

var clientIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)

// ErrGradeDisabled: no grade token configured. The site degrades to archive-only.
var ErrGradeDisabled = errors.New("grading not configured")

// Grade statuses.
const (
	StatusGraded      = "graded"
	StatusPendingKey  = "pending_key"
	StatusNeedsReview = "needs_review"
	StatusUnsupport   = "unsupported"
)

// GradeItem is one answer: Answer is a string (single choice, free text) or a
// []string (multiple choice keys, fill-in blanks; ≤ MaxBlanks entries).
type GradeItem struct {
	QRef        string
	Answer      any
	Rev         int       // 0 = not sent
	AttemptKind string    // "", "correction" or "variant"
	AnsweredAt  time.Time // zero = not sent
	SessionRef  string
	Payload     map[string]any
}

// GradeRequest is what the site asks the brain to grade and record.
type GradeRequest struct {
	ClientAttemptID string
	Items           []GradeItem
}

// GradeResult is the verdict for one qref. Correct is nil unless Status is
// graded. Recorded is true only if the brain itself said so (and, where the
// contract promises one, returned an attempt id): the page claims "submitted to
// the brain" for nothing else.
type GradeResult struct {
	QRef      string
	Status    string
	Correct   *bool
	AttemptID int64
	Recorded  bool
	Duplicate bool
}

// GradeResponse is the brain's confirmed reply.
type GradeResponse struct {
	Results []GradeResult
}

// RecordedCount is the number of items the brain recorded.
func (g *GradeResponse) RecordedCount() int {
	n := 0
	for _, r := range g.Results {
		if r.Recorded {
			n++
		}
	}
	return n
}

// ValidateGradeRequest enforces the contract limits locally so that a request
// the brain would refuse is never sent.
func ValidateGradeRequest(r GradeRequest) error {
	bad := func(what string) error { return &Error{Kind: KindBadRequest, What: what} }
	if !clientIDRe.MatchString(r.ClientAttemptID) {
		return bad("bad client_attempt_id")
	}
	if len(r.Items) == 0 || len(r.Items) > MaxGradeItems {
		return bad("items must be 1..50")
	}
	okText := func(s string) bool { return utf8.ValidString(s) && utf8.RuneCountInString(s) <= MaxAnswerLen }
	seen := map[string]bool{}
	for _, it := range r.Items {
		w := WireQRef(it.QRef)
		if w == "" || len(w) > maxQRefLen || seen[w] {
			return bad("bad or duplicate qref")
		}
		if len("ps:")+len(r.ClientAttemptID)+1+len(w) > maxKeyLen {
			return bad("idempotency key too long")
		}
		seen[w] = true
		switch a := it.Answer.(type) {
		case string:
			if !okText(a) {
				return bad("answer too long")
			}
		case []string:
			if len(a) > MaxBlanks {
				return bad("too many blanks")
			}
			for _, x := range a {
				if !okText(x) {
					return bad("answer too long")
				}
			}
		default:
			return bad("bad answer type")
		}
		switch it.AttemptKind {
		case "", AttemptCorrect, AttemptVariant:
		default:
			return bad("bad attempt_kind")
		}
		if it.Rev < 0 {
			return bad("bad rev")
		}
	}
	return nil
}

// encodeGradeRequest builds the one and only body shape the brain accepts.
// Required parts were checked by ValidateGradeRequest; optional ones that do not
// meet the contract (rev < 1, answered_at outside its window, session_ref not
// 1..200 characters, payload over 4096 bytes) are left out rather than sent.
func encodeGradeRequest(r GradeRequest, brainID string, now time.Time) ([]byte, error) {
	type item struct {
		QRef        string          `json:"qref"`
		Answer      any             `json:"answer"`
		Rev         int             `json:"rev,omitempty"`
		AttemptKind string          `json:"attempt_kind,omitempty"`
		AnsweredAt  string          `json:"answered_at,omitempty"`
		SessionRef  string          `json:"session_ref,omitempty"`
		Payload     json.RawMessage `json:"payload,omitempty"`
	}
	items := make([]item, 0, len(r.Items))
	for _, it := range r.Items {
		o := item{QRef: WireQRef(it.QRef), Answer: it.Answer, AttemptKind: it.AttemptKind}
		if it.Rev >= 1 {
			o.Rev = it.Rev
		}
		if n := utf8.RuneCountInString(it.SessionRef); n >= 1 && n <= maxSessionRef && utf8.ValidString(it.SessionRef) {
			o.SessionRef = it.SessionRef
		}
		if AnsweredAtSendable(it.AnsweredAt, now) {
			o.AnsweredAt = it.AnsweredAt.UTC().Format(time.RFC3339)
		}
		if len(it.Payload) > 0 {
			if pb, err := json.Marshal(it.Payload); err == nil && len(pb) <= MaxPayload {
				o.Payload = pb
			}
		}
		items = append(items, o)
	}
	return json.Marshal(map[string]any{"brain": brainID, "client_attempt_id": r.ClientAttemptID, "items": items})
}

func decodeGradeResponse(b []byte, req GradeRequest) (*GradeResponse, error) {
	var v struct {
		ClientAttemptID string `json:"client_attempt_id"`
		Items           []struct {
			QRef      string          `json:"qref"`
			Status    string          `json:"status"`
			Correct   *bool           `json:"correct"`
			AttemptID json.RawMessage `json:"attempt_id"`
			Recorded  bool            `json:"recorded"`
			Duplicate bool            `json:"duplicate"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed grade reply"}
	}
	// Confirmation: the id we sent must be echoed back.
	if v.ClientAttemptID != req.ClientAttemptID {
		return nil, &Error{Kind: KindUnavailable, What: "grade reply lacks brain confirmation"}
	}
	// the brain echoes the string we sent (lower-case); results carry the caller's own qref
	orig := map[string]string{}
	for _, it := range req.Items {
		orig[WireQRef(it.QRef)] = it.QRef
	}
	out := &GradeResponse{}
	seen := map[string]bool{}
	for _, it := range v.Items {
		o, ok := orig[it.QRef]
		if !ok || seen[o] {
			continue // never trust qrefs we did not send
		}
		seen[o] = true
		r := GradeResult{QRef: o, Status: it.Status}
		var aid int64
		if json.Unmarshal(it.AttemptID, &aid) != nil || aid < 1 {
			aid = 0
		}
		switch it.Status {
		case StatusGraded:
			// a verdict is shown only if the brain really recorded it
			if it.Correct == nil || !it.Recorded || aid == 0 {
				r.Status = StatusPendingKey
				r.Recorded = it.Recorded && aid > 0
				r.AttemptID = aid
				break
			}
			r.Correct, r.Recorded, r.AttemptID, r.Duplicate = it.Correct, true, aid, it.Duplicate
		case StatusPendingKey, StatusNeedsReview:
			r.Recorded = it.Recorded && aid > 0
			if r.Recorded {
				r.AttemptID, r.Duplicate = aid, it.Duplicate
			}
		case StatusUnsupport:
			// contract: nothing is written, no attempt id; never claim otherwise
		default:
			r.Status = StatusPendingKey
		}
		out.Results = append(out.Results, r)
	}
	for _, it := range req.Items { // anything the brain skipped stays pending, not recorded
		if !seen[it.QRef] {
			out.Results = append(out.Results, GradeResult{QRef: it.QRef, Status: StatusPendingKey})
		}
	}
	return out, nil
}

// Grade implements Bank. It never returns a partial success: either the brain
// replied to the whole batch or an error is returned and the caller decides
// (409/429/404 map to page states, anything else degrades to archive-only).
// Transport errors and 5xx (not 501) are retried once with the SAME body, hence
// the same client_attempt_id, which the brain treats idempotently.
func (c *Client) Grade(ctx context.Context, req GradeRequest) (*GradeResponse, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	if err := ValidateGradeRequest(req); err != nil {
		return nil, err
	}
	body, err := encodeGradeRequest(req, c.id(), c.now())
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, What: "bad grade request"}
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.retryDelay):
			case <-ctx.Done():
				return nil, classifyTransport(ctx.Err())
			}
		}
		resp, err := c.gradeOnce(ctx, body, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// retryable: the request may not have reached the brain, or the brain failed
// on its side (5xx). A malformed 200 reply, TLS errors and cancellations are not.
func retryable(err error) bool {
	var be *Error
	if !errors.As(err, &be) || be.Kind != KindUnavailable {
		return false
	}
	if be.Status != 0 {
		return be.Status >= 500
	}
	return be.What == "brain unreachable" || be.What == "brain timeout"
}

func (c *Client) gradeOnce(ctx context.Context, body []byte, req GradeRequest) (*GradeResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, gradeTimeout)
	defer cancel()
	path := c.gradePath
	if path == "" {
		path = DefaultGradePath
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path, nil), bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad upstream request"}
	}
	hr.Header.Set("Authorization", "Bearer "+c.gradeToken)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	hr.Header.Set("User-Agent", "ruyi-site/1")
	resp, err := c.hc.Do(hr)
	if err != nil {
		return nil, classifyTransport(err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated { // 201 = new write, 200 = replays only
		return nil, upstreamErr(resp)
	}
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, maxGradeBytes+1))
	resp.Body.Close()
	if rerr != nil || len(b) > maxGradeBytes {
		return nil, &Error{Kind: KindUnavailable, What: "bad grade reply body"}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil, &Error{Kind: KindUnavailable, What: "grade reply is not JSON"}
	}
	return decodeGradeResponse(b, req)
}
