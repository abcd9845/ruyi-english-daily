package brain

// Grade adapter. THE WHOLE WIRE CONTRACT OF THE GRADING CALL LIVES IN THIS FILE.
//
// STATUS: DRAFT. The brain's grading endpoint is not final. When the brain PM
// delivers the contract, only encodeGradeRequest, decodeGradeResponse and the
// two constants below change.
//
// Draft contract:
//
//	POST {base}{gradePath}                 Authorization: Bearer <grade token>
//	{"client_attempt_id":"<uuid>","set_id":"…","source":"…",
//	 "items":[{"qref":"…","answer":<string | [string,…]>}, …]}
//	-> 200 {"client_attempt_id":"<uuid>","attempt_id":"<brain id>","recorded":true,
//	        "items":[{"qref":"…","status":"graded|pending_key|unsupported","correct":true|false|null}, …]}
//
// client_attempt_id is the brain-side idempotency key: the same id submitted
// twice must return the stored result. The site only reports "submitted to the
// brain" when the reply says recorded=true and carries a brain attempt_id.
// The grade token is a separate env value (never the bank:read token) and must
// not be able to do anything but grade/record attempts.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultGradePath is the draft endpoint path (override with RUYI_BRAIN_GRADE_PATH).
const DefaultGradePath = "/v1/bank/grade"

const (
	gradeTimeout  = 10 * time.Second
	maxGradeBytes = 1 << 20
)

// ErrGradeDisabled: no grade token configured. The site degrades to archive-only.
var ErrGradeDisabled = errors.New("grading not configured")

// Grade statuses.
const (
	StatusGraded     = "graded"
	StatusPendingKey = "pending_key"
	StatusUnsupport  = "unsupported"
)

// GradeItem is one answer: Answer is a string (single choice, free text) or a
// []string (multiple choice keys, fill-in blanks).
type GradeItem struct {
	QRef   string
	Answer any
}

// GradeRequest is what the site asks the brain to grade and record.
type GradeRequest struct {
	ClientAttemptID string
	SetID           string
	Source          string
	Items           []GradeItem
}

// GradeResult is the verdict for one qref. Correct is nil unless Status is graded.
type GradeResult struct {
	QRef    string
	Status  string
	Correct *bool
}

// GradeResponse is the brain's confirmed reply.
type GradeResponse struct {
	BrainAttemptID string
	Results        []GradeResult
}

func encodeGradeRequest(r GradeRequest) ([]byte, error) {
	type item struct {
		QRef   string `json:"qref"`
		Answer any    `json:"answer"`
	}
	items := make([]item, 0, len(r.Items))
	for _, it := range r.Items {
		items = append(items, item{QRef: it.QRef, Answer: it.Answer})
	}
	return json.Marshal(map[string]any{
		"client_attempt_id": r.ClientAttemptID, "set_id": r.SetID, "source": r.Source, "items": items,
	})
}

func decodeGradeResponse(b []byte, req GradeRequest) (*GradeResponse, error) {
	var v struct {
		ClientAttemptID string `json:"client_attempt_id"`
		AttemptID       string `json:"attempt_id"`
		Recorded        bool   `json:"recorded"`
		Items           []struct {
			QRef    string `json:"qref"`
			Status  string `json:"status"`
			Correct *bool  `json:"correct"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed grade reply"}
	}
	// Confirmation: recorded + a brain attempt id + the id we sent echoed back.
	if !v.Recorded || v.AttemptID == "" || len(v.AttemptID) > 128 || v.ClientAttemptID != req.ClientAttemptID {
		return nil, &Error{Kind: KindUnavailable, What: "grade reply lacks brain confirmation"}
	}
	asked := map[string]bool{}
	for _, it := range req.Items {
		asked[it.QRef] = true
	}
	out := &GradeResponse{BrainAttemptID: v.AttemptID}
	seen := map[string]bool{}
	for _, it := range v.Items {
		if !asked[it.QRef] || seen[it.QRef] {
			continue // never trust qrefs we did not send
		}
		seen[it.QRef] = true
		r := GradeResult{QRef: it.QRef, Status: it.Status}
		switch it.Status {
		case StatusGraded:
			if it.Correct == nil {
				r.Status = StatusPendingKey
			} else {
				r.Correct = it.Correct
			}
		case StatusPendingKey, StatusUnsupport:
		default:
			r.Status = StatusPendingKey
		}
		out.Results = append(out.Results, r)
	}
	for _, it := range req.Items { // anything the brain skipped stays pending
		if !seen[it.QRef] {
			out.Results = append(out.Results, GradeResult{QRef: it.QRef, Status: StatusPendingKey})
		}
	}
	return out, nil
}

// Grade implements Bank. It never returns a partial success: either the brain
// confirmed the attempt or an error is returned and the caller degrades.
func (c *Client) Grade(ctx context.Context, req GradeRequest) (*GradeResponse, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	body, err := encodeGradeRequest(req)
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad grade request"}
	}
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
