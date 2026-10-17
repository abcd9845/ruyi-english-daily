package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/server"
)

func TestHealthzIndependentOfBrain(t *testing.T) {
	e := newEnv(t, opts{brainURL: "http://127.0.0.1:1"})
	resp, _ := e.do(t, "GET", "/healthz", "", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("healthz %d", resp.StatusCode)
	}
}

func TestStaticAllowlistAndTraversal(t *testing.T) {
	e := newEnv(t, opts{})
	ok := map[string]string{"/": "<h1>home</h1>", "/index.html": "<h1>home</h1>", "/units/x/": "x", "/units/x/index.html": "x"}
	for p, want := range ok {
		resp, b := e.do(t, "GET", p, "", nil, "")
		if resp.StatusCode != 200 || string(b) != want {
			t.Errorf("%s: status %d body %q", p, resp.StatusCode, b)
		}
	}
	// /units/x -> /units/x/ like the old python server
	resp, _ := e.do(t, "GET", "/units/x", "", nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/units/x/" {
		t.Errorf("dir redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	notFound := []string{
		"/server.log", "/server.pid", "/start-server.sh", "/stop-server.sh", "/.git/config", "/.git/", "/.git",
		"/.env", "/../server.log", "/%2e%2e/server.log", "/%2E%2E/server.log", "/..%2fserver.log", "/units/x/../../server.log",
		"/units/", "/units", "/units/x/other.txt", "//server.log", "/index.html/", "/%00", "/units/x/index.html%00",
		"/.%2e/server.log", "/%2e%2e%2fserver.log", "/units/%2e%2e/server.log", "/./server.log", "/\\server.log", "/%5cserver.log",
		"/units//x/index.html", "/INDEX.HTML",
	}
	for _, p := range notFound {
		resp, b := e.do(t, "GET", p, "", nil, "")
		if resp.StatusCode != 404 {
			t.Errorf("%s: want 404 got %d", p, resp.StatusCode)
		}
		if bytes.Contains(b, []byte("SECRET-LOG")) || bytes.Contains(b, []byte("[core]")) || bytes.Contains(b, []byte("K=V")) {
			t.Errorf("%s leaked content", p)
		}
	}
	resp, _ = e.do(t, "POST", "/index.html", "", nil, "")
	if resp.StatusCode != 405 {
		t.Errorf("POST static: %d", resp.StatusCode)
	}
}

func cookieOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

func TestLoginWrongCodeNoCookie(t *testing.T) {
	e := newEnv(t, opts{})
	resp, _ := e.login(t, "wrong-code")
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Fatal("wrong code must not set a cookie")
	}
}

func TestLoginCookieAttributes(t *testing.T) {
	for _, secure := range []bool{false, true} {
		e := newEnv(t, opts{secure: secure})
		resp, _ := e.login(t, e.code)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		c := cookieOf(resp)
		if c == nil {
			t.Fatal("no cookie")
		}
		raw := resp.Header.Get("Set-Cookie")
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure != secure {
			t.Errorf("attrs wrong (secure=%v): %s", secure, redactCookie(raw))
		}
		if c.MaxAge != 3600 || c.Expires.IsZero() {
			t.Errorf("lifetime wrong: %v %v", c.MaxAge, c.Expires)
		}
		if strings.Contains(raw, e.code) || strings.Contains(raw, e.secret) {
			t.Error("cookie leaks code/secret")
		}
		// session endpoint
		_, b := e.do(t, "GET", "/api/session", "", nil, c.Value)
		if !strings.Contains(string(b), `"authenticated":true`) {
			t.Errorf("session: %s", b)
		}
		_, b = e.do(t, "GET", "/api/session", "", nil, "")
		if !strings.Contains(string(b), `"authenticated":false`) {
			t.Errorf("session (anon): %s", b)
		}
	}
}

func redactCookie(s string) string {
	i := strings.Index(s, "=")
	j := strings.Index(s, ";")
	if i < 0 || j < i {
		return s
	}
	return s[:i+1] + "…" + s[j:]
}

