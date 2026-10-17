package brain

// Wire code for the two read-only calls of the "practice-site automatic plan"
// contract. (The grading call lives in grade.go.) Both use the read token
// (RUYI_BRAIN_TOKEN) only; the grade token never travels on these requests.
//
//	POST {base}/v1/bank/questions/lookup      {"qrefs":[…≤100]}
//	  -> 200 {"items":[Question…],"missing":["qref",…]}      (no answers; media, figure_status, rev)
//	GET  {base}/v1/brain/pick-practice?source=&n=&mode=due_first|wrong_only&brain=ruyi
//	  -> 200 {"items":[{"qref","number","origin":"review|wrong","reasons":[…]}]}
//
// Neither endpoint exists on the live brain until its contract change is
// deployed. Telling "not deployed" from a real answer:
//
//   - lookup never answers 404 once deployed (an unknown qref goes into
//     "missing"), so ANY 404/501/405 means "not deployed": fall back to reading
//     the source list (P1–P3 behaviour). ProbeLookup asks for one obviously
//     non-existent qref: 200 + that qref in "missing" = deployed.
//   - pick-practice answers 404 not_found for an unknown source/brain too. The
//     catch-all route 404 is {code:not_found, message:"not found"} without
//     details (Error.IsRouteNotFound) = not deployed -> ErrUnsupported; any
//     other 404 is a real "no such source/brain" and is returned as an error.
//   - grade: a 404 with details.qrefs is the deployed endpoint's "question not
//     found"; any other 404 = not deployed (see grade.go).
//
// qrefs are lower-case on the wire; the caller's original strings are kept for
// matching replies (lookup/pick answer canonical qrefs, "missing" echoes the
// request string; an alias resolves to its canonical item).

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

// Defaults and limits of the contract.
const (
	DefaultLookupPath = "/v1/bank/questions/lookup"
	DefaultPickPath   = "/v1/brain/pick-practice"
	DefaultBrainID    = "ruyi"
	MaxLookupQrefs    = 100

	lookupTimeout  = 10 * time.Second
	pickTimeout    = 10 * time.Second
	lookupDownFor  = 30 * time.Second
	maxPickBytes   = 1 << 20
	maxPickReasons = 12

	// MaxPickN is the contract's upper bound of n for pick-practice (1..10).
	MaxPickN = 10
	// probeQRef is an obviously non-existent qref used by ProbeLookup (read-only).
	probeQRef = "math:zz-probe-nonexistent:1"
)

