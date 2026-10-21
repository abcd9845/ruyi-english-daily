package server_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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
	"regexp"
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
	"github.com/abcd9845/ruyi-english-daily/server/internal/mockqueue"
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
	gradeAuths  []string             // Authorization header of every grade call
	gradeSeen   map[string]string    // ps:<cid>:<qref> -> canonical answer
	gradeIDs    map[string]int64     // ps:<cid>:<qref> -> attempt id
	gradeAt     map[string]time.Time // ps:<cid>:<qref> -> answered_at the key was created with (zero = none)
	gradeDayCnt map[string]int       // qref -> writes today
	gradeSeq    int64
	gradeQuery  []string // raw query of every grade call (must stay empty)
	gradeBank   int      // grade calls that used the bank:read token (must stay 0)
	gradeFirst  bool

	// lookup / pick-practice (contract)
	lookupMode string // "" ok | "off" | "501" | "fail"
	lookupReqs [][]byte
	revs       map[string]int // qref -> current rev (default 1)
	pickMode   string         // "" ok | "off" | "501" | "fail"
	assigned   []string       // open assignments (any source) returned first when assigned=1
	pickQuery  []url.Values
	pickAuths  []string
	pickBank   int // pick-practice calls that used the bank:read token (refused with 403)

	// pending-answer queue mock (contract v1.0) and its third (notify) token
	q           *mockqueue.Mock
	notifyToken string
	queueHints  bool // grade items carry queueable / queueable_reason

	// brainID is the one brain this fake serves (the real brain's token scope); "" = "ruyi".
	brainID string
}

func (f *fakeBrain) wantBrain() string {
	if f.brainID == "" {
		return "ruyi"
	}
	return f.brainID
}

func newFakeBrain(t *testing.T) *fakeBrain {
	f := &fakeBrain{token: rnd(t, "tok-"), gradeToken: rnd(t, "gtok-"), calls: map[string]int{}, gradeSeen: map[string]string{}, gradeIDs: map[string]int64{}, gradeAt: map[string]time.Time{}, gradeDayCnt: map[string]int{}, revs: map[string]int{}}
	mux := http.NewServeMux()
	jerr := func(w http.ResponseWriter, code int, c string) { jerrf(w, code, c) }
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
	f.notifyToken = rnd(t, "ntok-")
	f.q = mockqueue.New(mockqueue.Config{Brain: "ruyi", WriteToken: f.gradeToken, NotifyToken: f.notifyToken, Env: fakeQueueEnv{f}})
	f.q.Register(mux)
	// answer images (大脑 #57 PR-I1 contract): write token, jpeg/png, sha must match; "QUOTA..." → 429
	mux.HandleFunc("PUT /v1/brains/{slug}/answer-media/{sha}", func(w http.ResponseWriter, r *http.Request) {
		f.bump("answer_media")
		if r.Header.Get("Authorization") != "Bearer "+f.gradeToken {
			jerr(w, 403, "forbidden")
			return
		}
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte("HTML413")) { // what nginx does above its body limit
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(413)
			_, _ = io.WriteString(w, "<html><body>413 Request Entity Too Large</body></html>")
			return
		}
		if bytes.Contains(b, []byte("QUOTA")) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":{"code":"media_daily_quota","message":"quota","details":{"unit":"images","limit":60,"used":60,"retry_after_s":60}}}`)
			return
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != r.PathValue("sha") || r.PathValue("slug") != "ruyi" {
			jerr(w, 422, "sha_mismatch")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "sha256": r.PathValue("sha"), "mime": r.Header.Get("Content-Type"), "bytes": len(b), "created": true})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// fakeQueueEnv is the grade-side state the queue mock shares (attempt ids, day counts).
type fakeQueueEnv struct{ f *fakeBrain }

var fakeKnownQ = regexp.MustCompile(`^example\.set:(math|open):[1-9]$`)

func (e fakeQueueEnv) Lookup(q string) (string, bool) {
	return "pending_key", fakeKnownQ.MatchString(q)
}
func (e fakeQueueEnv) Attempt(key string) (int64, bool) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	id, ok := e.f.gradeIDs[key]
	return id, ok
}
func (e fakeQueueEnv) DayCount(q string) int {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	return e.f.gradeDayCnt[q]
}
func (e fakeQueueEnv) Seen(key string) (string, time.Time, bool) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	_, ok := e.f.gradeIDs[key]
	return "", e.f.gradeAt[key], ok
}
func (e fakeQueueEnv) Write(key, q, canon string, at time.Time) int64 {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	e.f.gradeAt[key] = at
	e.f.gradeSeq++
	e.f.gradeIDs[key] = e.f.gradeSeq
	e.f.gradeSeen[key] = canon
	e.f.gradeDayCnt[q]++
	e.f.calls["queue_new_attempt"]++
	return e.f.gradeSeq
}
func (e fakeQueueEnv) Now() time.Time { return time.Now() }

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
func (f *fakeBrain) lastGradeQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gradeQuery) == 0 {
		return ""
	}
	return f.gradeQuery[len(f.gradeQuery)-1]
}
func (f *fakeBrain) gradeCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.gradeReqs) }

