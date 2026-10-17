// Package server wires the HTTP handlers: static allowlist, family-code
// login, and the read-only brain proxy.
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/config"
	"github.com/abcd9845/ruyi-english-daily/server/internal/embedded"
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

const (
	ntfyBase       = "https://ntfy.sh/"
	loginFailMax   = 5
	loginFailSpan  = time.Minute
	maxLoginBody   = 1024
	mediaCacheCtrl = "private, max-age=3600"
)

// Deps are the collaborators of Server.
type Deps struct {
	Auth    *auth.Authenticator
	Limiter *auth.Limiter
	Brain   *brain.Client
	Static  http.Handler
	Log     *slog.Logger
	Sources []string
	Topic   string
	// Sets is the practice-set registry (RUYI_SETS_FILE); nil/empty = no sets.
	Sets *sets.Registry
	// Embedded serves the compiled-in practice page and KaTeX; nil = none.
	Embedded *embedded.Handler

	// Bank is the brain adapter (defaults to Brain).
	Bank brain.Bank
	// Attempts archives submissions; nil = nothing is archived (archived=false).
	Attempts *attempts.Store
	// NtfyBase ("https://ntfy.sh/") + Topic is where the parent summary is posted;
	// empty Topic = notifications off. NtfyRetryDelay: the wait before the first
	// retry of a failed send (doubles for the next one); see notify.go.
	NtfyBase       string
	NtfyRetryDelay time.Duration
	// NtfyAttemptTimeout / NtfyRequestWait are for tests only (zero = the
	// ntfyAttemptTimeout / ntfyRequestWait constants; no environment variable).
	NtfyAttemptTimeout time.Duration
	NtfyRequestWait    time.Duration
	// GradeMaxPerHour: grading submissions per session per set per hour (default 5).
	GradeMaxPerHour int
	// AutoSources enables dynamic sets (GET /api/practice/new) for these sources.
	AutoSources []string
	AutoTTL     time.Duration
	// SkipDeferred drops brain picks flagged deferred:* instead of marking them.
	SkipDeferred bool
	// Now is for tests (nil = time.Now).
	Now func() time.Time

	// S1 pending-answer queue (queue.go). All zero = off: the site then behaves
	// exactly like before. Queue is the brain adapter (*brain.Client in production).
	Queue        brain.Queue
	QueueEnabled bool
	// QueueNotify starts the completion-notification poller (queue_poller.go).
	QueueNotify bool
	QueueDir    string
	// QueuePoll is the poller period (config enforces >= 40 s; tests may go lower).
	QueuePoll time.Duration
	// QueueSweep / QueueQuiet / QueueJitter are for tests (zero = the defaults:
	// retry sweep 30 s, aggregation quiet period 5 min, random ±10% jitter).
	QueueSweep  time.Duration
	QueueQuiet  time.Duration
	QueueJitter func() float64
	// QueueViewTTL is the per-submission status cache (0 = 5 s; tests use a tiny value).
	QueueViewTTL time.Duration
	// QueueHook is a test-only fault injector for the poller: it is called with a
	// stage name and returns true to simulate a crash right there.
	QueueHook func(stage string) bool
}

// Server is the top-level http.Handler.
type Server struct {
	Deps
	srcRe interface{ MatchString(string) bool }
	allow map[string]bool

	auto       map[string]bool
	dyn        *dynSets
	gradeSess  *auth.Limiter
	gradeIP    *auth.Limiter
	idem       *idemStore
	quotaT     *quotaTally
	ntfyClient *http.Client

	// background parent notifications (notify.go)
	nwg         sync.WaitGroup
	nstop       chan struct{}
	nstopOnce   sync.Once
	nmu         sync.Mutex
	notified    map[string]bool
	notifiedOrd []string
	ntfyTimeout time.Duration
	ntfyWait    time.Duration

	// S1 queue runtime (nil when the queue is off)
	q          *queueRT
	qlock      *os.File
	qcloseOnce sync.Once
	poll       *poller
}