var (
	reasonRe   = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,64}$`)
	deferredRe = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

// NotReady reports whether err means "this brain does not have that endpoint
// (yet)": 501/405, or the catch-all route 404 (message "not found", no
// details). A 404 that carries details or another message is an answer of a
// deployed endpoint and is NOT "not ready".
func NotReady(err error) bool {
	var be *Error
	if !errors.As(err, &be) {
		return false
	}
	return be.Kind == KindNotReady || be.IsRouteNotFound()
}

// notDeployedAny is NotReady for endpoints that never answer 404 themselves
// (lookup): every 404 / 501 / 405 means "not deployed".
func notDeployedAny(err error) bool {
	var be *Error
	if !errors.As(err, &be) {
		return false
	}
	return be.Kind == KindNotReady || be.Kind == KindNotFound
}

// RouteMissing is true if err is a 404 for something real (a source or brain
// that does not exist, grade's unknown qref) rather than a missing route.
func RouteMissing(err error) bool {
	var be *Error
	return errors.As(err, &be) && be.Kind == KindNotFound && !be.IsRouteNotFound()
}

func (c *Client) id() string {
	if c.brainID == "" {
		return DefaultBrainID
	}
	return c.brainID
}

// doJSON reads a bounded JSON reply (tok is the bearer token: the read token for lookup, the
// grade token for pick-practice); any non-200 becomes a classified Error.
func (c *Client) doJSON(ctx context.Context, tok, method, rawURL string, body []byte, limit int64) ([]byte, error) {
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
	if resp.StatusCode != http.StatusOK {
		return nil, upstreamErr(resp)
	}
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	resp.Body.Close()
	if rerr != nil || int64(len(b)) > limit {
		return nil, &Error{Kind: KindUnavailable, What: "bad reply body"}
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil, &Error{Kind: KindUnavailable, What: "reply is not JSON"}
	}
	return b, nil
}

// LookupByQrefs implements Bank with the batch lookup endpoint (≤100 qrefs per
// call, chunked). The returned map is keyed by the caller's qref strings (an
// alias maps to its canonical question); missing echoes the caller's strings.
// Questions whose source differs from source are treated as missing. If the
// endpoint is not deployed it falls back to reading the whole source list and
// remembers that for a short while to avoid a wasted request each time.
func (c *Client) LookupByQrefs(ctx context.Context, source string, qrefs []string) (map[string]Question, []string, error) {
	want := make([]string, 0, len(qrefs)) // caller's strings, deduplicated
	seen := map[string]bool{}
	for _, r := range qrefs {
		if r != "" && !seen[r] {
			seen[r] = true
			want = append(want, r)
		}
	}
	found := map[string]Question{}
	if len(want) == 0 {
		return found, []string{}, nil
	}
	c.mu.Lock()
	down := c.now().Before(c.lookupDownUntil)
	c.mu.Unlock()
	if down {
		return c.listLookup(ctx, source, want)
	}
	path := c.lookupPath
	if path == "" {
		path = DefaultLookupPath
	}
	// the strings that go on the wire: lower-cased, one per distinct value
	var sent []string
	byWire := map[string][]string{} // wire string -> caller strings
	for _, r := range want {
		w := strings.ToLower(r)
		if _, ok := byWire[w]; !ok {
			sent = append(sent, w)
		}
		byWire[w] = append(byWire[w], r)
	}
	for i := 0; i < len(sent); i += MaxLookupQrefs {
		chunk := sent[i:min(i+MaxLookupQrefs, len(sent))]
		body, _ := json.Marshal(map[string]any{"qrefs": chunk})
		cctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		b, err := c.doJSON(cctx, c.token, http.MethodPost, c.endpoint(path, nil), body, MaxListBytes)
		cancel()
		if err != nil {
			if notDeployedAny(err) {
				c.mu.Lock()
				c.lookupDownUntil = c.now().Add(lookupDownFor)
				c.mu.Unlock()
				return c.listLookup(ctx, source, want)
			}
			return nil, nil, err
		}
		var rep struct {
			Items   []Question `json:"items"`
			Missing []string   `json:"missing"`
		}
		if json.Unmarshal(b, &rep) != nil {
			return nil, nil, &Error{Kind: KindUnavailable, What: "malformed lookup reply"}
		}
		for wire, q := range alignLookup(chunk, rep.Items, rep.Missing, source) {
			for _, orig := range byWire[wire] {
				found[orig] = q
			}
		}
	}
	// "missing" is whatever was asked and not resolved: that also covers a
	// brain-side missing list and a reply that simply omits a qref.
	missing := []string{}
	for _, r := range want {
		if _, ok := found[r]; !ok {
			missing = append(missing, r)
		}
	}
	return found, missing, nil
}

// alignLookup pairs the wire strings of one chunk with the reply's items.
// Items carry the CANONICAL qref; an exact (case-insensitive) match is
// unambiguous. A string that is neither echoed in "missing" nor equal to an
// item's qref is an alias; the reply lists items in request order and drops a
// second string that resolves to an already returned question, so aliases are
// paired by position only if that is unambiguous (as many leftover strings as
// leftover items). Anything else stays missing rather than being guessed.
func alignLookup(chunk []string, items []Question, missing []string, source string) map[string]Question {
	out := map[string]Question{}
	miss := map[string]bool{}
	for _, m := range missing {
		miss[strings.ToLower(m)] = true
	}
	used := make([]bool, len(items))
	var rest []string
	for _, w := range chunk {
		if miss[w] {
			continue
		}
		matched := false
		for i, q := range items {
			if !used[i] && strings.EqualFold(q.QRef, w) {
				used[i] = true
				matched = true
				if q.Source == nil || *q.Source == source {
					out[w] = normalizeQuestion(q)
				}
				break
			}
		}
		if !matched {
			rest = append(rest, w)
		}
	}
	var left []Question
	for i, q := range items {
		if !used[i] && q.QRef != "" {
			left = append(left, q)
		}
	}
	if len(rest) > 0 && len(rest) == len(left) {
		for i, w := range rest {
			if left[i].Source == nil || *left[i].Source == source {
				out[w] = normalizeQuestion(left[i])
			}
		}
	}
	return out
}

// ProbeLookup asks the lookup endpoint for one obviously non-existent qref
// (read-only; nothing is written anywhere): 200 whose "missing" names it means
// the endpoint is deployed; 404/501/405 means it is not. Any other outcome is
// an error (inconclusive). The answer feeds the same short-lived cache that
// LookupByQrefs uses, so a later lookup does not probe again.
func (c *Client) ProbeLookup(ctx context.Context) (deployed bool, err error) {
	path := c.lookupPath
	if path == "" {
		path = DefaultLookupPath
	}
	body, _ := json.Marshal(map[string]any{"qrefs": []string{probeQRef}})
	cctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	b, err := c.doJSON(cctx, c.token, http.MethodPost, c.endpoint(path, nil), body, MaxListBytes)
	if err != nil {
		if notDeployedAny(err) {
			c.mu.Lock()
			c.lookupDownUntil = c.now().Add(lookupDownFor)
			c.mu.Unlock()
			return false, nil
		}
		return false, err
	}
	var rep struct {
		Items   []Question `json:"items"`
		Missing []string   `json:"missing"`
	}
	if json.Unmarshal(b, &rep) != nil || len(rep.Items) != 0 {
		return false, &Error{Kind: KindUnavailable, What: "unexpected probe reply"}
	}
	for _, m := range rep.Missing {
		if m == probeQRef {
			c.mu.Lock()
			c.lookupDownUntil = time.Time{}
			c.mu.Unlock()
			return true, nil
		}
	}
	return false, &Error{Kind: KindUnavailable, What: "unexpected probe reply"}
}

// pick implements the brain-side modes of SelectForPractice.
func (c *Client) pick(ctx context.Context, source string, n int, o SelectOpts) ([]Pick, error) {
	path := c.pickPath
	if path == "" {
		path = DefaultPickPath
	}
	// The contract wants the target brain's "write" scope for pick-practice, which the
	// bank:read token does not have: it is sent with the grade token (never the read token).
	if c.gradeToken == "" {
		return nil, ErrPickNoToken
	}
	n = max(1, min(n, MaxPickN)) // contract: n is 1..10
	q := url.Values{"source": {source}, "n": {strconv.Itoa(n)}, "mode": {o.Mode}, "brain": {c.id()}}
	ctx, cancel := context.WithTimeout(ctx, pickTimeout)
	defer cancel()
	b, err := c.doJSON(ctx, c.gradeToken, http.MethodGet, c.endpoint(path, q), nil, maxPickBytes)
	if err != nil {
		if NotReady(err) {
			return nil, ErrUnsupported
		}
		return nil, err
	}
	var rep struct {
		Items []struct {
			QRef    string          `json:"qref"`
			Number  json.RawMessage `json:"number"`
			Origin  string          `json:"origin"`
			Reasons []string        `json:"reasons"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &rep) != nil {
		return nil, &Error{Kind: KindUnavailable, What: "malformed pick reply"}
	}
	var out []Pick
	seen := map[string]bool{}
	for _, it := range rep.Items {
		if it.QRef == "" || len(it.QRef) > 300 || seen[it.QRef] {
			continue
		}
		seen[it.QRef] = true
		p := Pick{QRef: it.QRef}
		var ns string
		var nn json.Number
		if json.Unmarshal(it.Number, &ns) == nil {
			p.Number = clipStr(ns, 40)
		} else if json.Unmarshal(it.Number, &nn) == nil {
			p.Number = nn.String()
		}
		if it.Origin == "review" || it.Origin == "wrong" {
			p.Origin = it.Origin
		}
		for _, r := range it.Reasons {
			if len(p.Reasons) >= maxPickReasons || !reasonRe.MatchString(r) {
				continue
			}
			p.Reasons = append(p.Reasons, r)
			if code, ok := strings.CutPrefix(r, "deferred:"); ok && p.Deferred == "" {
				p.Deferred = "unknown"
				if deferredRe.MatchString(code) {
					p.Deferred = code
				}
			}
		}
		if o.SkipDeferred && p.Deferred != "" {
			continue
		}
		out = append(out, p)
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

func clipStr(s string, n int) string {
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}
