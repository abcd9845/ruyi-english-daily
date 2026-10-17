package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const gradeSets = `{"sets":[
 {"id":"demo-1","title":"演示套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"other-2","title":"另一套","source":"` + practiceSrc + `","qrefs":["example.set:math:4"]}
]}`

const (
	cid1 = "11111111-1111-4111-8111-111111111111"
	cid2 = "22222222-2222-4222-8222-222222222222"
)

func uuid(n int) string { return fmt.Sprintf("%08d-0000-4000-8000-%012d", n, n) }

// ntfy stub: records every POST.
type ntfyStub struct {
	*httptest.Server
	mu    sync.Mutex
	posts []ntfyPost
	fail  bool
	hits  int           // every request, failing or not
	hang  chan struct{} // when non-nil: requests block until it is closed
}
type ntfyPost struct {
	Path, Body string
	Header     http.Header
}

func newNtfyStub(t *testing.T) *ntfyStub {
	n := &ntfyStub{}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		n.mu.Lock()
		n.hits++
		hang := n.hang
		n.mu.Unlock()
		if hang != nil {
			select {
			case <-hang:
			case <-r.Context().Done():
				return
			}
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.fail {
			http.Error(w, "down", 503)
			return
		}
		n.posts = append(n.posts, ntfyPost{r.URL.Path, b.String(), r.Header.Clone()})
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(n.Close)
	return n
}
func (n *ntfyStub) count() int { n.mu.Lock(); defer n.mu.Unlock(); return len(n.posts) }
func (n *ntfyStub) last() ntfyPost {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.posts) == 0 {
		return ntfyPost{}
	}
	return n.posts[len(n.posts)-1]
}
func (n *ntfyStub) hitCount() int  { n.mu.Lock(); defer n.mu.Unlock(); return n.hits }
func (n *ntfyStub) setFail(v bool) { n.mu.Lock(); n.fail = v; n.mu.Unlock() }
func (n *ntfyStub) url() string    { return n.Server.URL + "/" }

func answersJSON(cid string, a map[string]any) string {
	type an struct {
		QRef   string `json:"qref"`
		Answer any    `json:"answer"`
	}
	var list []an
	for _, q := range []string{"example.set:math:1", "example.set:math:2", "example.set:math:3", "example.set:math:4", "example.set:math:5"} {
		if v, ok := a[q]; ok {
			list = append(list, an{q, v})
		}
	}
	b, _ := json.Marshal(map[string]any{"client_attempt_id": cid, "answers": list})
	return string(b)
}

type gresp struct {
	ClientAttemptID string `json:"client_attempt_id"`
	SetID           string `json:"set_id"`
	Mode            string `json:"mode"`
	BrainRecorded   bool   `json:"brain_recorded"`
	RecordedCount   int    `json:"recorded_count"`
	BrainNote       string `json:"brain_note"`
	Results         []struct {
		QRef      string `json:"qref"`
		Status    string `json:"status"`
		Correct   *bool  `json:"correct"`
		Recorded  bool   `json:"recorded"`
		AttemptID int64  `json:"attempt_id"`
		Duplicate bool   `json:"duplicate"`
	} `json:"results"`
	Summary  struct{ Total, Graded, Correct, Pending, Unsupported, Unanswered, Recorded int } `json:"summary"`
	Archived bool                                                                             `json:"archived"`
	Notify   string                                                                           `json:"notify"`
	Replayed bool                                                                             `json:"replayed"`
}

func (e *env) submit(t *testing.T, ck, set, body string) (*http.Response, []byte, gresp) {
	t.Helper()
	resp, b := e.do(t, "POST", "/api/practice/"+set+"/grade", body, map[string]string{"Content-Type": "application/json"}, ck)
	var g gresp
	if resp.StatusCode == 200 {
		must(t, json.Unmarshal(b, &g))
	}
	return resp, b, g
}

var goodAnswers = map[string]any{
	"example.set:math:1": []string{"1", "2", "3"}, // brain says correct
	"example.set:math:2": "A",                     // brain says wrong
	"example.set:math:3": "free text",             // pending_key
}

func gradeEnv(t *testing.T, o opts) (*env, *ntfyStub, string) {
	n := newNtfyStub(t)
	o.sets, o.grade, o.ntfyURL = gradeSets, true, n.url()
	e := newEnv(t, o)
	return e, n, e.session(t)
}

