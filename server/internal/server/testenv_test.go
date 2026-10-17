package server_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/staticfiles"
)

const testSource = "math-intake.zz-yuekao-numberline-4y"

func rnd(t testing.TB, prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(b)
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-image-bytes")

const sha1x = "1111111111111111111111111111111111111111111111111111111111111111"
const sha5b = "5555555555555555555555555555555555555555555555555555555555555555"

// fakeBrain implements just enough of the contract for unit tests.
type fakeBrain struct {
	*httptest.Server
	token string
	mu    sync.Mutex
	calls map[string]int
	// failFirstMedia makes the first GET /v1/media with any ticket answer 401.
	failFirstMedia bool
	alwaysExpired  bool
	tickets        []string
	listQueries    []url.Values // every query the client sent to the list endpoint
}

func newFakeBrain(t *testing.T) *fakeBrain {
	f := &fakeBrain{token: rnd(t, "tok-"), calls: map[string]int{}}
	mux := http.NewServeMux()
	jerr := func(w http.ResponseWriter, code int, c string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"error":{"code":%q}}`, c)
	}
	mux.HandleFunc("GET /v1/bank/questions", func(w http.ResponseWriter, r *http.Request) {
		f.bump("list")
		f.mu.Lock()
		f.listQueries = append(f.listQueries, r.URL.Query())
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			jerr(w, 401, "unauthorized")
			return
		}
		var items []map[string]any
		for n := 1; n <= 60; n++ {
			q := map[string]any{"qref": fmt.Sprintf("math:q%02d", n), "source": testSource, "number": fakeNumber(n),
				"qtype": "fill_blank", "stem": fmt.Sprintf("stem %d <b>x</b>", n), "media": []any{}}
			switch n {
			case 1:
				q["figure_status"] = "ok"
			case 2:
				q["figure_status"] = "missing"
			case 4:
				q["figure_status"] = "none"
			case 5:
				q["figure_status"] = "ok" // inconsistent: ok but no media
			case 6:
				q["figure_status"] = "bogus"
			}
			if n == 3 {
				q["qtype"] = "single_choice"
				q["options"] = json.RawMessage(`[{"key":"A","text":"1"},{"key":"B","text":"2"}]`)
			}
			if n == 1 {
				q["media"] = []any{
					map[string]any{"sha256": sha5b, "mime": "image/png", "ordinal": 2, "slot": "option", "option_key": "B"},
					map[string]any{"sha256": sha1x, "mime": "image/png", "ordinal": 1, "slot": "stem", "alt": "d"},
					map[string]any{"sha256": strings.Repeat("e", 64), "mime": "image/png", "ordinal": 3, "slot": "explanation"},
					map[string]any{"sha256": "short", "mime": "image/png", "ordinal": 4, "slot": "stem"},
					map[string]any{"sha256": strings.Repeat("d", 64), "mime": "image/svg+xml", "ordinal": 5, "slot": "stem"},
				}
			}
			items = append(items, q)
		}
		// scrambled order (the real list is updated_at DESC, not number order)
		scr := make([]map[string]any, 60)
		for i := range items {
			scr[(i*37+11)%60] = items[i]
		}
		items = scr
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": nil})
	})
	mux.HandleFunc("POST /v1/media/{sha}/ticket", func(w http.ResponseWriter, r *http.Request) {
		f.bump("ticket")
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			jerr(w, 401, "unauthorized")
			return
		}
		f.mu.Lock()
		tk := fmt.Sprintf("v1.%d.%s", time.Now().Add(15*time.Minute).Unix(), rnd(t, "tk"))
		f.tickets = append(f.tickets, tk)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": tk, "expires_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /v1/media/{sha}", func(w http.ResponseWriter, r *http.Request) {
		f.bump("media")
		if r.Header.Get("Authorization") != "" {
			f.bump("media_with_auth")
		}
		f.mu.Lock()
		first := f.failFirstMedia
		f.failFirstMedia = false
		always := f.alwaysExpired
		f.mu.Unlock()
		if first || always {
			jerr(w, 401, "ticket_expired")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// fakeNumber is the number of the question at sorted position n: "1(1)", "1(2)", "1(3)", "2(1)" …
// with a circled suffix on position 12 ("4(3)③") to look like the real exam.
func fakeNumber(n int) string {
	s := fmt.Sprintf("%d(%d)", (n-1)/3+1, (n-1)%3+1)
	if n == 12 {
		s += "③"
	}
	return s
}

func (f *fakeBrain) bump(k string) { f.mu.Lock(); f.calls[k]++; f.mu.Unlock() }
func (f *fakeBrain) count(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[k]
}
func (f *fakeBrain) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.calls {
		n += v
	}
	return n
}

type env struct {
	ts      *httptest.Server
	brain   *fakeBrain
	logs    *bytes.Buffer
	code    string
	secret  string
	topic   string
	au      *auth.Authenticator
	staticD string
}

type opts struct {
	secure   bool
	noTopic  bool
	brainURL string // override (e.g. dead address)
}

func newEnv(t *testing.T, o opts) *env {
	t.Helper()
	e := &env{brain: newFakeBrain(t), logs: &bytes.Buffer{}, code: rnd(t, "code-"), secret: rnd(t, "sec-"), topic: rnd(t, "topic-")}
	if o.noTopic {
		e.topic = ""
	}
	e.staticD = t.TempDir()
	must(t, os.WriteFile(filepath.Join(e.staticD, "index.html"), []byte("<h1>home</h1>"), 0o644))
	must(t, os.WriteFile(filepath.Join(e.staticD, "server.log"), []byte("SECRET-LOG"), 0o644))
	must(t, os.MkdirAll(filepath.Join(e.staticD, "units", "x"), 0o755))
	must(t, os.WriteFile(filepath.Join(e.staticD, "units", "x", "index.html"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(e.staticD, "units", "x", "other.txt"), []byte("not allowed"), 0o644))
	must(t, os.MkdirAll(filepath.Join(e.staticD, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(e.staticD, ".git", "config"), []byte("[core]"), 0o644))
	must(t, os.WriteFile(filepath.Join(e.staticD, ".env"), []byte("K=V"), 0o644))
	h, _ := staticfiles.New(e.staticD, []string{"index.html", "units/x/index.html"})

	bu := o.brainURL
	if bu == "" {
		bu = e.brain.URL
	}
	u, _ := url.Parse(bu)
	bc, err := brain.New(brain.Config{BaseURL: u, Token: e.brain.token})
	must(t, err)
	secrets := []string{e.code, e.secret, e.brain.token, e.topic}
	log := slog.New(slog.NewTextHandler(server.NewRedactWriter(e.logs, nil), &slog.HandlerOptions{Level: slog.LevelDebug}))
	_ = secrets // logs are captured RAW (no redaction) so tests prove the code itself never logs secrets
	e.au = auth.New(e.code, e.secret, time.Hour, o.secure, nil)
	app := server.New(server.Deps{Auth: e.au, Limiter: server.NewLimiter(), Brain: bc, Static: h, Log: log,
		Sources: []string{testSource}, Topic: e.topic})
	e.ts = httptest.NewServer(app)
	t.Cleanup(e.ts.Close)
	return e
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// do sends a request without following redirects. rawPath is used verbatim.
func (e *env) do(t *testing.T, method, rawPath string, body string, hdr map[string]string, cookie string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+"/", strings.NewReader(body))
	must(t, err)
	pu, err := url.ParseRequestURI(rawPath)
	must(t, err)
	req.URL = &url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host, Path: pu.Path, RawPath: pu.RawPath, RawQuery: pu.RawQuery}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) login(t *testing.T, code string) (*http.Response, []byte) {
	return e.do(t, "POST", "/api/login", `{"code":`+strconv.Quote(code)+`}`, map[string]string{"Content-Type": "application/json"}, "")
}

func (e *env) session(t *testing.T) string {
	t.Helper()
	resp, _ := e.login(t, e.code)
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatal("no cookie")
	return ""
}
