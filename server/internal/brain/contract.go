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
// deployed. A 404/501/405 therefore means "not deployed here yet", not "no such
// question": lookup falls back to reading the source list (P1–P3 behaviour),
// pick-practice answers ErrUnsupported.

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
)

var (
	reasonRe   = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,64}$`)
	deferredRe = regexp.MustCompile(`^[a-z_]{1,40}$`)
)

// NotReady reports whether err means "this brain does not have that endpoint
// (yet)": 404 / 501 / 405 on a route that must exist after deployment.
func NotReady(err error) bool {
	var be *Error
	if !errors.As(err, &be) {
		return false
	}
	return be.Kind == KindNotReady || be.Kind == KindNotFound
}

func (c *Client) id() string {
	if c.brainID == "" {
		return DefaultBrainID
	}
	return c.brainID
}

// doJSON reads a bounded JSON reply; any non-200 becomes a classified Error.
func (c *Client) doJSON(ctx context.Context, method, rawURL string, body []byte, limit int64) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad upstream request"}
	}
	req.Header.Set("Authorization", "Bearer "+c.token) // read token only
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
// call, chunked). Questions whose source differs from source are treated as
// missing. If the endpoint is not deployed it falls back to reading the whole
// source list, and remembers that for a short while to avoid a wasted request
// each time.
func (c *Client) LookupByQrefs(ctx context.Context, source string, qrefs []string) (map[string]Question, []string, error) {
	want := make([]string, 0, len(qrefs))
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
	for i := 0; i < len(want); i += MaxLookupQrefs {
		chunk := want[i:min(i+MaxLookupQrefs, len(want))]
		body, _ := json.Marshal(map[string]any{"qrefs": chunk})
		cctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		b, err := c.doJSON(cctx, http.MethodPost, c.endpoint(path, nil), body, MaxListBytes)
		cancel()
		if err != nil {
			if NotReady(err) {
				c.mu.Lock()
				c.lookupDownUntil = c.now().Add(lookupDownFor)
				c.mu.Unlock()
				return c.listLookup(ctx, source, want)
			}
			return nil, nil, err
		}
		var rep struct {
			Items []Question `json:"items"`
		}
		if json.Unmarshal(b, &rep) != nil {
			return nil, nil, &Error{Kind: KindUnavailable, What: "malformed lookup reply"}
		}
		asked := map[string]bool{}
		for _, r := range chunk {
			asked[r] = true
		}
		for _, q := range rep.Items {
			if !asked[q.QRef] || (q.Source != nil && *q.Source != source) {
				continue // never trust qrefs we did not ask for; foreign source = missing
			}
			if _, dup := found[q.QRef]; !dup {
				found[q.QRef] = normalizeQuestion(q)
			}
		}
	}
	// "missing" is whatever was asked and not returned: that also covers a
	// brain-side missing list and a reply that simply omits a qref.
	missing := []string{}
	for _, r := range want {
		if _, ok := found[r]; !ok {
			missing = append(missing, r)
		}
	}
	return found, missing, nil
}

// pick implements the brain-side modes of SelectForPractice.
func (c *Client) pick(ctx context.Context, source string, n int, o SelectOpts) ([]Pick, error) {
	path := c.pickPath
	if path == "" {
		path = DefaultPickPath
	}
	q := url.Values{"source": {source}, "n": {strconv.Itoa(n)}, "mode": {o.Mode}, "brain": {c.id()}}
	ctx, cancel := context.WithTimeout(ctx, pickTimeout)
	defer cancel()
	b, err := c.doJSON(ctx, http.MethodGet, c.endpoint(path, q), nil, maxPickBytes)
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