func TestGradeGradedMode(t *testing.T) {
	e, ntfy, ck := gradeEnv(t, opts{})
	resp, body, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if g.Mode != "graded" || !g.BrainRecorded || g.Replayed || g.ClientAttemptID != cid1 || g.SetID != "demo-1" {
		t.Errorf("header: %+v", g)
	}
	want := []struct {
		st string
		c  string
	}{{"graded", "true"}, {"graded", "false"}, {"pending_key", "null"}}
	for i, r := range g.Results {
		c := "null"
		if r.Correct != nil {
			c = fmt.Sprint(*r.Correct)
		}
		if r.Status != want[i].st || c != want[i].c {
			t.Errorf("result %d: %+v", i, r)
		}
	}
	if g.Summary.Total != 3 || g.Summary.Graded != 2 || g.Summary.Correct != 1 || g.Summary.Pending != 1 || g.Summary.Unanswered != 0 {
		t.Errorf("summary %+v", g.Summary)
	}
	if !g.Archived || g.Notify != "sent" || ntfy.count() != 1 {
		t.Errorf("archived=%v notify=%q posts=%d", g.Archived, g.Notify, ntfy.count())
	}
	// the brain was asked with the contract, with the grade token only
	if e.brain.count("grade_new_item") != 3 || e.brain.count("grade") != 1 || e.brain.gradeBank != 0 {
		t.Errorf("brain calls: %v bankTokenUsed=%d", e.brain.calls, e.brain.gradeBank)
	}
	var sent struct {
		Brain string `json:"brain"`
		CID   string `json:"client_attempt_id"`
		Items []struct {
			QRef       string `json:"qref"`
			Answer     any    `json:"answer"`
			Rev        int    `json:"rev"`
			SessionRef string `json:"session_ref"`
		} `json:"items"`
	}
	must(t, json.Unmarshal(e.brain.gradeReqs[0], &sent))
	if sent.Brain != "ruyi" || sent.CID != cid1 || len(sent.Items) != 3 || sent.Items[0].SessionRef != "demo-1" || sent.Items[0].Rev != 0 {
		t.Errorf("upstream request: %s", e.brain.gradeReqs[0])
	}
	// per-item brain confirmation, integer attempt ids, recorded everywhere
	if !g.BrainRecorded || g.RecordedCount != 3 || g.Summary.Recorded != 3 || g.BrainNote != "" {
		t.Errorf("recorded: %+v", g)
	}
	for i, r := range g.Results {
		if !r.Recorded || r.AttemptID != int64(i+1) || r.Duplicate {
			t.Errorf("result %d: %+v", i, r)
		}
	}
	// no answers or explanations, no brain internals, no foreign qref (the fake brain also "graded" math:5)
	for _, bad := range []string{"SECRET", `"answer"`, `"explanation"`, `"solution"`, "brain-", "example.set:math:5", e.brain.token, e.brain.gradeToken} {
		if strings.Contains(string(body), bad) {
			t.Errorf("response contains %q: %s", bad, body)
		}
	}
	allowed := map[string]bool{"client_attempt_id": true, "set_id": true, "mode": true, "brain_recorded": true, "recorded_count": true, "results": true, "summary": true, "archived": true, "notify": true, "replayed": true}
	var generic map[string]any
	must(t, json.Unmarshal(body, &generic))
	for k := range generic {
		if !allowed[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
}

func TestGradeIdempotentByClientAttemptID(t *testing.T) {
	e, ntfy, ck := gradeEnv(t, opts{maxPerHour: 2})
	_, b1, g1 := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	for i := 0; i < 4; i++ { // more repeats than the hourly limit: replays are free
		resp, b2, g2 := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
		if resp.StatusCode != 200 || !g2.Replayed || g2.Mode != g1.Mode || g2.Summary != g1.Summary || len(g2.Results) != 3 {
			t.Fatalf("replay %d: %d %s (first %s)", i, resp.StatusCode, b2, b1)
		}
	}
	// the same id with different answers is NOT silently replayed: 409 idempotency_conflict, retry_new_id
	resp3, b3, _ := e.submit(t, ck, "demo-1", answersJSON(cid1, map[string]any{"example.set:math:2": "B"}))
	if resp3.StatusCode != 409 || !strings.Contains(string(b3), `"error":"idempotency_conflict"`) || !strings.Contains(string(b3), `"retry_new_id":true`) {
		t.Errorf("replay with other payload: %d %s", resp3.StatusCode, b3)
	}
	if e.brain.count("grade") != 1 || ntfy.count() != 1 {
		t.Errorf("brain grade calls=%d ntfy=%d (want 1, 1)", e.brain.count("grade"), ntfy.count())
	}
	if n := countLines(t, e.attDir); n != 1 {
		t.Errorf("attempt archived %d times", n)
	}
	// same id for another set is refused
	resp, _, _ := e.submit(t, ck, "other-2", answersJSON(cid1, map[string]any{"example.set:math:4": []string{"x"}}))
	if resp.StatusCode != 409 {
		t.Errorf("cross-set reuse: %d", resp.StatusCode)
	}
	// uppercase spelling of the same uuid is the same attempt
	_, _, g4 := e.submit(t, ck, "demo-1", answersJSON(strings.ToUpper(cid1), goodAnswers))
	if !g4.Replayed {
		t.Error("case-insensitive uuid")
	}
}

func TestGradeParallelDuplicatesGradeOnce(t *testing.T) {
	e, ntfy, ck := gradeEnv(t, opts{})
	var wg sync.WaitGroup
	replays := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _, g := e.submit(t, ck, "demo-1", answersJSON(cid2, goodAnswers))
			if resp.StatusCode != 200 {
				t.Errorf("status %d", resp.StatusCode)
			}
			replays <- g.Replayed
		}()
	}
	wg.Wait()
	close(replays)
	fresh := 0
	for r := range replays {
		if !r {
			fresh++
		}
	}
	if fresh != 1 || e.brain.count("grade") != 1 || ntfy.count() != 1 || countLines(t, e.attDir) != 1 {
		t.Errorf("fresh=%d brain=%d ntfy=%d lines=%d", fresh, e.brain.count("grade"), ntfy.count(), countLines(t, e.attDir))
	}
}

