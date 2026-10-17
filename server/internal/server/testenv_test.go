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

	ruyisite "github.com/abcd9845/ruyi-english-daily/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/config"
	"github.com/abcd9845/ruyi-english-daily/server/internal/embedded"
	"github.com/abcd9845/ruyi-english-daily/server/internal/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
	"github.com/abcd9845/ruyi-english-daily/server/internal/staticfiles"
)

const testSource = "math-intake.zz-yuekao-numberline-4y"

// practiceSrc is the synthetic source behind the practice-set tests.
const practiceSrc = "math.synthetic.p0-demo"
const shaP1 = "7777777777777777777777777777777777777777777777777777777777777777"
const shaP4 = "8888888888888888888888888888888888888888888888888888888888888888"

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

	// grading (contract): see handleGrade
	gradeToken string
	// gradeMode: "" ok | "fail" (500 always) | "fail-once" (500 first call) | "drop-once" (connection
	// cut first call) | "off" (plain 404: route not deployed) | "off-json" (404 {"error":{"code":"not_found"}})
	// | "501" | "norecord" (recorded:false everywhere) | "429" | "rev" (409 rev_mismatch) |
	// "idem" (409 idempotency_conflict) | "unknown" (404 unknown_qref)
	gradeMode   string
	gradeReqs   [][]byte
	gradeAuths  []string          // Authorization header of every grade call
	gradeSeen   map[string]string // ps:<cid>:<qref> -> canonical answer
	gradeIDs    map[string]int64  // ps:<cid>:<qref> -> attempt id
	gradeDayCnt map[string]int    // qref -> writes today
	gradeSeq    int64
	gradeBank   int // grade calls that used the bank:read token (must stay 0)
	gradeFirst  bool

	// lookup / pick-practice (contract)
	lookupMode string // "" ok | "off" | "501" | "fail"
	lookupReqs [][]byte
	revs       map[string]int // qref -> current rev (default 1)
	pickMode   string         // "" ok | "off" | "501" | "fail"
	pickQuery  []url.Values
	pickAuths  []string
}

func newFakeBrain(t *testing.T) *fakeBrain {
	f := &fakeBrain{token: rnd(t, "tok-"), gradeToken: rnd(t, "gtok-"), calls: map[string]int{}, gradeSeen: map[string]string{}, gradeIDs: map[string]int64{}, gradeDayCnt: map[string]int{}, revs: map[string]int{}}
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
		if r.URL.Query().Get("source") == practiceSrc {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": f.practiceItems(), "next_cursor": nil})
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
	mux.HandleFunc("POST /v1/bank/grade", f.handleGrade)
	mux.HandleFunc("POST /v1/bank/questions/lookup", f.handleLookup)
	mux.HandleFunc("GET /v1/brain/pick-practice", f.handlePick)
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

func (f *fakeBrain) curRev(q string) int {
	if v, ok := f.revs[q]; ok {
		return v
	}
	return 1
}

func (f *fakeBrain) setRev(q string, v int) { f.mu.Lock(); f.revs[q] = v; f.mu.Unlock() }
func (f *fakeBrain) setGradeMode(m string) {
	f.mu.Lock()
	f.gradeMode = m
	f.gradeFirst = false
	f.mu.Unlock()
}
func (f *fakeBrain) setLookupMode(m string) { f.mu.Lock(); f.lookupMode = m; f.mu.Unlock() }
func (f *fakeBrain) setPickMode(m string)   { f.mu.Lock(); f.pickMode = m; f.mu.Unlock() }
func (f *fakeBrain) lastGradeReq() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gradeReqs) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(f.gradeReqs[len(f.gradeReqs)-1], &m)
	return m
}
func (f *fakeBrain) gradeCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.gradeReqs) }