func TestLoginRequiresJSONContentType(t *testing.T) {
	e := newEnv(t, opts{})
	resp, _ := e.do(t, "POST", "/api/login", `{"code":"x"}`, map[string]string{"Content-Type": "text/plain"}, "")
	if resp.StatusCode != 415 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, opts{})
	for i := 1; i <= 5; i++ {
		resp, _ := e.login(t, "bad")
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp, _ := e.login(t, "bad")
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("6th: %d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	// even the right code is refused while blocked, and X-Forwarded-For does not help
	req := map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "203.0.113.9"}
	resp, _ = e.do(t, "POST", "/api/login", `{"code":"`+e.code+`"}`, req, "")
	if resp.StatusCode != 429 || cookieOf(resp) != nil {
		t.Fatalf("blocked login: %d", resp.StatusCode)
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := auth.NewLimiter(5, time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		l.Fail("1.2.3.4")
	}
	if b, _ := l.Blocked("1.2.3.4"); !b {
		t.Fatal("should be blocked")
	}
	if b, _ := l.Blocked("5.6.7.8"); b {
		t.Fatal("other IP must not be blocked")
	}
	now = now.Add(61 * time.Second)
	if b, _ := l.Blocked("1.2.3.4"); b {
		t.Fatal("should unblock after window")
	}
}

func TestCookieTamperExpiryAndRotation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	a := auth.New("family-code-1", "secret-secret-secret-1", time.Hour, false, clock)
	v, _ := a.Issue()
	if !a.Valid(v) {
		t.Fatal("fresh cookie must be valid")
	}
	parts := strings.Split(v, ".")
	// flip a MAC nibble
	mac := []byte(parts[2])
	if mac[0] == 'a' {
		mac[0] = 'b'
	} else {
		mac[0] = 'a'
	}
	for name, bad := range map[string]string{
		"flipped mac":     parts[0] + "." + parts[1] + "." + string(mac),
		"extended expiry": parts[0] + ".9999999999." + parts[2],
		"empty":           "",
		"garbage":         "not-a-cookie",
		"wrong version":   "v2." + parts[1] + "." + parts[2],
		"missing mac":     parts[0] + "." + parts[1] + ".",
		"non-hex mac":     parts[0] + "." + parts[1] + ".zz",
		"negative expiry": parts[0] + ".-5." + parts[2],
		"extra component": v + ".x",
	} {
		if a.Valid(bad) {
			t.Errorf("%s accepted", name)
		}
	}
	// expiry
	now = now.Add(61 * time.Minute)
	if a.Valid(v) {
		t.Error("expired cookie accepted")
	}
	now = time.Unix(1_800_000_000, 0)
	// changed code, same secret
	if auth.New("family-code-2", "secret-secret-secret-1", time.Hour, false, clock).Valid(v) {
		t.Error("cookie survives family code change")
	}
	// changed secret, same code
	if auth.New("family-code-1", "secret-secret-secret-2", time.Hour, false, clock).Valid(v) {
		t.Error("cookie survives secret change")
	}
	// same config (e.g. restart) still valid
	if !auth.New("family-code-1", "secret-secret-secret-1", time.Hour, false, clock).Valid(v) {
		t.Error("cookie must survive restart with same config")
	}
	if !a.CheckCode("family-code-1") || a.CheckCode("family-code-1 ") || a.CheckCode("") {
		t.Error("CheckCode wrong")
	}
}