func TestGradeValidation(t *testing.T) {
	e, _, ck := gradeEnv(t, opts{maxPerHour: 1000})
	post := func(set, body string) int {
		resp, _, _ := e.submit(t, ck, set, body)
		return resp.StatusCode
	}
	ans := func(n int, a map[string]any) string { return answersJSON(uuid(n), a) }
	cases := []struct {
		name string
		set  string
		body string
		want int
	}{
		{"bad uuid", "demo-1", answersJSON("not-a-uuid", goodAnswers), 400},
		{"empty uuid", "demo-1", answersJSON("", goodAnswers), 400},
		{"qref outside the set (alternate question 4)", "demo-1", ans(1, map[string]any{"example.set:math:1": []string{"1", "2", "3"}, "example.set:math:4": "x"}), 400},
		{"qref outside the set (question 5)", "demo-1", ans(2, map[string]any{"example.set:math:5": "B"}), 400},
		{"invented qref", "demo-1", `{"client_attempt_id":"` + uuid(3) + `","answers":[{"qref":"nope:1","answer":"A"}]}`, 400},
		{"duplicate qref", "demo-1", `{"client_attempt_id":"` + uuid(4) + `","answers":[{"qref":"example.set:math:2","answer":"A"},{"qref":"example.set:math:2","answer":"B"}]}`, 400},
		{"fill: string instead of array", "demo-1", ans(5, map[string]any{"example.set:math:1": "1"}), 400},
		{"fill: wrong blank count", "demo-1", ans(6, map[string]any{"example.set:math:1": []string{"1", "2"}}), 400},
		{"fill: blank too long", "demo-1", ans(7, map[string]any{"example.set:math:1": []string{strings.Repeat("x", 201), "", ""}}), 400},
		{"fill: control character", "demo-1", ans(8, map[string]any{"example.set:math:1": []string{"a\u0000b", "", ""}}), 400},
		{"choice: array instead of string", "demo-1", ans(9, map[string]any{"example.set:math:2": []string{"A"}}), 400},
		{"choice: unknown key", "demo-1", ans(10, map[string]any{"example.set:math:2": "Z"}), 400},
		{"free: array instead of string", "demo-1", ans(11, map[string]any{"example.set:math:3": []string{"x"}}), 400},
		{"free: too long", "demo-1", ans(12, map[string]any{"example.set:math:3": strings.Repeat("x", 2001)}), 400},
		{"number as answer", "demo-1", `{"client_attempt_id":"` + uuid(13) + `","answers":[{"qref":"example.set:math:2","answer":1}]}`, 400},
		{"nothing answered", "demo-1", ans(14, map[string]any{"example.set:math:2": "", "example.set:math:1": []string{"", "", ""}}), 400},
		{"empty answers list", "demo-1", `{"client_attempt_id":"` + uuid(15) + `","answers":[]}`, 400},
		{"unknown field", "demo-1", `{"client_attempt_id":"` + uuid(16) + `","answers":[{"qref":"example.set:math:2","answer":"A"}],"correct":true}`, 400},
		{"unknown answer field", "demo-1", `{"client_attempt_id":"` + uuid(17) + `","answers":[{"qref":"example.set:math:2","answer":"A","is_correct":true}]}`, 400},
		{"trailing data", "demo-1", ans(18, goodAnswers) + `{}`, 400},
		{"not json", "demo-1", `nope`, 400},
		{"unknown set", "no-such", ans(19, goodAnswers), 404},
		{"bad set id", "BAD", ans(20, goodAnswers), 404},
		{"too many answers", "other-2", ans(21, goodAnswers), 400},
	}
	for _, c := range cases {
		if got := post(c.set, c.body); got != c.want {
			t.Errorf("%s: want %d got %d", c.name, c.want, got)
		}
	}
	// a body over the limit
	big := `{"client_attempt_id":"` + uuid(30) + `","answers":[{"qref":"example.set:math:3","answer":"` + strings.Repeat("x", 70<<10) + `"}]}`
	if got := post("demo-1", big); got != 400 {
		t.Errorf("oversized body: %d", got)
	}
	// multiple valid shapes still pass: partial answers, unanswered ones are reported as such
	resp, body, g := e.submit(t, ck, "demo-1", ans(40, map[string]any{"example.set:math:2": "B", "example.set:math:1": []string{"", "", ""}}))
	if resp.StatusCode != 200 || g.Summary.Unanswered != 2 || g.Summary.Graded != 1 || g.Summary.Correct != 1 {
		t.Errorf("partial: %d %s", resp.StatusCode, body)
	}
	for _, r := range g.Results {
		if r.QRef != "example.set:math:2" && r.Status != "unanswered" {
			t.Errorf("unanswered status: %+v", r)
		}
	}
	// nothing reached the brain for any rejected request
	if e.brain.count("grade") != 1 {
		t.Errorf("brain grade calls = %d, want 1 (only the valid one)", e.brain.count("grade"))
	}
	// auth / method / content type
	resp, _ = e.do(t, "POST", "/api/practice/demo-1/grade", ans(50, goodAnswers), map[string]string{"Content-Type": "application/json"}, "")
	if resp.StatusCode != 401 {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/practice/demo-1/grade", ans(51, goodAnswers), map[string]string{"Content-Type": "text/plain"}, ck)
	if resp.StatusCode != 415 {
		t.Errorf("content-type: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/api/practice/demo-1/grade", "", nil, ck)
	if resp.StatusCode != 405 {
		t.Errorf("GET grade: %d", resp.StatusCode)
	}
}

func TestGradeDegradesWhenNotConfigured(t *testing.T) {
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: gradeSets, ntfyURL: n.url()}) // no grade token
	ck := e.session(t)
	resp, body, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if g.Mode != "archive_only" || g.BrainRecorded || g.Summary.Pending != 3 || g.Summary.Graded != 0 || g.Summary.Correct != 0 {
		t.Errorf("%+v", g)
	}
	for _, r := range g.Results {
		if r.Status != "pending_key" || r.Correct != nil {
			t.Errorf("degraded result must be pending_key/null: %+v", r)
		}
	}
	if !g.Archived || g.Notify != "sent" {
		t.Errorf("still archived and notified: %+v", g)
	}
	if e.brain.count("grade") != 0 {
		t.Error("the grade endpoint must not be called without a grade token")
	}
	msg := n.last().Body
	if !strings.Contains(msg, "未判分") || strings.Contains(msg, "，对 ") || !strings.Contains(msg, "待核对 3") {
		t.Errorf("summary for archive-only: %q", msg)
	}
	if strings.Contains(string(body)+msg, "大脑") {
		t.Errorf("archive-only must never claim the brain recorded it: %q", msg)
	}
}