// jerrf answers like the real brain: {"code","message"} (+ details through jerrd).
func jerrf(w http.ResponseWriter, code int, c string) {
	jerrd(w, code, c, strings.ReplaceAll(c, "_", " "), nil)
}

func jerrd(w http.ResponseWriter, code int, c, msg string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	m := map[string]any{"code": c, "message": msg}
	if details != nil {
		m["details"] = details
	}
	_ = json.NewEncoder(w).Encode(m)
}

// switchReply answers the generic "route not deployed" modes. true = handled.
func switchReply(w http.ResponseWriter, r *http.Request, mode string) bool {
	switch mode {
	case "off":
		http.NotFound(w, r)
	case "off-json": // the brain's catch-all 404: not_found / "not found", no details
		jerrd(w, 404, "not_found", "not found", nil)
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
	if r.URL.RawQuery != "" {
		jerrf(w, 400, "validation_failed")
		return
	}
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
	// questions of another source (assignable from anywhere; 大脑 PR #92)
	have["math:q01"] = map[string]any{"qref": "math:q01", "source": testSource, "number": "1(1)", "qtype": "short_answer", "stem": "assigned stem",
		"media": []any{map[string]any{"sha256": sha1x, "mime": "image/png", "ordinal": 1, "slot": "stem"}}, "figure_status": "ok"}
	have["example.set:math:11"] = map[string]any{"qref": "example.set:math:11", "source": practiceSrc, "number": "11", "qtype": "short_answer",
		"stem": strings.Repeat("x ________ ", 11), "media": []any{}, "figure_status": "none"} // 11 blanks: over the contract's 10
	items := []map[string]any{}
	missing := []string{}
	returned := map[string]bool{}
	for _, ref := range req.QRefs {
		q, ok := have[fakeCanon(ref)]
		if !ok {
			missing = append(missing, ref)
			continue
		}
		if returned[fakeCanon(ref)] { // two strings of one question: only the first is returned
			continue
		}
		returned[fakeCanon(ref)] = true
		c := map[string]any{}
		for k, v := range q {
			c[k] = v // the fake keeps answer/explanation markers on purpose: the site must strip them
		}
		c["rev"] = f.curRev(fakeCanon(ref))
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
	// like the real brain: pick-practice needs the target brain's write scope (the grade token);
	// the bank:read token is refused with 403
	switch r.Header.Get("Authorization") {
	case "Bearer " + f.gradeToken:
	case "Bearer " + f.token:
		f.pickBank++
		jerrf(w, 403, "forbidden")
		return
	default:
		jerrf(w, 401, "unauthorized")
		return
	}
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	for k := range q {
		if k != "source" && k != "n" && k != "date" && k != "brain" && k != "mode" && k != "assigned" {
			jerrf(w, 400, "validation_failed")
			return
		}
	}
	if a := q.Get("assigned"); a != "" && a != "1" && a != "true" {
		jerrf(w, 400, "validation_failed")
		return
	}
	if q.Get("source") == "no-such-source" {
		jerrd(w, 404, "not_found", "source not found", nil)
		return
	}
	if q.Get("source") == "ambiguous-source" {
		jerrf(w, 400, "ambiguous_source")
		return
	}
	if q.Get("brain") != f.wantBrain() || n < 1 || n > 10 || (q.Get("mode") != "due_first" && q.Get("mode") != "wrong_only") || q.Get("source") != practiceSrc {
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
	if q.Get("assigned") == "1" && len(f.assigned) > 0 { // 大脑 PR #92: open assignments first
		var pre []pk
		for _, a := range f.assigned {
			pre = append(pre, pk{a, "", "assigned", []string{"assigned"}})
		}
		all = append(pre, all...)
	}
	if len(all) > n {
		all = all[:n]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": all})
}

// fakeAliases: an old qref that the brain resolves to its canonical one.
var fakeAliases = map[string]string{"example.set:math:2-old": "example.set:math:2"}

func fakeCanon(q string) string {
	if c, ok := fakeAliases[q]; ok {
		return c
	}
	return q
}

var gradeCIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)

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
	f.gradeQuery = append(f.gradeQuery, r.URL.RawQuery)
	switch f.gradeMode {
	case "off":
		http.NotFound(w, r)
		return
	case "off-json": // the brain's catch-all 404: not_found / "not found", no details
		jerrd(w, 404, "not_found", "not found", nil)
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
		w.Header().Set("Retry-After", "3600")
		jerrd(w, 429, "rate_limited", "daily grade limit reached", map[string]any{"qrefs": []string{"example.set:math:1"}})
		return
	case "rl-other": // a 429 that is NOT the daily cap: no details, another message
		w.Header().Set("Retry-After", "30")
		jerrd(w, 429, "rate_limited", "slow down", nil)
		return
	case "rev":
		jerrd(w, 409, "rev_mismatch", "rev mismatch", map[string]any{"qref": "example.set:math:2", "rev": 1, "current_rev": 2, "index": 0})
		return
	case "idem":
		jerrf(w, 409, "idempotency_conflict")
		return
	case "unknown":
		jerrd(w, 404, "not_found", "question not found", map[string]any{"qrefs": []string{"example.set:math:1"}})
		return
	case "unknown-nodetails": // old code-based hint, but no details.qrefs: NOT the grade endpoint's "question not found"
		jerrd(w, 404, "unknown_qref", "question not found", nil)
		return
	}
	// the real brain is strict: unknown fields, a query string, bad values are 400
	var req struct {
		Brain string `json:"brain"`
		CID   string `json:"client_attempt_id"`
		Items []struct {
			QRef        string          `json:"qref"`
			Answer      any             `json:"answer"`
			Rev         *int            `json:"rev"`
			AttemptKind string          `json:"attempt_kind"`
			AnsweredAt  string          `json:"answered_at"`
			SessionRef  string          `json:"session_ref"`
			Payload     json.RawMessage `json:"payload"`
		} `json:"items"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		jerrf(w, 400, "invalid_json")
		return
	}
	if r.URL.RawQuery != "" || req.Brain != f.wantBrain() || !gradeCIDRe.MatchString(req.CID) || len(req.Items) == 0 || len(req.Items) > 50 {
		jerrf(w, 400, "validation_failed")
		return
	}
	seenQ := map[string]bool{}
	for _, it := range req.Items {
		n := 0
		switch a := it.Answer.(type) {
		case string:
			n = len([]rune(a))
		case []any:
			if len(a) > 10 {
				jerrf(w, 400, "validation_failed")
				return
			}
			for _, x := range a {
				xs, ok := x.(string)
				if !ok || len([]rune(xs)) > 2000 {
					jerrf(w, 400, "validation_failed")
					return
				}
			}
		default:
			jerrf(w, 400, "validation_failed")
			return
		}
		bad := it.QRef == "" || it.QRef != strings.ToLower(it.QRef) || seenQ[fakeCanon(it.QRef)] || n > 2000 ||
			(it.Rev != nil && *it.Rev < 1) || (it.AttemptKind != "" && it.AttemptKind != "correction" && it.AttemptKind != "variant") ||
			(it.SessionRef != "" && len([]rune(it.SessionRef)) > 200) || len(it.Payload) > 4096 ||
			3+len(req.CID)+1+len(it.QRef) > 198
		if it.AnsweredAt != "" {
			t, err := time.Parse(time.RFC3339, it.AnsweredAt)
			bad = bad || err != nil || t.After(time.Now().Add(time.Minute)) || t.Before(time.Now().Add(-7*24*time.Hour))
		}
		if bad {
			jerrf(w, 400, "validation_failed")
			return
		}
		seenQ[fakeCanon(it.QRef)] = true
	}
	for _, it := range req.Items {
		if it.Rev != nil && *it.Rev != f.curRev(fakeCanon(it.QRef)) {
			jerrf(w, 409, "rev_mismatch")
			return
		}
	}
	if strings.Contains(string(body), `"qref":"example.set:math:99"`) { // an unknown question, with the real 404 shape
		jerrd(w, 404, "not_found", "question not found", map[string]any{"qrefs": []string{"example.set:math:99"}})
		return
	}
	if f.gradeMode == "cap3" { // the real rule: >3 new writes of one question per day refuses the whole batch
		var capped []string
		for _, it := range req.Items {
			ref := fakeCanon(it.QRef)
			if _, dup := f.gradeIDs["ps:"+req.CID+":"+ref]; !dup && ref != "example.set:math:4" && f.gradeDayCnt[ref] >= 3 {
				capped = append(capped, it.QRef)
			}
		}
		if len(capped) > 0 {
			w.Header().Set("Retry-After", "3600")
			jerrd(w, 429, "rate_limited", "daily grade limit reached", map[string]any{"qrefs": capped})
			return
		}
		for _, it := range req.Items {
			ref := fakeCanon(it.QRef)
			if _, dup := f.gradeIDs["ps:"+req.CID+":"+ref]; !dup && ref != "example.set:math:4" {
				f.gradeDayCnt[ref]++
			}
		}
	}
	var items []map[string]any
	newWrites := false
	for _, it := range req.Items {
		key := "ps:" + req.CID + ":" + fakeCanon(it.QRef)
		a := canonJSON(it.Answer)
		if prev, ok := f.gradeSeen[key]; ok && prev != a {
			jerrf(w, 409, "idempotency_conflict")
			return
		}
	}
	for _, it := range req.Items {
		key := "ps:" + req.CID + ":" + fakeCanon(it.QRef)
		a := canonJSON(it.Answer)
		ref := fakeCanon(it.QRef)
		o := map[string]any{"qref": it.QRef, "status": "pending_key", "recorded": f.gradeMode != "norecord", "duplicate": false}
		switch {
		case strings.Contains(a, "~review~"), strings.Contains(a, "厘米"): // a unit tail the brain cannot read -> needs_review, never wrong
			o["status"] = "needs_review"
		case ref == "example.set:math:1":
			o["status"], o["correct"] = "graded", a == `["1","2","3"]`
		case ref == "example.set:math:2":
			o["status"], o["correct"] = "graded", a == `"B"`
		case ref == "example.set:math:4":
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
				if it.AnsweredAt != "" {
					if t, err := time.Parse(time.RFC3339, it.AnsweredAt); err == nil {
						f.gradeAt[key] = t
					}
				}
				if f.gradeMode != "cap3" {
					f.gradeDayCnt[ref]++ // the real brain counts every new write; cap3 does its own counting above
				}
				f.calls["grade_new_item"]++
				newWrites = true
			}
		}
		if f.queueHints {
			st, _ := o["status"].(string)
			qb, qr := f.q.Queueable(ref, st)
			o["queueable"] = qb
			if qr == "" {
				o["queueable_reason"] = nil
			} else {
				o["queueable_reason"] = qr
			}
		}
		items = append(items, o)
	}
	items = append(items, map[string]any{"qref": "example.set:math:5", "status": "graded", "correct": true, "recorded": true, "attempt_id": 99, "duplicate": false}) // never asked
	w.Header().Set("Content-Type", "application/json")
	if newWrites { // 201 when something new was written, 200 for replays / nothing written
		w.WriteHeader(201)
	}
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
	app     *server.Server
	qDir    string
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
	ntfyRaw     *string       // when set: Deps.NtfyBase verbatim (nil = use ntfyURL rules)
	maxPerHour  int           // 0 = default 5
	auto        []string      // RUYI_AUTO_SOURCES
	autoTTL     time.Duration // 0 = default
	retry       time.Duration // ntfy retry delay (0 = 150ms)
	ntfyTimeout time.Duration // per-attempt ntfy timeout (0 = production value)
	ntfyWait    time.Duration // how long a submission waits for the first ntfy attempt (0 = production value)
	now         func() time.Time
	reuse       *env // reuse brain + attempts dir of another env (restart simulation)

	lookup       string // fake brain lookup mode ("" ok | "off" | "501" | "fail")
	skipDeferred bool   // RUYI_PICK_SKIP_DEFERRED

	gradeAsRead bool   // configure the site's grade token as the fake's bank:read token (a token without the write scope)
	brainID     string // the site's configured brain id (RUYI_BRAIN_ID); "" = unset
	fakeBrainID string // the only brain id the fake brain serves; "" = ruyi

	// pending-answer queue (S1)
	queue            bool          // RUYI_QUEUE_ENABLED (needs grade)
	queueNotify      bool          // poller configured (EnableNotify; no background goroutine unless queueLoop)
	queueLoop        bool          // run StartQueue (sweep + poller goroutines)
	queueDir         string        // "" = a fresh temp dir
	queuePoll        time.Duration // 0 = 45s
	queueSweep       time.Duration
	queueQuiet       time.Duration
	queueHook        func(stage string) bool
	queueNoHint      bool   // the fake's grade carries no queueable hints
	queueNotifyToken string // overrides the notify token the site is configured with
	images           bool   // answer image upload on (ImagesEnabled + Media)
}

func newEnv(t *testing.T, o opts) *env {
	t.Helper()
	e := &env{brain: newFakeBrain(t), logs: &syncBuf{}, code: rnd(t, "code-"), secret: rnd(t, "sec-"), topic: rnd(t, "topic-")}
	if o.reuse != nil {
		e.brain, e.code, e.secret, e.topic = o.reuse.brain, o.reuse.code, o.reuse.secret, o.reuse.topic
		o.attemptsDir = o.reuse.attDir
		if o.queueDir == "" {
			o.queueDir = o.reuse.qDir
		}
	}
	if o.noTopic {
		e.topic = ""
	}
	e.brain.setLookupMode(o.lookup)
	if o.reuse == nil {
		e.brain.brainID = o.fakeBrainID
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
	gt := ""
	if o.grade {
		gt = e.brain.gradeToken
	}
	if o.gradeAsRead {
		gt = e.brain.token
	}
	nt := ""
	if o.queue && o.queueNotify {
		nt = e.brain.notifyToken
		if o.queueNotifyToken != "" {
			nt = o.queueNotifyToken
		}
	}
	e.brain.queueHints = o.queue && !o.queueNoHint
	bc, err := brain.New(brain.Config{BaseURL: u, Token: e.brain.token, GradeToken: gt, BrainID: o.brainID, NotifyToken: nt})
	must(t, err)
	e.attDir = o.attemptsDir
	if e.attDir == "" {
		e.attDir = filepath.Join(t.TempDir(), "attempts")
	}
	e.qDir = o.queueDir
	if o.queue && e.qDir == "" {
		e.qDir = filepath.Join(t.TempDir(), "queue")
	}
	var att *attempts.Store
	if !o.noAttempts {
		att = attempts.Open(e.attDir, slog.New(slog.NewTextHandler(e.logs, nil)))
	}
	ntfy := o.ntfyURL
	if ntfy == "" {
		ntfy = "http://127.0.0.1:1/"
	}
	if o.ntfyRaw != nil {
		ntfy = *o.ntfyRaw
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
		Attempts: att, NtfyBase: ntfy, NtfyRetryDelay: retry, NtfyAttemptTimeout: o.ntfyTimeout, NtfyRequestWait: o.ntfyWait, GradeMaxPerHour: o.maxPerHour, AutoSources: o.auto, AutoTTL: o.autoTTL, SkipDeferred: o.skipDeferred, Now: o.now,
		Queue: bc, QueueEnabled: o.queue, QueueNotify: o.queueNotify, QueueDir: e.qDir, QueuePoll: o.queuePoll, QueueSweep: o.queueSweep, QueueQuiet: o.queueQuiet, QueueHook: o.queueHook, QueueViewTTL: time.Nanosecond,
		QueueJitter: func() float64 { return 0 }, ImagesEnabled: o.images, Media: bc})
	e.app = app
	if o.queue {
		t.Cleanup(app.CloseQueue)
		if o.queueLoop {
			app.StartQueue()
		} else if o.queueNotify {
			must(t, app.EnableNotify())
		}
	}
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