func TestAPIRequiresCookie(t *testing.T) {
	e := newEnv(t, opts{})
	for _, p := range []string{"/api/sets/" + testSource, "/api/media/" + testSource + "/" + sha1x, "/api/config"} {
		resp, b := e.do(t, "GET", p, "", nil, "")
		if resp.StatusCode != 401 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
		if bytes.Contains(b, []byte(e.brain.token)) {
			t.Error("leak")
		}
		resp, _ = e.do(t, "GET", p, "", nil, "v1.9999999999.deadbeef")
		if resp.StatusCode != 401 {
			t.Errorf("%s with forged cookie: %d", p, resp.StatusCode)
		}
	}
	if e.brain.total() != 0 {
		t.Errorf("brain was contacted %d times without a session", e.brain.total())
	}
	// old cookie dies when the code changes (server restarted with a new code)
	c := e.session(t)
	resp, _ := e.do(t, "GET", "/api/config", "", nil, c)
	if resp.StatusCode != 200 {
		t.Fatalf("valid cookie: %d", resp.StatusCode)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	e := newEnv(t, opts{})
	resp, _ := e.do(t, "POST", "/api/logout", "", nil, e.session(t))
	c := cookieOf(resp)
	if resp.StatusCode != 200 || c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout: %d %+v", resp.StatusCode, c)
	}
}

func configNtfyURL(t *testing.T, o opts) (string, string) {
	t.Helper()
	e := newEnv(t, o)
	_, b := e.do(t, "GET", "/api/config", "", nil, e.session(t))
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("config body: %s", b)
	}
	u, _ := v["ntfy_url"].(string)
	return u, e.topic
}

func TestConfigNtfy(t *testing.T) {
	// not configured: the built-in default, unchanged behaviour
	empty := ""
	if u, topic := configNtfyURL(t, opts{ntfyRaw: &empty}); u != "https://ntfy.sh/"+topic {
		t.Errorf("default base: %s", u)
	}
	// configured base, with and without a trailing slash: exactly one slash
	for _, base := range []string{"https://ntfy-me.com", "https://ntfy-me.com/", "https://ntfy-me.com///"} {
		base := base
		if u, topic := configNtfyURL(t, opts{ntfyRaw: &base}); u != "https://ntfy-me.com/"+topic {
			t.Errorf("base %q: %s", base, u)
		}
	}
	// no topic: null, whatever the base
	e2 := newEnv(t, opts{noTopic: true})
	_, b := e2.do(t, "GET", "/api/config", "", nil, e2.session(t))
	if !strings.Contains(string(b), `"ntfy_url":null`) {
		t.Errorf("config without topic: %s", b)
	}
	custom := "https://ntfy-me.com"
	e3 := newEnv(t, opts{noTopic: true, ntfyRaw: &custom})
	_, b = e3.do(t, "GET", "/api/config", "", nil, e3.session(t))
	if !strings.Contains(string(b), `"ntfy_url":null`) {
		t.Errorf("config without topic, custom base: %s", b)
	}
}

func TestSourceInjectionRejected(t *testing.T) {
	e := newEnv(t, opts{})
	c := e.session(t)
	bad := []string{
		"../../v1/bank/sources", "..%2f..%2fv1%2fbank%2fsources", "%2e%2e/x", "a/b", "a%2fb", testSource + "%3Fx%3D1",
		testSource + "&include_answer=true", testSource + "%00", testSource + "%0d%0aX:1",
		"MATH-INTAKE", strings.Repeat("a", 300), "", "http:%2f%2fevil.example", "a b", testSource + "/", "-x", "x",
		"valid.syntax.but-not-allowed", testSource + "%20", "%e4%b8%ad%e6%96%87", "a;b", "a@b",
	}
	for _, s := range bad {
		resp, _ := e.do(t, "GET", "/api/sets/"+s, "", nil, c)
		if resp.StatusCode != 404 {
			t.Errorf("sets %q: %d", s, resp.StatusCode)
		}
		resp, _ = e.do(t, "GET", "/api/media/"+s+"/"+sha1x, "", nil, c)
		if resp.StatusCode != 404 {
			t.Errorf("media %q: %d", s, resp.StatusCode)
		}
	}
	for _, sha := range []string{"short", strings.Repeat("A", 64), sha1x + "00", sha1x + "%2f..", "../" + sha1x, sha1x + "%3Fticket%3Dx", "", "z" + sha1x[1:]} {
		resp, _ := e.do(t, "GET", "/api/media/"+testSource+"/"+sha, "", nil, c)
		if resp.StatusCode != 404 {
			t.Errorf("sha %q: %d", sha, resp.StatusCode)
		}
	}
	// well-formed sha that is not part of the source: 404 without minting a ticket
	resp, _ := e.do(t, "GET", "/api/media/"+testSource+"/"+strings.Repeat("9", 64), "", nil, c)
	if resp.StatusCode != 404 {
		t.Errorf("foreign sha: %d", resp.StatusCode)
	}
	if e.brain.count("ticket") != 0 || e.brain.count("media") != 0 {
		t.Errorf("proxy contacted brain for rejected input: tickets=%d media=%d", e.brain.count("ticket"), e.brain.count("media"))
	}
	for _, p := range []string{"/api", "/api/", "/api/unknown", "/api/sets", "/api/media/", "/api/media/" + testSource} {
		resp, _ := e.do(t, "GET", p, "", nil, c)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "POST", "/api/sets/"+testSource, "", nil, c); resp.StatusCode != 405 {
		t.Errorf("POST sets: %d", resp.StatusCode)
	}
}

