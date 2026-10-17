// Package brain is the read-only client for the learning-center ("brain") API.
//
// It talks to exactly three endpoints from the phase-2 contract
// (docs/题库图片支持-阶段2对接约定.md in ruyi-learning-center):
//
//	GET  /v1/bank/questions?source=…&limit=…[&cursor=…]   (Authorization: Bearer)
//	POST /v1/media/{sha256}/ticket                         (Authorization: Bearer)
//	GET  /v1/media/{sha256}?ticket=…                       (no Authorization)
//
// The bearer token and tickets never leave this package except as upstream
// request headers/query. Errors returned from here are classified and carry no
// URL, header or query text, so callers can log them safely.
package brain

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits.
const (
	MaxListBytes  = 8 << 20
	MaxMediaBytes = 5 << 20 // contract: stored media <= 4 MiB
	maxPages      = 10
	pageLimit     = 500
	listTimeout   = 10 * time.Second
	mediaTimeout  = 20 * time.Second
	listTTL       = 30 * time.Second
	ticketSkew    = 30 * time.Second
)

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// svg is deliberately absent: it can carry script.
	allowedMime = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}
	slots       = map[string]bool{"stem": true, "option": true, "explanation": true, "figure": true}
)

// ValidSHA reports whether s is a 64-char lowercase hex digest.
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// AllowedMime reports whether mime is an image type we will forward.
func AllowedMime(m string) bool { return allowedMime[m] }

// Kind classifies a failure.
type Kind int

// Failure kinds.
const (
	KindUnavailable Kind = iota + 1 // network, timeout, 5xx, bad TLS, malformed reply
	KindAuth                        // the brain rejected our bearer token
	KindNotFound                    // 404
	KindTicket                      // ticket rejected twice (401 after refresh)
	KindTooLarge
)

// Error is a safe, classified error.
type Error struct {
	Kind   Kind
	Status int    // upstream HTTP status, 0 if none
	Code   string // upstream error code (e.g. ticket_expired), only [a-z_] chars
	What   string // short fixed description
}

func (e *Error) Error() string {
	s := e.What
	if e.Status != 0 {
		s += " (status " + strconv.Itoa(e.Status) + ")"
	}
	if e.Code != "" {
		s += " code=" + e.Code
	}
	return s
}

// Media is one figure attached to a question, as the contract defines it.
type Media struct {
	SHA256    string `json:"sha256"`
	Mime      string `json:"mime"`
	Bytes     *int64 `json:"bytes,omitempty"`
	Ordinal   int    `json:"ordinal"`
	Slot      string `json:"slot"`
	OptionKey string `json:"option_key,omitempty"`
	Alt       string `json:"alt,omitempty"`
}

// Question is the subset of the list response this site uses.
type Question struct {
	QRef    string          `json:"qref"`
	Source  *string         `json:"source"`
	Number  *string         `json:"number"`
	QType   string          `json:"qtype"`
	Stem    string          `json:"stem"`
	Options json.RawMessage `json:"options"`
	Media   []Media         `json:"media"`
	// FigureStatus is the brain's read-only derived field: ok / missing / none /
	// unknown. It may be absent (not live yet); absent is treated as unknown.
	FigureStatus *string `json:"figure_status"`
}

// Figure statuses as delivered to the page.
const (
	FigureOK      = "ok"
	FigureMissing = "missing"
	FigureNone    = "none"
	FigureUnknown = "unknown"
)

// Figure returns the normalized figure_status: a missing key or any value
// outside the four known ones counts as unknown.
func (q Question) Figure() string {
	if q.FigureStatus == nil {
		return FigureUnknown
	}
	switch *q.FigureStatus {
	case FigureOK, FigureMissing, FigureNone:
		return *q.FigureStatus
	}
	return FigureUnknown
}

// Config configures a Client.
type Config struct {
	BaseURL     *url.URL
	Token       string
	InsecureTLS bool
	CAFile      string
	Now         func() time.Time
}

type ticket struct {
	value string
	exp   time.Time
}

type listEntry struct {
	at    time.Time
	items []Question
}

// Client is safe for concurrent use.
type Client struct {
	base  *url.URL
	token string
	hc    *http.Client
	now   func() time.Time

	mu      sync.Mutex
	tickets map[string]ticket
	lists   map[string]*listEntry
	listMu  map[string]*sync.Mutex
}