func jerrf(w http.ResponseWriter, code int, c string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"code":%q}}`, c)
}

// switchReply answers the generic "route not deployed" modes. true = handled.
func switchReply(w http.ResponseWriter, r *http.Request, mode string) bool {
	switch mode {
	case "off":
		http.NotFound(w, r)
	case "501":
		jerrf(w, 501, "not_implemented")
	case "fail":
		jerrf(w, 500, "internal")
	default:
		return false
	}
	return true
}

func (f *fakeBrain) handleLookup(w http.ResponseWriter, r *http.Request) {
	f.bump("lookup")
	f.mu.Lock()
	defer f.mu.Unlock()
	if switchReply(w, r, f.lookupMode) {
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		jerrf(w, 401, "unauthorized")
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.lookupReqs = append(f.lookupReqs, body)
	var req struct {
		QRefs []string `json:"qrefs"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.QRefs) == 0 || len(req.QRefs) > 100 {
		jerrf(w, 400, "validation_failed")
		return
	}
	have := map[string]map[string]any{}
	for _, q := range f.practiceItems() {
		have[q["qref"].(string)] = q
	}
	have["example.set:math:11"] = map[string]any{"qref": "example.set:math:11", "source": practiceSrc, "number": "11", "qtype": "short_answer",
		"stem": strings.Repeat("x ________ ", 11), "media": []any{}, "figure_status": "none"} // 11 blanks: over the contract's 10
	items := []map[string]any{}
	missing := []string{}
	for _, ref := range req.QRefs {
		q, ok := have[ref]
		if !ok {
			missing = append(missing, ref)
			continue
		}
		c := map[string]any{}
		for k, v := range q {
			c[k] = v // the fake keeps answer/explanation markers on purpose: the site must strip them
		}
		c["rev"] = f.curRev(ref)
		items = append(items, c)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "missing": missing})
}