// New builds a Server.
func New(d Deps) *Server {
	s := &Server{Deps: d, srcRe: config.SourceRe(), allow: map[string]bool{}}
	for _, x := range d.Sources {
		if x != sets.LocalSource { // reserved for local test sets: never a brain source
			s.allow[x] = true
		}
	}
	if s.Bank == nil && s.Brain != nil {
		s.Bank = s.Brain
	}
	s.auto = map[string]bool{}
	for _, x := range d.AutoSources {
		if x != sets.LocalSource {
			s.auto[x] = true
		}
	}
	if s.AutoTTL <= 0 {
		s.AutoTTL = 6 * time.Hour
	}
	if s.GradeMaxPerHour <= 0 {
		s.GradeMaxPerHour = 5
	}
	if s.NtfyBase == "" {
		s.NtfyBase = ntfyBase
	}
	if s.NtfyRetryDelay <= 0 {
		s.NtfyRetryDelay = time.Minute
	}
	s.dyn = newDynSets(s.AutoTTL, d.Now)
	s.gradeSess = auth.NewLimiter(s.GradeMaxPerHour, time.Hour, d.Now)
	s.gradeIP = auth.NewLimiter(s.GradeMaxPerHour, time.Hour, d.Now)
	s.idem = newIdemStore(d.Attempts)
	s.quotaT = newQuotaTally(d.Attempts)
	s.nstop = make(chan struct{})
	s.initQueue()
	s.notified = map[string]bool{}
	s.ntfyTimeout, s.ntfyWait = d.NtfyAttemptTimeout, d.NtfyRequestWait
	if s.ntfyTimeout <= 0 {
		s.ntfyTimeout = ntfyAttemptTimeout
	}
	if s.ntfyWait <= 0 {
		s.ntfyWait = ntfyRequestWait
	}
	// The transport's phase limits sit just inside the per-attempt timeout, so
	// the attempt context (not an inner limit) is what ends a slow send.
	s.ntfyClient = &http.Client{
		Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: s.ntfyTimeout * 4 / 5}).DialContext,
			TLSHandshakeTimeout: s.ntfyTimeout * 4 / 5, ResponseHeaderTimeout: s.ntfyTimeout * 9 / 10},
		Timeout:       s.ntfyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s
}