func TestGradeDegradesWhenBrainFails(t *testing.T) {
	// the brain replies but records nothing: verdicts are never shown, nothing claimed
	{
		e, n, ck := gradeEnv(t, opts{})
		e.brain.setGradeMode("norecord")
		resp, body, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
		if resp.StatusCode != 200 || g.Mode != "graded" || g.BrainRecorded || g.RecordedCount != 0 || g.Summary.Pending != 3 || g.Summary.Graded != 0 || !g.Archived || n.count() != 1 {
			t.Fatalf("norecord: %d %s", resp.StatusCode, body)
		}
		if !strings.Contains(n.last().Body, "未记入大脑") {
			t.Errorf("ntfy must say it was not recorded: %q", n.last().Body)
		}
	}
	for mode, note := range map[string]string{"fail": "unavailable", "off": "not_ready", "off-json": "not_ready", "501": "not_ready"} {
		t.Run(mode, func(t *testing.T) {
			e, n, ck := gradeEnv(t, opts{})
			e.brain.setGradeMode(mode)
			resp, body, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
			if resp.StatusCode != 200 || g.Mode != "archive_only" || g.BrainRecorded || g.BrainNote != note || g.Summary.Pending != 3 || !g.Archived || n.count() != 1 {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			for _, r := range g.Results {
				if r.Recorded || r.Correct != nil || r.AttemptID != 0 {
					t.Errorf("nothing may be claimed as recorded: %+v", r)
				}
			}
			if strings.Contains(e.logs.String(), e.brain.gradeToken) {
				t.Error("grade token in logs")
			}
			if !strings.Contains(e.logs.String(), "grading unavailable") {
				t.Errorf("expected a warn log:\n%s", e.logs.String())
			}
		})
	}
	// unreachable brain: practice itself fails, grading too (502), nothing is stored
	e := newEnv(t, opts{sets: gradeSets, grade: true, brainURL: "http://127.0.0.1:1"})
	ck := e.session(t)
	resp, _, _ := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 502 {
		t.Errorf("brain down at validation: %d", resp.StatusCode)
	}
	if countLines(t, e.attDir) != 0 {
		t.Error("nothing may be archived for a request that failed validation")
	}
}

func TestGradeRateLimit(t *testing.T) {
	e, _, ck := gradeEnv(t, opts{maxPerHour: 2})
	for i := 1; i <= 2; i++ {
		if resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(uuid(i), goodAnswers)); resp.StatusCode != 200 {
			t.Fatalf("submission %d: %d %s", i, resp.StatusCode, b)
		}
	}
	resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(uuid(3), goodAnswers))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third: %d %s", resp.StatusCode, b)
	}
	// replays stay free; invalid requests do not count; other sets are separate
	if resp, _, g := e.submit(t, ck, "demo-1", answersJSON(uuid(1), goodAnswers)); resp.StatusCode != 200 || !g.Replayed {
		t.Errorf("replay while limited: %d", resp.StatusCode)
	}
	if resp, _, _ := e.submit(t, ck, "other-2", answersJSON(uuid(4), map[string]any{"example.set:math:4": []string{"x"}})); resp.StatusCode != 200 {
		t.Errorf("another set: %d", resp.StatusCode)
	}
	// a fresh login (new session cookie) from the same address does not reset the limit
	ck2 := e.session(t)
	if resp, _, _ := e.submit(t, ck2, "demo-1", answersJSON(uuid(5), goodAnswers)); resp.StatusCode != 429 {
		t.Errorf("new session, same address: %d", resp.StatusCode)
	}
	if e.brain.count("grade") != 3 {
		t.Errorf("brain was asked %d times", e.brain.count("grade"))
	}
	// the refused attempt left no stale lock: the same id works once the window passes (checked via a replayless id)
	if resp, _, _ := e.submit(t, ck, "demo-1", answersJSON(uuid(3), goodAnswers)); resp.StatusCode != 429 {
		t.Errorf("still limited: %d", resp.StatusCode)
	}
}