func (f *fakeBrain) handlePick(w http.ResponseWriter, r *http.Request) {
	f.bump("pick")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pickQuery = append(f.pickQuery, r.URL.Query())
	f.pickAuths = append(f.pickAuths, r.Header.Get("Authorization"))
	if switchReply(w, r, f.pickMode) {
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		jerrf(w, 401, "unauthorized")
		return
	}
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	if q.Get("brain") != "ruyi" || n < 1 || (q.Get("mode") != "due_first" && q.Get("mode") != "wrong_only") || q.Get("source") != practiceSrc {
		jerrf(w, 400, "validation_failed")
		return
	}
	type pk struct {
		QRef    string   `json:"qref"`
		Number  string   `json:"number"`
		Origin  string   `json:"origin"`
		Reasons []string `json:"reasons"`
	}
	all := []pk{
		{"example.set:math:2", "2", "review", []string{"due"}},
		{"example.set:math:1", "1", "wrong", []string{"unresolved_wrong"}},
		{"example.set:math:3", "3", "wrong", []string{"unresolved_wrong", "deferred:pending_key"}},
		{"example.set:math:4", "4", "wrong", []string{"deferred:unsupported_qtype"}},
		{"example.set:math:2", "2", "wrong", []string{"dup-of-the-first"}}, // duplicate: ignored
		{"example.set:math:5", "5", "bogus-origin", []string{"deferred:figure_missing", "<script>"}},
	}
	if q.Get("mode") == "wrong_only" {
		all = all[1:]
	}
	if len(all) > n {
		all = all[:n]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": all})
}

func canonJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// handleGrade is the contract grade endpoint (see internal/brain/grade.go).
func (f *fakeBrain) handleGrade(w http.ResponseWriter, r *http.Request) {
	f.bump("grade")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gradeAuths = append(f.gradeAuths, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	switch r.Header.Get("Authorization") {
	case "Bearer " + f.gradeToken:
	case "Bearer " + f.token:
		f.gradeBank++
		jerrf(w, 403, "forbidden")
		return
	default:
		jerrf(w, 401, "unauthorized")
		return
	}
	f.gradeReqs = append(f.gradeReqs, body)
	switch f.gradeMode {
	case "off":
		http.NotFound(w, r)
		return
	case "off-json":
		jerrf(w, 404, "not_found")
		return
	case "501":
		jerrf(w, 501, "not_implemented")
		return
	case "fail":
		jerrf(w, 500, "internal")
		return
	case "fail-once", "drop-once":
		if !f.gradeFirst {
			f.gradeFirst = true
			if f.gradeMode == "drop-once" {
				if hj, ok := w.(http.Hijacker); ok {
					c, _, _ := hj.Hijack()
					_ = c.Close()
					return
				}
			}
			jerrf(w, 500, "internal")
			return
		}
	case "429":
		jerrf(w, 429, "rate_limited")
		return
	case "rev":
		jerrf(w, 409, "rev_mismatch")
		return
	case "idem":
		jerrf(w, 409, "idempotency_conflict")
		return
	case "unknown":
		jerrf(w, 404, "unknown_qref")
		return
	}
	var req struct {
		Brain string `json:"brain"`
		CID   string `json:"client_attempt_id"`
		Items []struct {
			QRef   string `json:"qref"`
			Answer any    `json:"answer"`
			Rev    *int   `json:"rev"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &req) != nil || req.Brain != "ruyi" || req.CID == "" || len(req.Items) == 0 || len(req.Items) > 50 {
		jerrf(w, 400, "validation_failed")
		return
	}
	for _, it := range req.Items {
		if it.Rev != nil && *it.Rev != f.curRev(it.QRef) {
			jerrf(w, 409, "rev_mismatch")
			return
		}
	}
	var items []map[string]any
	for _, it := range req.Items {
		key := "ps:" + req.CID + ":" + it.QRef
		a := canonJSON(it.Answer)
		if prev, ok := f.gradeSeen[key]; ok && prev != a {
			jerrf(w, 409, "idempotency_conflict")
			return
		}
	}
	for _, it := range req.Items {
		key := "ps:" + req.CID + ":" + it.QRef
		a := canonJSON(it.Answer)
		o := map[string]any{"qref": it.QRef, "status": "pending_key", "recorded": f.gradeMode != "norecord", "duplicate": false}
		switch {
		case strings.Contains(a, "~review~"):
			o["status"] = "needs_review"
		case it.QRef == "example.set:math:1":
			o["status"], o["correct"] = "graded", a == `["1","2","3"]`
		case it.QRef == "example.set:math:2":
			o["status"], o["correct"] = "graded", a == `"B"`
		case it.QRef == "example.set:math:4":
			o["status"], o["recorded"] = "unsupported", false
		}
		if o["recorded"] == true {
			if id, ok := f.gradeIDs[key]; ok {
				o["attempt_id"], o["duplicate"] = id, true
				f.calls["grade_dup_item"]++
			} else {
				f.gradeSeq++
				f.gradeIDs[key] = f.gradeSeq
				o["attempt_id"] = f.gradeSeq
				f.gradeSeen[key] = a
				f.calls["grade_new_item"]++
			}
		}
		items = append(items, o)
	}
	items = append(items, map[string]any{"qref": "example.set:math:5", "status": "graded", "correct": true, "recorded": true, "attempt_id": 99, "duplicate": false}) // never asked
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"client_attempt_id": req.CID, "items": items})
}

// practiceItems: 5 synthetic questions in scrambled order. Every one carries
// answer/explanation/solution fields (and an answer flag inside an option) that
// the site must strip. q1 and q4 have a figure.
func (f *fakeBrain) practiceItems() []map[string]any {
	mk := func(n int, qtype, stem string, opts any) map[string]any {
		q := map[string]any{"qref": fmt.Sprintf("example.set:math:%d", n), "source": practiceSrc, "number": strconv.Itoa(n),
			"qtype": qtype, "stem": stem, "media": []any{},
			"answer": "SECRET-ANSWER-MARK", "explanation": "SECRET-EXPLANATION-MARK", "solution": "SECRET-SOLUTION-MARK",
			"include_answer": true, "figure_status": "unknown"}
		if opts != nil {
			q["options"] = opts
		}
		return q
	}
	q1 := mk(1, "short_answer", "a ________ b ____ c $x_____y$ d\n\n＿＿＿＿ e ___ f", nil)
	q1["media"] = []any{map[string]any{"sha256": shaP1, "mime": "image/png", "ordinal": 1, "slot": "stem"}}
	q1["figure_status"] = "ok"
	q2 := mk(2, "single_choice", "choose（ ）", []map[string]any{
		{"key": "A", "text": "$\\dfrac{1}{2}$", "answer": true}, {"key": "B", "text": "two", "is_correct": false}})
	q3 := mk(3, "calculation", "plain $|x|<2$ text", nil)
	q4 := mk(4, "short_answer", "alt ________", nil)
	q4["media"] = []any{map[string]any{"sha256": shaP4, "mime": "image/png", "ordinal": 1, "slot": "stem"}}
	q5 := mk(5, "short_answer", "alt2", nil)
	return []map[string]any{q4, q2, q5, q3, q1}
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
	attDir  string
	ts      *httptest.Server
	brain   *fakeBrain
	logs    *syncBuf
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
	sets     string // RUYI_SETS_FILE content; "" = no sets

	grade       bool          // configure the grade token
	attemptsDir string        // "" = a fresh temp dir
	noAttempts  bool          // Attempts store nil
	ntfyURL     string        // "" = ntfy base is a dead address
	maxPerHour  int           // 0 = default 5
	auto        []string      // RUYI_AUTO_SOURCES
	autoTTL     time.Duration // 0 = default
	retry       time.Duration // ntfy retry delay (0 = 150ms)
	now         func() time.Time
	reuse       *env // reuse brain + attempts dir of another env (restart simulation)

	lookup       string // fake brain lookup mode ("" ok | "off" | "501" | "fail")
	skipDeferred bool   // RUYI_PICK_SKIP_DEFERRED
}

func newEnv(t *testing.T, o opts) *env {
	t.Helper()
	e := &env{brain: newFakeBrain(t), logs: &syncBuf{}, code: rnd(t, "code-"), secret: rnd(t, "sec-"), topic: rnd(t, "topic-")}
	if o.reuse != nil {
		e.brain, e.code, e.secret, e.topic = o.reuse.brain, o.reuse.code, o.reuse.secret, o.reuse.topic
		o.attemptsDir = o.reuse.attDir
	}
	if o.noTopic {
		e.topic = ""
	}
	e.brain.setLookupMode(o.lookup)
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
	gt := ""
	if o.grade {
		gt = e.brain.gradeToken
	}
	bc, err := brain.New(brain.Config{BaseURL: u, Token: e.brain.token, GradeToken: gt})
	must(t, err)
	e.attDir = o.attemptsDir
	if e.attDir == "" {
		e.attDir = filepath.Join(t.TempDir(), "attempts")
	}
	var att *attempts.Store
	if !o.noAttempts {
		att = attempts.Open(e.attDir, slog.New(slog.NewTextHandler(e.logs, nil)))
	}
	ntfy := o.ntfyURL
	if ntfy == "" {
		ntfy = "http://127.0.0.1:1/"
	}
	retry := o.retry
	if retry == 0 {
		retry = 150 * time.Millisecond
	}
	secrets := []string{e.code, e.secret, e.brain.token, e.topic}
	log := slog.New(slog.NewTextHandler(server.NewRedactWriter(e.logs, nil), &slog.HandlerOptions{Level: slog.LevelDebug}))
	_ = secrets // logs are captured RAW (no redaction) so tests prove the code itself never logs secrets
	e.au = auth.New(e.code, e.secret, time.Hour, o.secure, nil)
	var reg *sets.Registry
	if o.sets != "" {
		reg, err = sets.Parse([]byte(o.sets), config.SourceRe().MatchString)
		must(t, err)
	}
	emb, err := embedded.New(ruyisite.Pages(), ruyisite.KaTeX())
	must(t, err)
	app := server.New(server.Deps{Auth: e.au, Limiter: server.NewLimiter(), Brain: bc, Static: h, Log: log,
		Sources: []string{testSource}, Topic: e.topic, Sets: reg, Embedded: emb,
		Attempts: att, NtfyBase: ntfy, NtfyRetryDelay: retry, GradeMaxPerHour: o.maxPerHour, AutoSources: o.auto, AutoTTL: o.autoTTL, SkipDeferred: o.skipDeferred, Now: o.now})
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

// syncBuf is a log sink that async goroutines (ntfy retry) may write to while a test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