func TestSetsShape(t *testing.T) {
	e := newEnv(t, opts{})
	resp, b := e.do(t, "GET", "/api/sets/"+testSource, "", nil, e.session(t))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if bytes.Contains(b, []byte(e.brain.token)) || bytes.Contains(b, []byte("Bearer")) || bytes.Contains(b, []byte("ticket")) {
		t.Fatal("response leaks credentials")
	}
	var v struct {
		Count int `json:"count"`
		Items []struct {
			Ordinal int    `json:"ordinal"`
			Number  string `json:"number"`
			QRef    string `json:"qref"`
			Stem    string `json:"stem"`
			Options []struct{ Key, Text string }
			Media   []struct {
				URL       string `json:"url"`
				Slot      string `json:"slot"`
				OptionKey string `json:"option_key"`
				Ordinal   int    `json:"ordinal"`
			}
		}
	}
	must(t, json.Unmarshal(b, &v))
	if v.Count != 60 || len(v.Items) != 60 {
		t.Fatalf("count %d", v.Count)
	}
	for i, it := range v.Items {
		if it.Ordinal != i+1 || it.Number != fakeNumber(i+1) || it.QRef != fmt.Sprintf("math:q%02d", i+1) {
			t.Fatalf("item %d: %+v", i, it)
		}
	}
	first := v.Items[0]
	if first.Number != "1(1)" || len(first.Media) != 2 {
		t.Fatalf("first: %+v", first)
	}
	// ordered by media ordinal; explanation / invalid / svg entries are dropped
	if first.Media[0].Slot != "stem" || first.Media[0].URL != "/api/media/"+testSource+"/"+sha1x ||
		first.Media[1].Slot != "option" || first.Media[1].OptionKey != "B" {
		t.Fatalf("media: %+v", first.Media)
	}
	if len(v.Items[2].Options) != 2 || v.Items[2].Options[0].Key != "A" {
		t.Fatalf("options: %+v", v.Items[2].Options)
	}
	if len(v.Items[1].Media) != 0 || !strings.Contains(string(b), `"media":[]`) {
		t.Fatal("no-media question must have empty media array")
	}
}