func countLines(t *testing.T, dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "attempts.jsonl"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func TestAttemptsFileContentAndPermissions(t *testing.T) {
	e, _, ck := gradeEnv(t, opts{})
	resp, _ := e.do(t, "POST", "/api/practice/demo-1/grade", answersJSON(cid1, goodAnswers),
		map[string]string{"Content-Type": "application/json", "User-Agent": "UnitTest/1.0", "Authorization": "Bearer leak-me-not", "Cookie": "other=leak-cookie"}, ck)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	di, err := os.Stat(e.attDir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", err, di)
	}
	fi, err := os.Stat(filepath.Join(e.attDir, "attempts.jsonl"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file: %v %v", err, fi)
	}
	raw, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	for _, bad := range []string{e.code, e.secret, e.brain.token, e.brain.gradeToken, e.topic, ck, auth.CookieName, "leak-me-not", "leak-cookie", "Bearer", "Authorization", "SECRET-"} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Errorf("attempts file contains %q", bad)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines", len(lines))
	}
	var rec struct {
		V               int    `json:"v"`
		ClientAttemptID string `json:"client_attempt_id"`
		SetID           string `json:"set_id"`
		SetTitle        string `json:"set_title"`
		Source          string `json:"source"`
		SubmittedAt     string `json:"submitted_at"`
		Mode            string `json:"mode"`
		Items           []struct {
			QRef      string `json:"qref"`
			Answer    any    `json:"answer"`
			Status    string `json:"status"`
			Correct   *bool  `json:"correct"`
			Recorded  bool   `json:"recorded"`
			AttemptID int64  `json:"attempt_id"`
		} `json:"items"`
		Summary struct{ Total, Graded, Correct, Pending, Unanswered int }
		Client  struct{ UA, IP string }
	}
	must(t, json.Unmarshal([]byte(lines[0]), &rec))
	if rec.V != 2 || rec.ClientAttemptID != cid1 || rec.SetID != "demo-1" || rec.SetTitle != "演示套" || rec.Source != practiceSrc || rec.Mode != "graded" ||
		len(rec.Items) != 3 || !rec.Items[0].Recorded || rec.Items[0].AttemptID != 1 || rec.Items[2].AttemptID != 3 || rec.Client.UA != "UnitTest/1.0" || rec.Client.IP == "" || rec.Summary.Total != 3 {
		t.Errorf("record: %s", lines[0])
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.SubmittedAt); err != nil {
		t.Errorf("submitted_at %q", rec.SubmittedAt)
	}
	if rec.Items[0].QRef != "example.set:math:1" || fmt.Sprint(rec.Items[0].Answer) != "[1 2 3]" || rec.Items[1].Answer != "A" {
		t.Errorf("items: %+v", rec.Items)
	}
}

func TestAttemptsSurviveRestartForIdempotency(t *testing.T) {
	e, _, ck := gradeEnv(t, opts{})
	e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	n2 := newNtfyStub(t)
	e2 := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n2.url(), reuse: e}) // "restart": same dir, same brain
	ck2 := e2.session(t)
	resp, _, g := e2.submit(t, ck2, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 || !g.Replayed || g.Mode != "graded" || g.Summary.Correct != 1 {
		t.Errorf("after restart: %d %+v", resp.StatusCode, g)
	}
	if e.brain.count("grade") != 1 || n2.count() != 0 || countLines(t, e.attDir) != 1 {
		t.Errorf("replay after restart must not re-grade/re-notify/re-archive: brain=%d ntfy=%d lines=%d", e.brain.count("grade"), n2.count(), countLines(t, e.attDir))
	}
}