// NewLimiter returns the production login limiter (5 failures / minute / IP).
func NewLimiter() *auth.Limiter { return auth.NewLimiter(loginFailMax, loginFailSpan, nil) }

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.code == 0 {
		w.code = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP dispatches without ServeMux so that odd paths (../, //, %2e%2e)
// are never "cleaned" into something that exists: they simply 404.
func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w := &statusWriter{ResponseWriter: rw}
	defer func() {
		if rec := recover(); rec != nil {
			s.Log.Error("panic in handler")
			if w.code == 0 {
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}
		lvl := slog.LevelInfo
		if r.URL.Path == "/healthz" {
			lvl = slog.LevelDebug
		}
		// Only the path is logged: never the query (tickets), headers or cookies.
		s.Log.Log(r.Context(), lvl, "request",
			"method", r.Method, "path", safePath(r.URL.Path), "status", w.code,
			"ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	}()
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")

	p := r.URL.Path
	switch {
	case p == "/healthz":
		h.Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	case p == "/api" || strings.HasPrefix(p, "/api/"):
		h.Set("Cache-Control", "no-store")
		s.api(w, r)
	case s.Embedded != nil && s.Embedded.Handles(p):
		s.Embedded.ServeHTTP(w, r)
	default:
		s.Static.ServeHTTP(w, r)
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/api/login":
		s.need(w, r, http.MethodPost, s.login)
	case p == "/api/logout":
		s.need(w, r, http.MethodPost, s.logout)
	case p == "/api/session":
		s.need(w, r, http.MethodGet, s.session)
	case p == "/api/config":
		s.need(w, r, http.MethodGet, s.authed(s.config))
	case strings.HasPrefix(p, "/api/sets/"):
		src := strings.TrimPrefix(p, "/api/sets/")
		s.need(w, r, http.MethodGet, s.authed(func(w http.ResponseWriter, r *http.Request) { s.sets(w, r, src) }))
	case p == "/api/practice" || strings.HasPrefix(p, "/api/practice/"):
		if s.Sets.Len() == 0 && len(s.auto) == 0 {
			writeErr(w, http.StatusNotFound, "not_found", "")
			return
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(p, "/api/practice"), "/")
		switch {
		case rest == "new":
			if len(s.auto) == 0 {
				writeErr(w, http.StatusNotFound, "not_found", "")
				return
			}
			s.need(w, r, http.MethodGet, s.authed(s.autoNew))
		case s.queueOn() && strings.HasPrefix(rest, "attempts/"): // off: falls through to the old behaviour
			s.queueRoute(w, r, rest)
		case strings.HasSuffix(rest, "/check"):
			id := strings.TrimSuffix(rest, "/check")
			s.need(w, r, http.MethodPost, s.authed(func(w http.ResponseWriter, r *http.Request) { s.checkLocal(w, r, id) }))
		case strings.HasSuffix(rest, "/quota"):
			id := strings.TrimSuffix(rest, "/quota")
			s.need(w, r, http.MethodGet, s.authed(func(w http.ResponseWriter, r *http.Request) { s.quota(w, r, id) }))
		case strings.HasSuffix(rest, "/grade"):
			id := strings.TrimSuffix(rest, "/grade")
			s.need(w, r, http.MethodPost, s.authed(func(w http.ResponseWriter, r *http.Request) { s.grade(w, r, id) }))
		default:
			s.need(w, r, http.MethodGet, s.authed(func(w http.ResponseWriter, r *http.Request) { s.practice(w, r, rest) }))
		}
	case strings.HasPrefix(p, "/api/media/"):
		rest := strings.TrimPrefix(p, "/api/media/")
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			writeErr(w, http.StatusNotFound, "not_found", "")
			return
		}
		src, sha := rest[:i], rest[i+1:]
		s.need(w, r, http.MethodGet, s.authed(func(w http.ResponseWriter, r *http.Request) { s.media(w, r, src, sha) }))
	default:
		writeErr(w, http.StatusNotFound, "not_found", "")
	}
}

func (s *Server) need(w http.ResponseWriter, r *http.Request, method string, h http.HandlerFunc) {
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	h(w, r)
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Auth.Authenticated(r) {
			writeErr(w, http.StatusUnauthorized, "login_required", "需要先输入家庭码")
			return
		}
		h(w, r)
	}
}

// ---- auth endpoints --------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])); mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return
	}
	ip := clientIP(r)
	if blocked, wait := s.Limiter.Blocked(ip); blocked {
		secs := int(wait/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.Log.Warn("login rate limited", "ip", ip)
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", "尝试次数太多，请稍后再试")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody))
	if err := dec.Decode(&body); err != nil {
		s.Limiter.Fail(ip)
		writeErr(w, http.StatusBadRequest, "bad_request", "")
		return
	}
	if !s.Auth.CheckCode(strings.TrimSpace(body.Code)) {
		s.Limiter.Fail(ip)
		s.Log.Warn("login failed", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "bad_code", "家庭码不对")
		return
	}
	s.Limiter.Reset(ip)
	http.SetCookie(w, s.Auth.Cookie())
	s.Log.Info("login ok", "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.Auth.ClearCookie())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": s.Auth.Authenticated(r)})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	var ntfy any
	if s.Topic != "" {
		ntfy = s.ntfyURL()
	}
	out := map[string]any{"ntfy_url": ntfy}
	if s.queueOn() {
		out["queue"] = true // only when on: with the queue off the answer stays byte-identical to #22
	}
	writeJSON(w, http.StatusOK, out)
}

// ntfyURL is "<NtfyBase>/<Topic>" with exactly one slash between them, whether
// or not NtfyBase ends in one. Both /api/config and the server-side push use it.
func (s *Server) ntfyURL() string {
	return strings.TrimRight(s.NtfyBase, "/") + "/" + s.Topic
}

// ---- brain proxy -----------------------------------------------------------

type optionOut struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

type mediaOut struct {
	URL       string `json:"url"`
	Slot      string `json:"slot"`
	OptionKey string `json:"option_key,omitempty"`
	Alt       string `json:"alt,omitempty"`
	Mime      string `json:"mime"`
	Ordinal   int    `json:"ordinal"`
}

type questionOut struct {
	Ordinal int         `json:"ordinal"`
	Number  string      `json:"number,omitempty"`
	QRef    string      `json:"qref"`
	QType   string      `json:"qtype"`
	Stem    string      `json:"stem"`
	Options []optionOut `json:"options,omitempty"`
	// FigureStatus is ok / missing / none / unknown (never empty).
	FigureStatus string     `json:"figure_status"`
	Media        []mediaOut `json:"media"`
}

func (s *Server) validSource(src string) bool {
	return len(src) <= 128 && s.srcRe.MatchString(src) && s.allow[src]
}

func (s *Server) sets(w http.ResponseWriter, r *http.Request, source string) {
	if !s.validSource(source) {
		writeErr(w, http.StatusNotFound, "unknown_source", "")
		return
	}
	items, err := s.Brain.Source(r.Context(), source, false)
	if err != nil {
		s.brainErr(w, "list", err)
		return
	}
	out := make([]questionOut, 0, len(items))
	for i, q := range items {
		qo := questionOut{Ordinal: i + 1, QRef: q.QRef, QType: q.QType, Stem: q.Stem, Media: []mediaOut{}, FigureStatus: q.Figure()}
		if qo.FigureStatus == brain.FigureOK && len(q.Media) == 0 {
			// data inconsistency on the brain side: says "has figure" but lists none
			s.Log.Warn("figure_status ok but media empty", "qref", q.QRef)
		}
		if q.Number != nil {
			qo.Number = *q.Number
		}
		if len(q.Options) > 0 && string(q.Options) != "null" {
			var opts []optionOut
			if json.Unmarshal(q.Options, &opts) == nil {
				for _, o := range opts {
					if o.Key != "" {
						qo.Options = append(qo.Options, o)
					}
				}
			} else {
				s.Log.Warn("options not in expected shape", "qref", q.QRef)
			}
		}
		for _, m := range q.Media {
			if m.Slot == "explanation" {
				continue // explanations are not shown before submission
			}
			qo.Media = append(qo.Media, mediaOut{
				URL:  "/api/media/" + source + "/" + m.SHA256,
				Slot: m.Slot, OptionKey: m.OptionKey, Alt: m.Alt, Mime: m.Mime, Ordinal: m.Ordinal,
			})
		}
		out = append(out, qo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": source, "count": len(out), "items": out})
}

func (s *Server) media(w http.ResponseWriter, r *http.Request, source, sha string) {
	if len(source) > 128 || !s.srcRe.MatchString(source) || !brain.ValidSHA(sha) {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	// Only figures that belong to this source's current questions are served.
	// A source that is allowed only through a practice set serves just the
	// figures of that set's whitelisted questions.
	var keep func(brain.Question) bool
	switch {
	case s.allow[source]:
	case s.Sets.HasSource(source) || s.dyn.hasSource(source):
		keep = func(q brain.Question) bool {
			return s.Sets.AllowsQRef(source, q.QRef) || s.dyn.allowsQRef(source, q.QRef)
		}
	default:
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	if _, err := s.Brain.FindMediaWhere(r.Context(), source, sha, keep); err != nil {
		s.brainErr(w, "media-lookup", err)
		return
	}
	etag := `"` + sha + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", mediaCacheCtrl)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	img, err := s.Brain.FetchMedia(r.Context(), sha)
	if err != nil {
		s.brainErr(w, "media", err)
		return
	}
	defer img.Close()

	var data []byte
	if img.ContentLength < 0 {
		data, err = io.ReadAll(io.LimitReader(img.Body, brain.MaxMediaBytes+1))
		if err != nil || len(data) > brain.MaxMediaBytes {
			s.brainErr(w, "media", &brain.Error{Kind: brain.KindTooLarge, What: "image body"})
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", img.ContentType)
	h.Set("Cache-Control", mediaCacheCtrl)
	h.Set("ETag", etag)
	h.Set("Content-Disposition", "inline")
	if data != nil {
		h.Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}
	h.Set("Content-Length", strconv.FormatInt(img.ContentLength, 10))
	_, _ = io.Copy(w, io.LimitReader(img.Body, img.ContentLength))
}

func (s *Server) brainErr(w http.ResponseWriter, op string, err error) {
	var be *brain.Error
	if !errors.As(err, &be) {
		be = &brain.Error{Kind: brain.KindUnavailable, What: "internal"}
	}
	// be.Error() is built only from fixed text, an HTTP status and an upstream
	// error code; it never contains URLs, headers or ticket values.
	s.Log.Warn("brain request failed", "op", op, "err", be.Error())
	switch be.Kind {
	case brain.KindNotFound:
		writeErr(w, http.StatusNotFound, "not_found", "")
	case brain.KindAuth:
		writeErr(w, http.StatusBadGateway, "brain_auth", "题库拒绝了练习站的访问，请通知家长")
	case brain.KindTicket:
		writeErr(w, http.StatusBadGateway, "brain_ticket", "图片暂时取不到，请刷新再试")
	default:
		writeErr(w, http.StatusBadGateway, "brain_unavailable", "题库暂时连不上，请稍后再试")
	}
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

func writeErr(w http.ResponseWriter, code int, id, msg string) {
	m := map[string]any{"error": id}
	if msg != "" {
		m["message"] = msg
	}
	writeJSON(w, code, m)
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func safePath(p string) string {
	if len(p) > 200 {
		p = p[:200]
	}
	return p
}

// RedactWriter wraps w and replaces every occurrence of each secret with
// "[redacted]" in everything written. It is a second line of defence behind
// the rule "never log secrets".
type RedactWriter struct {
	W       io.Writer
	Secrets [][]byte
}

// NewRedactWriter ignores empty secrets.
func NewRedactWriter(w io.Writer, secrets []string) *RedactWriter {
	rw := &RedactWriter{W: w}
	for _, s := range secrets {
		if s != "" {
			rw.Secrets = append(rw.Secrets, []byte(s))
			// also the URL-escaped and JSON-escaped forms
			if e := []byte(url.QueryEscape(s)); !bytes.Equal(e, []byte(s)) {
				rw.Secrets = append(rw.Secrets, e)
			}
		}
	}
	return rw
}

func (r *RedactWriter) Write(p []byte) (int, error) {
	q := p
	for _, s := range r.Secrets {
		if bytes.Contains(q, s) {
			q = bytes.ReplaceAll(q, s, []byte("[redacted]"))
		}
	}
	if _, err := r.W.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}