func TestMediaProxy(t *testing.T) {
	e := newEnv(t, opts{})
	c := e.session(t)
	u := "/api/media/" + testSource + "/" + sha1x
	resp, b := e.do(t, "GET", u, "", nil, c)
	if resp.StatusCode != 200 || !bytes.Equal(b, pngBytes) {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(resp.Header.Get("Cache-Control"), "private") {
		t.Errorf("headers: %v", resp.Header)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
	// second fetch reuses the cached ticket
	e.do(t, "GET", u, "", nil, c)
	if e.brain.count("ticket") != 1 {
		t.Errorf("tickets minted: %d (want 1, cached)", e.brain.count("ticket"))
	}
	if e.brain.count("media_with_auth") != 0 {
		t.Error("Authorization sent on ticketed media GET")
	}
	// conditional request
	resp, _ = e.do(t, "GET", u, "", map[string]string{"If-None-Match": `"` + sha1x + `"`}, c)
	if resp.StatusCode != 304 {
		t.Errorf("etag: %d", resp.StatusCode)
	}
}

func TestMediaTicketExpiredRetriesOnce(t *testing.T) {
	e := newEnv(t, opts{})
	c := e.session(t)
	u := "/api/media/" + testSource + "/" + sha1x
	e.do(t, "GET", u, "", nil, c) // caches a ticket
	e.brain.mu.Lock()
	e.brain.failFirstMedia = true
	e.brain.mu.Unlock()
	resp, b := e.do(t, "GET", u, "", nil, c)
	if resp.StatusCode != 200 || !bytes.Equal(b, pngBytes) {
		t.Fatalf("after expiry: %d", resp.StatusCode)
	}
	if e.brain.count("ticket") != 2 {
		t.Errorf("tickets: %d (want 2: initial + one refresh)", e.brain.count("ticket"))
	}
	// permanently failing tickets: one retry only, then 502
	e.brain.mu.Lock()
	e.brain.alwaysExpired = true
	e.brain.mu.Unlock()
	before := e.brain.count("media")
	resp, _ = e.do(t, "GET", u, "", nil, c)
	if resp.StatusCode != 502 {
		t.Errorf("want 502, got %d", resp.StatusCode)
	}
	if got := e.brain.count("media") - before; got != 2 {
		t.Errorf("media GET attempts: %d (want 2)", got)
	}
}

func TestBrainUnreachableIsReadable(t *testing.T) {
	e := newEnv(t, opts{brainURL: "http://127.0.0.1:1"})
	c := e.session(t)
	resp, b := e.do(t, "GET", "/api/sets/"+testSource, "", nil, c)
	if resp.StatusCode != 502 || !strings.Contains(string(b), "brain_unavailable") || !strings.Contains(string(b), "题库暂时连不上") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if resp, _ := e.do(t, "GET", "/", "", nil, ""); resp.StatusCode != 200 {
		t.Error("static page must stay up")
	}
}

func TestLogsContainNoSecrets(t *testing.T) {
	e := newEnv(t, opts{})
	// exercise every path, including failures
	e.login(t, "definitely-wrong")
	e.login(t, e.code+"x")
	c := e.session(t)
	e.do(t, "GET", "/api/config", "", nil, c)
	e.do(t, "GET", "/api/sets/"+testSource, "", nil, c)
	e.do(t, "GET", "/api/media/"+testSource+"/"+sha1x, "", nil, c)
	e.do(t, "GET", "/api/media/"+testSource+"/"+sha1x+"?ticket=SHOULD-NOT-BE-LOGGED&x=1", "", nil, c)
	e.do(t, "GET", "/api/sets/..%2fx?include_answer=true", "", nil, c)
	e.do(t, "GET", "/index.html?secretquery=QQQ", "", map[string]string{"Authorization": "Bearer HEADER-SECRET"}, c)
	e.brain.mu.Lock()
	e.brain.alwaysExpired = true
	e.brain.mu.Unlock()
	e.do(t, "GET", "/api/media/"+testSource+"/"+sha1x, "", nil, c)
	e.brain.mu.Lock()
	tickets := append([]string(nil), e.brain.tickets...)
	e.brain.mu.Unlock()
	// now kill the brain: transport errors would embed the ticket URL if passed through
	e.brain.mu.Lock()
	e.brain.alwaysExpired = false
	e.brain.mu.Unlock()
	e.brain.Close()
	e.do(t, "GET", "/api/media/"+testSource+"/"+sha1x, "", nil, c)
	e.do(t, "GET", "/api/media/"+testSource+"/"+sha5b, "", nil, c)

	logs := e.logs.String()
	if logs == "" {
		t.Fatal("no logs captured")
	}
	forbidden := []string{e.code, e.secret, e.brain.token, e.topic, c, "SHOULD-NOT-BE-LOGGED", "secretquery", "QQQ", "HEADER-SECRET",
		"Bearer", "Authorization", "ticket=", "Set-Cookie", "include_answer"}
	forbidden = append(forbidden, tickets...)
	for _, f := range forbidden {
		if f != "" && strings.Contains(logs, f) {
			t.Errorf("log contains forbidden string (len %d): %q", len(f), shorten(f))
		}
	}
	if !strings.Contains(logs, "login failed") || !strings.Contains(logs, "brain request failed") {
		t.Errorf("expected log events missing:\n%s", logs)
	}
}

func shorten(s string) string {
	if len(s) > 6 {
		return s[:3] + "…"
	}
	return s
}

func TestRedactWriter(t *testing.T) {
	var buf bytes.Buffer
	w := server.NewRedactWriter(&buf, []string{"s3cret-value", "", "a b&c"})
	n, err := w.Write([]byte("x s3cret-value y a%20b%26c a+b%26c\n"))
	if err != nil || n != len("x s3cret-value y a%20b%26c a+b%26c\n") {
		t.Fatal(n, err)
	}
	if strings.Contains(buf.String(), "s3cret-value") || strings.Contains(buf.String(), "a+b%26c") {
		t.Fatalf("not redacted: %q", buf.String())
	}
}

func TestFigureStatusPassThroughAndWarn(t *testing.T) {
	e := newEnv(t, opts{})
	_, b := e.do(t, "GET", "/api/sets/"+testSource, "", nil, e.session(t))
	var v struct {
		Items []struct {
			FigureStatus string `json:"figure_status"`
			Media        []any  `json:"media"`
		}
	}
	must(t, json.Unmarshal(b, &v))
	want := []string{"ok", "missing", "unknown", "none", "ok", "unknown"} // ok, missing, key absent, none, ok-but-empty, bogus value
	for i, w := range want {
		if v.Items[i].FigureStatus != w {
			t.Errorf("item %d figure_status=%q want %q", i+1, v.Items[i].FigureStatus, w)
		}
	}
	for i, it := range v.Items {
		if it.FigureStatus == "" {
			t.Errorf("item %d: figure_status must never be empty", i+1)
		}
	}
	if len(v.Items[4].Media) != 0 {
		t.Error("q05 should have no media")
	}
	logs := e.logs.String()
	if strings.Count(logs, "figure_status ok but media empty") != 1 || !strings.Contains(logs, "math:q05") {
		t.Errorf("expected exactly one warn for q05 (ok but empty):\n%s", logs)
	}
	if strings.Contains(logs, "math:q01") && strings.Contains(logs, "figure_status ok but media empty qref=math:q01") {
		t.Error("q01 (ok with media) must not warn")
	}
}

// The client may only send source, limit and cursor to the list endpoint; the
// brain rejects other parameters (and include_answer needs bank:write).
func TestListQueryOnlySourceLimitCursor(t *testing.T) {
	e := newEnv(t, opts{})
	c := e.session(t)
	e.do(t, "GET", "/api/sets/"+testSource+"?include_answer=true&status=active&subject=math", "", nil, c)
	e.brain.mu.Lock()
	defer e.brain.mu.Unlock()
	if len(e.brain.listQueries) == 0 {
		t.Fatal("brain not called")
	}
	for _, q := range e.brain.listQueries {
		for k := range q {
			if k != "source" && k != "limit" && k != "cursor" {
				t.Errorf("client sent forbidden list parameter %q", k)
			}
		}
		if q.Get("source") != testSource || q.Get("limit") != "500" {
			t.Errorf("unexpected source/limit: %v", q)
		}
	}
}