func TestAttemptsNotWritableStillGrades(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "file")
	must(t, os.WriteFile(blocker, []byte("x"), 0o600))
	e, n, ck := gradeEnv(t, opts{attemptsDir: filepath.Join(blocker, "attempts")}) // a path below a regular file
	for i := 1; i <= 2; i++ {
		resp, b, g := e.submit(t, ck, "demo-1", answersJSON(uuid(i), goodAnswers))
		if resp.StatusCode != 200 || g.Archived || g.Mode != "graded" || g.Summary.Correct != 1 {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
	}
	if n.count() != 2 {
		t.Errorf("notifications still sent: %d", n.count())
	}
	if c := strings.Count(e.logs.String(), "attempts archive is not writable"); c != 1 {
		t.Errorf("error should be logged once, got %d:\n%s", c, e.logs.String())
	}
	for _, bad := range []string{"example.set", "free text", blocker, e.code, e.brain.token} {
		if strings.Contains(e.logs.String(), bad) {
			t.Errorf("log contains %q", bad)
		}
	}
	// no store at all behaves the same
	e2 := newEnv(t, opts{sets: gradeSets, grade: true, noAttempts: true})
	if _, _, g := e2.submit(t, e2.session(t), "demo-1", answersJSON(cid1, goodAnswers)); g.Archived || g.Mode != "graded" {
		t.Errorf("no store: %+v", g)
	}
}

// the production value (ntfyMaxAttempts) - first send plus two retries.
const ntfyMaxAttemptsForTest = 3