// New builds a Client. It does not contact the brain.
func New(c Config) (*Client, error) {
	tr := &http.Transport{
		Proxy:                 nil, // never route the token through an environment proxy
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	if c.BaseURL.Scheme == "https" {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		switch {
		case c.InsecureTLS:
			// Only reachable for a loopback brain address (config enforces it);
			// this client only ever talks to that one base URL.
			tc.InsecureSkipVerify = true //nolint:gosec
		case c.CAFile != "":
			pem, err := os.ReadFile(c.CAFile)
			if err != nil {
				return nil, errors.New("cannot read RUYI_BRAIN_CA_FILE")
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("RUYI_BRAIN_CA_FILE has no valid PEM certificate")
			}
			tc.RootCAs = pool
		}
		tr.TLSClientConfig = tc
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		base: c.BaseURL, token: c.Token, now: now,
		hc: &http.Client{
			Transport:     tr,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		tickets: map[string]ticket{},
		lists:   map[string]*listEntry{},
		listMu:  map[string]*sync.Mutex{},
	}, nil
}

func (c *Client) endpoint(path string, q url.Values) string {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	return u.String()
}

// do performs a request and converts any transport error into a safe Error.
func (c *Client) do(ctx context.Context, method, rawURL string, bearer bool, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad upstream request"}
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json, image/*")
	req.Header.Set("User-Agent", "ruyi-site/1")
	resp, err := c.hc.Do(req)
	if err != nil {
		// err is a *url.Error whose text contains the URL (and thus a ticket).
		// Classify it instead of passing it on.
		what := "brain unreachable"
		var ne net.Error
		var ce x509.UnknownAuthorityError
		switch {
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
			what = "brain timeout"
		case errors.As(err, &ce) || strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "tls:"):
			what = "brain tls error"
		case errors.Is(err, context.Canceled):
			what = "request canceled"
		}
		return nil, &Error{Kind: KindUnavailable, What: what}
	}
	return resp, nil
}

var codeRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

// upstreamErr maps a non-2xx reply to an Error. It reads at most 2 KiB of the
// body, only to extract {"error":{"code":…}} or {"code":…}.
func upstreamErr(resp *http.Response) *Error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	code := ""
	var v struct {
		Code  string `json:"code"`
		Error any    `json:"error"`
	}
	if json.Unmarshal(b, &v) == nil {
		code = v.Code
		if m, ok := v.Error.(map[string]any); ok {
			if s, ok := m["code"].(string); ok {
				code = s
			}
		} else if s, ok := v.Error.(string); ok && code == "" {
			code = s
		}
	}
	if !codeRe.MatchString(code) {
		code = ""
	}
	e := &Error{Status: resp.StatusCode, Code: code, What: "brain error"}
	switch {
	case resp.StatusCode == 404:
		e.Kind = KindNotFound
		e.What = "not found"
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		e.Kind = KindAuth
		e.What = "brain rejected credentials"
	default:
		e.Kind = KindUnavailable
	}
	return e
}

// ---- question list ---------------------------------------------------------

// Source returns the active questions of a source, sorted by question number
// (major, minor, suffix; unparsable last, stable). The caller must have validated
// source. Results are cached for listTTL.
func (c *Client) Source(ctx context.Context, source string, forceFresh bool) ([]Question, error) {
	c.mu.Lock()
	lm := c.listMu[source]
	if lm == nil {
		lm = &sync.Mutex{}
		c.listMu[source] = lm
	}
	c.mu.Unlock()
	lm.Lock()
	defer lm.Unlock()

	c.mu.Lock()
	e := c.lists[source]
	c.mu.Unlock()
	if e != nil && c.now().Sub(e.at) < listTTL && !forceFresh {
		return e.items, nil
	}
	items, err := c.fetchSource(ctx, source)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.lists[source] = &listEntry{at: c.now(), items: items}
	c.mu.Unlock()
	return items, nil
}

func (c *Client) fetchSource(ctx context.Context, source string) ([]Question, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var all []Question
	cursor := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"source": {source}, "limit": {strconv.Itoa(pageLimit)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp, err := c.do(ctx, http.MethodGet, c.endpoint("/v1/bank/questions", q), true, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, upstreamErr(resp)
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxListBytes+1))
		resp.Body.Close()
		if rerr != nil || len(b) > MaxListBytes {
			return nil, &Error{Kind: KindUnavailable, What: "bad question list body"}
		}
		var pg struct {
			Items      []Question `json:"items"`
			NextCursor *string    `json:"next_cursor"`
		}
		if err := json.Unmarshal(b, &pg); err != nil {
			return nil, &Error{Kind: KindUnavailable, What: "malformed question list"}
		}
		all = append(all, pg.Items...)
		if pg.NextCursor == nil || *pg.NextCursor == "" {
			return normalize(all, source), nil
		}
		cursor = *pg.NextCursor
		if len(cursor) > 512 {
			return nil, &Error{Kind: KindUnavailable, What: "bad cursor"}
		}
	}
	return nil, &Error{Kind: KindUnavailable, What: "too many pages"}
}

// normalize drops foreign-source rows, invalid media entries and sorts.
func normalize(in []Question, source string) []Question {
	out := make([]Question, 0, len(in))
	for _, q := range in {
		if q.QRef == "" || (q.Source != nil && *q.Source != source) {
			continue
		}
		var ms []Media
		for _, m := range q.Media {
			if !ValidSHA(m.SHA256) || !allowedMime[m.Mime] || !slots[m.Slot] {
				continue
			}
			ms = append(ms, m)
		}
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].Ordinal < ms[j].Ordinal })
		q.Media = ms
		out = append(out, q)
	}
	SortQuestions(out)
	return out
}

var numberRe = regexp.MustCompile(`^(\d+)\((\d+)\)(.*)$`)

type numKey struct {
	major, minor int
	suffix       string
}

// parseNumber splits a question number such as "11(2)③" into (11, 2, "③").
// Numbers that do not match, or are absent, report ok=false.
func parseNumber(q Question) (numKey, bool) {
	if q.Number == nil {
		return numKey{}, false
	}
	m := numberRe.FindStringSubmatch(*q.Number)
	if m == nil {
		return numKey{}, false
	}
	maj, err1 := strconv.Atoi(m[1])
	min, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return numKey{}, false
	}
	return numKey{maj, min, m[3]}, true
}

// less orders by (major, minor, suffix as string). Numbers that cannot be
// parsed sort after all parsed ones; among themselves (and for equal keys)
// the original order is kept because the caller uses a stable sort.
func less(a, b Question) bool {
	ak, aok := parseNumber(a)
	bk, bok := parseNumber(b)
	switch {
	case aok && bok:
		if ak.major != bk.major {
			return ak.major < bk.major
		}
		if ak.minor != bk.minor {
			return ak.minor < bk.minor
		}
		return ak.suffix < bk.suffix
	case aok != bok:
		return aok
	}
	return false
}

// SortQuestions sorts in place by question number (stable).
func SortQuestions(qs []Question) {
	sort.SliceStable(qs, func(i, j int) bool { return less(qs[i], qs[j]) })
}

// FindMedia looks up a sha256 in the cached list of source. If it is unknown
// and the cache is not fresh from this instant, it refreshes once.
func (c *Client) FindMedia(ctx context.Context, source, sha string) (*Media, error) {
	for attempt := 0; attempt < 2; attempt++ {
		items, err := c.Source(ctx, source, attempt == 1)
		if err != nil {
			return nil, err
		}
		for _, q := range items {
			for i := range q.Media {
				if q.Media[i].SHA256 == sha {
					m := q.Media[i]
					return &m, nil
				}
			}
		}
	}
	return nil, &Error{Kind: KindNotFound, What: "media not in source"}
}

// ---- media -----------------------------------------------------------------

// Image is a fetched figure.
type Image struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64 // -1 if unknown
	cancel        context.CancelFunc
}

// Close releases the upstream connection.
func (i *Image) Close() error { err := i.Body.Close(); i.cancel(); return err }

func (c *Client) getTicket(ctx context.Context, sha string, fresh bool) (string, error) {
	if !fresh {
		c.mu.Lock()
		t, ok := c.tickets[sha]
		c.mu.Unlock()
		if ok && c.now().Add(ticketSkew).Before(t.exp) {
			return t.value, nil
		}
	}
	resp, err := c.do(ctx, http.MethodPost, c.endpoint("/v1/media/"+sha+"/ticket", nil), true, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", upstreamErr(resp)
	}
	b, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	var tr struct {
		Ticket    string    `json:"ticket"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if rerr != nil || json.Unmarshal(b, &tr) != nil || tr.Ticket == "" || strings.ContainsAny(tr.Ticket, " \r\n\t&#?") {
		return "", &Error{Kind: KindUnavailable, What: "malformed ticket reply"}
	}
	c.mu.Lock()
	if len(c.tickets) > 2000 {
		c.tickets = map[string]ticket{}
	}
	c.tickets[sha] = ticket{value: tr.Ticket, exp: tr.ExpiresAt}
	c.mu.Unlock()
	return tr.Ticket, nil
}

func (c *Client) dropTicket(sha string) {
	c.mu.Lock()
	delete(c.tickets, sha)
	c.mu.Unlock()
}

// FetchMedia gets the image bytes for sha. A cached ticket is reused until
// shortly before it expires. If the brain answers 401 (ticket expired or
// invalid), the ticket is dropped, a new one is requested and the GET is
// retried exactly once.
func (c *Client) FetchMedia(ctx context.Context, sha string) (*Image, error) {
	if !ValidSHA(sha) {
		return nil, &Error{Kind: KindNotFound, What: "bad media id"}
	}
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	for attempt := 0; attempt < 2; attempt++ {
		tk, err := c.getTicket(ctx, sha, attempt == 1)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := c.do(ctx, http.MethodGet, c.endpoint("/v1/media/"+sha, url.Values{"ticket": {tk}}), false, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			ue := upstreamErr(resp)
			c.dropTicket(sha)
			if attempt == 0 {
				continue
			}
			cancel()
			ue.Kind = KindTicket
			return nil, ue
		}
		if resp.StatusCode != http.StatusOK {
			cancel()
			return nil, upstreamErr(resp)
		}
		if resp.ContentLength > MaxMediaBytes {
			resp.Body.Close()
			cancel()
			return nil, &Error{Kind: KindTooLarge, What: "image too large"}
		}
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
		if !allowedMime[ct] {
			resp.Body.Close()
			cancel()
			return nil, &Error{Kind: KindUnavailable, What: "unexpected content type"}
		}
		return &Image{Body: resp.Body, ContentType: ct, ContentLength: resp.ContentLength, cancel: cancel}, nil
	}
	cancel()
	return nil, &Error{Kind: KindTicket, What: "ticket retry exhausted"}
}
