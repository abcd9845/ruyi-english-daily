package brain

// Grade adapter. THE WHOLE WIRE CONTRACT OF THE GRADING CALL LIVES IN THIS FILE.
//
// Contract (brain "practice-site automatic plan", POST /v1/bank/grade):
//
//	POST {base}{gradePath}                 Authorization: Bearer <grade token>
//	{"brain":"ruyi","client_attempt_id":"<1..160 of [A-Za-z0-9._:-]>",
//	 "items":[{"qref","answer"(string | [string…≤10 blanks]),"rev"?,
//	           "attempt_kind"?:"correction|variant","answered_at"?:RFC3339,
//	           "session_ref"?,"payload"?}, …≤50, qrefs unique]}
//	-> 200 {"client_attempt_id":<echo>,
//	        "items":[{"qref","status":"graded|pending_key|needs_review|unsupported",
//	                  "correct"?:bool (graded only),"attempt_id":<int>,
//	                  "recorded":bool,"duplicate":bool}, …]}
//	   unsupported => recorded:false, nothing written, no attempt_id.
//	   pending_key / needs_review => the answer is recorded, no right/wrong.
//	-> 404 an unknown qref anywhere in the batch (nothing written)
//	-> 409 rev_mismatch (item rev is not current) | idempotency_conflict (same
//	       client_attempt_id + qref seen with a different answer/result)
//	-> 429 rate_limited (same question > 3 writes a day per brain; whole batch refused)
//
// Idempotency key on the brain side: ps:<client_attempt_id>:<qref>. The same id
// with the same answer replies 200 + duplicate:true with the same attempt_id.
// Network retries therefore reuse the id (Grade retries once itself); a user
// re-submitting with changed answers must use a NEW id (the page does that).
//
// The token: the grade token is a separate env value (RUYI_BRAIN_GRADE_TOKEN),
// never equal to the read token, server-side only. Its scope is whatever the
// brain issued for the practice site ("bank:read + single-brain write" today,
// maybe a narrower bank:grade later); this code never inspects or names a scope.

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
)

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
	seen := map[string]bool{}
	for _, it := range r.Items {
		if it.QRef == "" || len(it.QRef) > maxQRefLen || seen[it.QRef] {
			return bad("bad or duplicate qref")
		}
		seen[it.QRef] = true
		switch a := it.Answer.(type) {
		case string:
		case []string:
			if len(a) > MaxBlanks {
				return bad("too many blanks")
			}
		default:
			return bad("bad answer type")
		}
		switch it.AttemptKind {
		case "", AttemptCorrect, AttemptVariant:
		default:
			return bad("bad attempt_kind")
		}
		if it.Rev < 0 || len(it.SessionRef) > maxSessionRef {
			return bad("bad rev or session_ref")
		}
	}
	return nil
}

func encodeGradeRequest(r GradeRequest, brainID string) ([]byte, error) {
	type item struct {
		QRef        string         `json:"qref"`
		Answer      any            `json:"answer"`
		Rev         int            `json:"rev,omitempty"`
		AttemptKind string         `json:"attempt_kind,omitempty"`
		AnsweredAt  string         `json:"answered_at,omitempty"`
		SessionRef  string         `json:"session_ref,omitempty"`
		Payload     map[string]any `json:"payload,omitempty"`
	}
	items := make([]item, 0, len(r.Items))
	for _, it := range r.Items {
		o := item{QRef: it.QRef, Answer: it.Answer, Rev: it.Rev, AttemptKind: it.AttemptKind, SessionRef: it.SessionRef, Payload: it.Payload}
		if !it.AnsweredAt.IsZero() {
			o.AnsweredAt = it.AnsweredAt.UTC().Format(time.RFC3339)
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
	asked := map[string]bool{}
	for _, it := range req.Items {
		asked[it.QRef] = true
	}
	out := &GradeResponse{}
	seen := map[string]bool{}
	for _, it := range v.Items {
		if !asked[it.QRef] || seen[it.QRef] {
			continue // never trust qrefs we did not send
		}
		seen[it.QRef] = true
		r := GradeResult{QRef: it.QRef, Status: it.Status}
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
	body, err := encodeGradeRequest(req, c.id())
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
	if resp.StatusCode != http.StatusOK {
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