func TestNotifySummaryAndBody(t *testing.T) {
	e, n, ck := gradeEnv(t, opts{})
	e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	p := n.last()
	if p.Path != "/"+e.topic {
		t.Errorf("posted to %q", p.Path)
	}
	// graded mode, 3 answered: right 1, wrong 1, pending 1; one short paragraph, no JSON
	head, tail, found := strings.Cut(p.Body, "完成于 ")
	if !found || head != "如意练习：演示套 3 题，对 1，错 1，待核对 1，已提交。attempt 11111111。" {
		t.Errorf("summary line: %q", p.Body)
	}
	if !regexp.MustCompile(`^\d\d:\d\d:\d\d（北京时间）。$`).MatchString(tail) {
		t.Errorf("time part: %q", tail)
	}
	if strings.ContainsAny(p.Body, "{}\n") || strings.Contains(p.Body, "qref") || strings.Contains(p.Body, "example.set") || strings.Contains(p.Body, cid1) {
		t.Errorf("body must be a short paragraph without JSON/answers/qrefs/full ids: %q", p.Body)
	}
	if len(p.Body) > 300 {
		t.Errorf("body too long: %d", len(p.Body))
	}
	for _, bad := range []string{e.code, e.secret, e.brain.token, e.brain.gradeToken, ck, auth.CookieName, "Bearer", "SECRET-", "等家长"} {
		if strings.Contains(p.Body, bad) {
			t.Errorf("ntfy body contains %q", bad)
		}
	}
	if strings.Contains(p.Body, "录") {
		t.Errorf("the message is a heads-up, not a request to approve: %q", p.Body)
	}
	if p.Header.Get("Authorization") != "" || p.Header.Get("Cookie") != "" {
		t.Error("credentials sent to ntfy")
	}
}

func TestNotifyFailureDoesNotAffectResultAndRetries(t *testing.T) {
	e, n, ck := gradeEnv(t, opts{retry: 200 * time.Millisecond})
	n.setFail(true)
	resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 || g.Notify != "retrying" || g.Mode != "graded" || g.Summary.Correct != 1 || !g.Archived {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	n.setFail(false) // recovers before the retry fires
	deadline := time.Now().Add(3 * time.Second)
	for n.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	if n.count() != 1 {
		t.Errorf("retry should deliver exactly once, got %d", n.count())
	}
	// retry also fails: a warning, no topic in the logs, still no panic
	n2 := newNtfyStub(t)
	n2.setFail(true)
	e2 := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n2.url(), retry: 100 * time.Millisecond})
	e2.submit(t, e2.session(t), "demo-1", answersJSON(cid2, goodAnswers))
	time.Sleep(700 * time.Millisecond)
	logs := e2.logs.String()
	if !strings.Contains(logs, "ntfy notification failed, giving up") || strings.Contains(logs, e2.topic) || n2.hitCount() != ntfyMaxAttemptsForTest {
		t.Errorf("logs:\n%s", logs)
	}
	// ntfy unreachable entirely
	e3 := newEnv(t, opts{sets: gradeSets, grade: true}) // dead ntfy address
	_, _, g3 := e3.submit(t, e3.session(t), "demo-1", answersJSON(cid2, goodAnswers))
	if g3.Notify != "retrying" || g3.Mode != "graded" || strings.Contains(e3.logs.String(), e3.topic) || strings.Contains(e3.logs.String(), "127.0.0.1:1") {
		t.Errorf("unreachable ntfy: %+v\n%s", g3, e3.logs.String())
	}
}

func TestNotifyOffWithoutTopic(t *testing.T) {
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), noTopic: true})
	_, _, g := e.submit(t, e.session(t), "demo-1", answersJSON(cid1, goodAnswers))
	if g.Notify != "off" || n.count() != 0 || !g.Archived {
		t.Errorf("%+v posts=%d", g, n.count())
	}
}

func TestGradeLogsCarryNoSecrets(t *testing.T) {
	e, _, ck := gradeEnv(t, opts{})
	e.brain.setGradeMode("fail")
	e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	for _, bad := range []string{e.brain.gradeToken, e.brain.token, e.code, e.secret, e.topic, ck, "free text", "example.set:math", "Bearer"} {
		if strings.Contains(e.logs.String(), bad) {
			t.Errorf("log contains %q:\n%s", bad, e.logs.String())
		}
	}
}
