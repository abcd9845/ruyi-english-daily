package server_test

import (
	"encoding/json"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localSets: two real sets and the local test sets. The fill_blank answer is a
// distinctive marker so a leak is greppable (an option key like "B" is not).
const localAnsMark = "ZZ-LOCAL-ANSWER-MARK"

const localSets = `{"sets":[
 {"id":"demo-1","title":"演示套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"smoke-test","local":true,"title":"测试套","questions":[
   {"qref":"smoke:q1","qtype":"single_choice","stem":"【测试题，随便填】1 + 1 = ?","options":[{"key":"A","text":"1"},{"key":"B","text":"2"},{"key":"C","text":"3"}],"answer":["B"]}]},
 {"id":"test-fill","local":true,"title":"填空测试","questions":[
   {"qref":"smoke:f1","qtype":"fill_blank","stem":"测试题 ____ 和 ____","answer":["` + localAnsMark + `","  Two "]},
   {"qref":"smoke:f2","qtype":"single_choice","stem":"第二题","options":[{"key":"A","text":"x"},{"key":"B","text":"y"}],"answer":["A"]}]}
]}`

func localEnv(t *testing.T, o opts) (*env, *ntfyStub, string) {
	n := newNtfyStub(t)
	o.sets, o.ntfyURL = localSets, n.url()
	e := newEnv(t, o)
	return e, n, e.session(t)
}

func smokeBody(cid, key string) string {
	b, _ := json.Marshal(map[string]any{"client_attempt_id": cid, "answers": []map[string]any{{"qref": "smoke:q1", "answer": key}}})
	return string(b)
}

func lastLine(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "attempts.jsonl"))
	must(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var m map[string]any
	must(t, json.Unmarshal([]byte(lines[len(lines)-1]), &m))
	return m
}

func TestLocalSetServedWithoutBrainAndWithoutAnswers(t *testing.T) {
	e, _, ck := localEnv(t, opts{grade: true})
	resp, b := e.do(t, "GET", "/api/practice/smoke-test", "", nil, ck)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var got struct {
		SetID string `json:"set_id"`
		Title string `json:"title"`
		Local bool   `json:"local"`
		Items []struct {
			QRef    string `json:"qref"`
			QType   string `json:"qtype"`
			StemMD  string `json:"stem_md"`
			Options []struct{ Key, TextMD string }
			Rev     int `json:"rev"`
		} `json:"items"`
	}
	must(t, json.Unmarshal(b, &got))
	if got.SetID != "smoke-test" || !got.Local || len(got.Items) != 1 || got.Items[0].QRef != "smoke:q1" || len(got.Items[0].Options) != 3 ||
		!strings.Contains(got.Items[0].StemMD, "测试题，随便填") || got.Items[0].Rev != 0 {
		t.Errorf("response: %s", b)
	}
	if e.brain.total() != 0 {
		t.Errorf("serving a local set must not touch the brain: %v", e.brain.calls)
	}
	// no answer field anywhere: no "answer" key, nothing the file keeps for judging
	var generic any
	must(t, json.Unmarshal(b, &generic))
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if strings.Contains(strings.ToLower(k), "answer") || k == "explanation" || k == "solution" {
					t.Errorf("answer-like key %q in response: %s", k, b)
				}
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	walk(generic)
	// the fill_blank set: its distinctive answers are nowhere in the response
	resp, b = e.do(t, "GET", "/api/practice/test-fill", "", nil, ck)
	if resp.StatusCode != 200 || strings.Contains(string(b), localAnsMark) || strings.Contains(string(b), "Two") {
		t.Errorf("fill set: %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), `"blanks":2`) {
		t.Errorf("blank count missing: %s", b)
	}
	// login is still required
	if resp, _ := e.do(t, "GET", "/api/practice/smoke-test", "", nil, ""); resp.StatusCode != 401 {
		t.Errorf("no cookie: %d", resp.StatusCode)
	}
}

func TestLocalCheckJudgesOnServerAndLeaksNothing(t *testing.T) {
	e, ntfy, ck := localEnv(t, opts{grade: true})
	hdr := map[string]string{"Content-Type": "application/json"}
	check := func(set, body string) (int, string) {
		resp, b := e.do(t, "POST", "/api/practice/"+set+"/check", body, hdr, ck)
		return resp.StatusCode, string(b)
	}
	type cr struct {
		Test    bool `json:"test"`
		Results []struct {
			QRef    string `json:"qref"`
			Status  string `json:"status"`
			Correct *bool  `json:"correct"`
		} `json:"results"`
	}
	run := func(set, body string) cr {
		t.Helper()
		code, b := check(set, body)
		if code != 200 {
			t.Fatalf("%d %s", code, b)
		}
		if strings.Contains(b, localAnsMark) || strings.Contains(strings.ToLower(b), `"answer`) {
			t.Errorf("check response leaks: %s", b)
		}
		var r cr
		must(t, json.Unmarshal([]byte(b), &r))
		return r
	}
	want := func(r cr, i int, status string, correct *bool) {
		t.Helper()
		g := r.Results[i]
		if g.Status != status || (g.Correct == nil) != (correct == nil) || (correct != nil && *g.Correct != *correct) {
			t.Errorf("result %d: %+v", i, g)
		}
	}
	yes, no := true, false
	r := run("smoke-test", `{"answers":[{"qref":"smoke:q1","answer":"B"}]}`)
	if !r.Test {
		t.Error("test flag")
	}
	want(r, 0, "graded", &yes)
	want(run("smoke-test", `{"answers":[{"qref":"smoke:q1","answer":"A"}]}`), 0, "graded", &no)
	want(run("smoke-test", `{"answers":[{"qref":"smoke:q1","answer":""}]}`), 0, "unanswered", nil)
	// fill_blank: trimmed + case-insensitive, per blank; second question not sent = unanswered
	r = run("test-fill", `{"answers":[{"qref":"smoke:f1","answer":["`+strings.ToLower(localAnsMark)+`"," two"]}]}`)
	want(r, 0, "graded", &yes)
	want(r, 1, "unanswered", nil)
	want(run("test-fill", `{"answers":[{"qref":"smoke:f1","answer":["x","two"]}]}`), 0, "graded", &no)

	// refusals: real set, unknown set, foreign qref, wrong shape, bad body, wrong method
	if c, _ := check("demo-1", `{"answers":[{"qref":"example.set:math:1","answer":["1","2","3"]}]}`); c != 404 {
		t.Errorf("a real set has no check: %d", c)
	}
	if c, _ := check("nope", `{"answers":[{"qref":"smoke:q1","answer":"B"}]}`); c != 404 {
		t.Errorf("unknown set: %d", c)
	}
	for name, body := range map[string]string{
		"foreign qref": `{"answers":[{"qref":"example.set:math:1","answer":"B"}]}`,
		"bad key":      `{"answers":[{"qref":"smoke:q1","answer":"Z"}]}`,
		"array answer": `{"answers":[{"qref":"smoke:q1","answer":["B"]}]}`,
		"duplicate":    `{"answers":[{"qref":"smoke:q1","answer":"B"},{"qref":"smoke:q1","answer":"A"}]}`,
		"empty list":   `{"answers":[]}`,
		"unknown key":  `{"answers":[{"qref":"smoke:q1","answer":"B"}],"x":1}`,
		"trailing":     `{"answers":[{"qref":"smoke:q1","answer":"B"}]} {}`,
		"not json":     `x`,
	} {
		if c, b := check("smoke-test", body); c != 400 {
			t.Errorf("%s: %d %s", name, c, b)
		}
	}
	if resp, _ := e.do(t, "GET", "/api/practice/smoke-test/check", "", nil, ck); resp.StatusCode != 405 && resp.StatusCode != 400 {
		t.Errorf("GET check: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/practice/smoke-test/check", `{"answers":[{"qref":"smoke:q1","answer":"B"}]}`, hdr, ""); resp.StatusCode != 401 {
		t.Errorf("no cookie: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/practice/smoke-test/check", `{}`, map[string]string{"Content-Type": "text/plain"}, ck); resp.StatusCode != 415 {
		t.Errorf("content type: %d", resp.StatusCode)
	}
	// checking never touches the brain, the archive or ntfy
	if e.brain.total() != 0 || countLines(t, e.attDir) != 0 || ntfy.count() != 0 {
		t.Errorf("check side effects: brain=%v lines=%d ntfy=%d", e.brain.calls, countLines(t, e.attDir), ntfy.count())
	}
}

// The core promise: a local set never calls the brain on submit, even with a
// grade token configured — 0 requests of any kind (grade, lookup, list, pick, media).
func TestLocalSubmitNeverCallsBrainEvenWithGradeToken(t *testing.T) {
	e, ntfy, ck := localEnv(t, opts{grade: true, auto: []string{practiceSrc}})
	_, _, _ = e.submit(t, ck, "smoke-test", smokeBody(cid1, "B"))
	resp, b, g := e.submit(t, ck, "smoke-test", smokeBody(cid2, "A"))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if e.brain.total() != 0 || e.brain.gradeCalls() != 0 {
		t.Fatalf("brain was called: %v", e.brain.calls)
	}
	for _, k := range []string{"grade", "lookup", "list", "pick", "ticket", "media", "grade_new_item"} {
		if e.brain.count(k) != 0 {
			t.Errorf("brain %s calls = %d", k, e.brain.count(k))
		}
	}
	if g.Mode != "archive_only" || g.BrainNote != "test_set" || g.BrainRecorded || g.RecordedCount != 0 || g.Summary.Recorded != 0 || !g.Archived || g.SetID != "smoke-test" {
		t.Errorf("response: %+v", g)
	}
	if len(g.Results) != 1 || g.Results[0].Recorded || g.Results[0].Status != "graded" || g.Results[0].Correct == nil || *g.Results[0].Correct || g.Results[0].AttemptID != 0 {
		t.Errorf("result: %+v", g.Results)
	}
	var generic map[string]any
	must(t, json.Unmarshal(b, &generic))
	if generic["test"] != true {
		t.Errorf("response must say test:true: %s", b)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), e.brain.gradeToken) {
		t.Errorf("leak: %s", b)
	}
	// archive: test:true, archive_only/test_set, nothing recorded
	rec := lastLine(t, e.attDir)
	if rec["test"] != true || rec["mode"] != "archive_only" || rec["brain_note"] != "test_set" || rec["set_id"] != "smoke-test" || rec["source"] != "local.smoke" {
		t.Errorf("archive: %v", rec)
	}
	items := rec["items"].([]any)
	if it := items[0].(map[string]any); it["recorded"] != false || it["qref"] != "smoke:q1" || it["answer"] != "A" {
		t.Errorf("archive item: %v", it)
	}
	if countLines(t, e.attDir) != 2 {
		t.Errorf("lines = %d", countLines(t, e.attDir))
	}
	// ntfy: 【测试】 in the title (RFC 2047) and as the first thing of the text (no JSON any more)
	if ntfy.count() != 2 {
		t.Fatalf("ntfy posts = %d", ntfy.count())
	}
	p := ntfy.last()
	title, err := new(mime.WordDecoder).DecodeHeader(p.Header.Get("Title"))
	must(t, err)
	if !strings.HasPrefix(title, "【测试】") || !strings.Contains(title, "Ruyi practice") {
		t.Errorf("ntfy title %q (raw %q)", title, p.Header.Get("Title"))
	}
	for _, c := range p.Header.Get("Title") {
		if c > 0x7e {
			t.Errorf("raw Title header must be ASCII (RFC 2047): %q", p.Header.Get("Title"))
			break
		}
	}
	if !strings.HasPrefix(p.Body, "【测试】") || !strings.Contains(p.Body, "未记入大脑") || strings.Contains(p.Body, "已提交。") || !strings.Contains(p.Body, "错 1") {
		t.Errorf("ntfy body: %s", p.Body)
	}
	if strings.ContainsAny(p.Body, "{}\n") || strings.Contains(p.Body, "smoke:q1") || strings.Contains(p.Body, "test\":") {
		t.Errorf("ntfy body must not carry JSON or qrefs: %s", p.Body)
	}
	// the topic is only in the URL path
	if strings.Contains(p.Body, e.topic) || strings.Contains(e.logs.String(), e.topic) {
		t.Error("topic leaked")
	}
}

func TestLocalSubmitReplayAndRestart(t *testing.T) {
	e, ntfy, ck := localEnv(t, opts{grade: true})
	_, _, g1 := e.submit(t, ck, "smoke-test", smokeBody(cid1, "B"))
	_, b, g2 := e.submit(t, ck, "smoke-test", smokeBody(cid1, "B"))
	if !g2.Replayed || g2.Mode != "archive_only" || g1.ClientAttemptID != g2.ClientAttemptID || countLines(t, e.attDir) != 1 || ntfy.count() != 1 {
		t.Errorf("replay: %+v lines=%d ntfy=%d", g2, countLines(t, e.attDir), ntfy.count())
	}
	if !strings.Contains(string(b), `"test":true`) {
		t.Errorf("a replay still says test:true: %s", b)
	}
	n2 := newNtfyStub(t)
	e2 := newEnv(t, opts{sets: localSets, grade: true, ntfyURL: n2.url(), reuse: e})
	_, b, g3 := e2.submit(t, e2.session(t), "smoke-test", smokeBody(cid1, "B"))
	if !g3.Replayed || !strings.Contains(string(b), `"test":true`) || n2.count() != 0 || e.brain.total() != 0 {
		t.Errorf("after restart: %s ntfy=%d brain=%v", b, n2.count(), e.brain.calls)
	}
}

func TestLocalSubmitRateLimitPerSet(t *testing.T) {
	e, _, ck := localEnv(t, opts{maxPerHour: 2})
	for i, want := range []int{200, 200, 429} {
		keys := []string{"A", "B", "C"}
		if resp, b, _ := e.submit(t, ck, "smoke-test", smokeBody(uuid(10+i), keys[i])); resp.StatusCode != want {
			t.Errorf("submit %d: %d %s", i, resp.StatusCode, b)
		}
	}
	// the limit is per set: another set is unaffected
	resp, b, _ := e.submit(t, ck, "test-fill", `{"client_attempt_id":"`+uuid(21)+`","answers":[{"qref":"smoke:f2","answer":"A"}]}`)
	if resp.StatusCode != 200 {
		t.Errorf("other set: %d %s", resp.StatusCode, b)
	}
}

func TestLocalSubmitValidation(t *testing.T) {
	e, ntfy, ck := localEnv(t, opts{grade: true})
	for name, body := range map[string]string{
		"foreign qref":  `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"example.set:math:1","answer":"B"}]}`,
		"bad key":       `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"smoke:q1","answer":"Z"}]}`,
		"nothing":       `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"smoke:q1","answer":""}]}`,
		"too many":      `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"smoke:q1","answer":"B"},{"qref":"smoke:q1","answer":"B"}]}`,
		"bad attempt":   `{"client_attempt_id":"nope","answers":[{"qref":"smoke:q1","answer":"B"}]}`,
		"unknown field": `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"smoke:q1","answer":"B"}],"x":1}`,
	} {
		if resp, b, _ := e.submit(t, ck, "smoke-test", body); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	if e.brain.total() != 0 || countLines(t, e.attDir) != 0 || ntfy.count() != 0 {
		t.Errorf("rejected submissions had effects: brain=%v lines=%d ntfy=%d", e.brain.calls, countLines(t, e.attDir), ntfy.count())
	}
}

// Real sets keep today's behaviour byte for byte: no test field in the archive or
// the response, the plain ntfy title, no 【测试】 anywhere.
func TestRealSetUnchangedNextToLocalSets(t *testing.T) {
	e, ntfy, ck := localEnv(t, opts{grade: true})
	resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 || g.Mode != "graded" || !g.BrainRecorded {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if strings.Contains(string(b), `"test"`) {
		t.Errorf("real response has a test field: %s", b)
	}
	rec := lastLine(t, e.attDir)
	if _, has := rec["test"]; has {
		t.Errorf("real archive line has a test field: %v", rec)
	}
	raw, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	if strings.Contains(string(raw), `"test"`) || strings.Contains(string(raw), "测试") {
		t.Errorf("archive: %s", raw)
	}
	p := ntfy.last()
	if p.Header.Get("Title") != "Ruyi practice" || strings.Contains(p.Body, "测试") || strings.Contains(p.Body, `"test"`) || !strings.HasPrefix(p.Body, "如意练习：演示套 ") {
		t.Errorf("real ntfy changed: title=%q body=%s", p.Header.Get("Title"), p.Body)
	}
	if e.brain.gradeCalls() != 1 {
		t.Errorf("real set must still be graded by the brain: %v", e.brain.calls)
	}
	// and the real set's GET has no "local" key, check is 404
	_, b = e.do(t, "GET", "/api/practice/demo-1", "", nil, ck)
	if strings.Contains(string(b), `"local"`) {
		t.Errorf("real set response has a local key: %s", b)
	}
}

// A local set is never picked by the automatic set builder, and its reserved
// source cannot be turned into an auto source or a media/brain source.
func TestLocalSetNotAutoSelectable(t *testing.T) {
	// the reserved source slipped into AutoSources / Sources directly (config refuses it, the server does too)
	e, _, ck := localEnv(t, opts{grade: true, auto: []string{practiceSrc, "local.smoke"}})
	if code, _, b := e.autoNew(t, ck, "source=local.smoke&n=1"); code != 400 {
		t.Errorf("auto source local.smoke: %d %s", code, b)
	}
	if e.brain.total() != 0 {
		t.Errorf("brain touched: %v", e.brain.calls)
	}
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=5&order=seq")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if strings.HasPrefix(a.SetID, "smoke-") || strings.HasPrefix(a.SetID, "test-") || !strings.HasPrefix(a.SetID, "auto-") {
		t.Errorf("auto set id %q", a.SetID)
	}
	resp, pb := e.do(t, "GET", "/api/practice/"+a.SetID, "", nil, ck)
	if resp.StatusCode != 200 || strings.Contains(string(pb), "smoke:") || strings.Contains(string(pb), `"local"`) {
		t.Errorf("auto set content: %d %s", resp.StatusCode, pb)
	}
	// with the brain's pick modes too (the pick call itself is the brain's; the set is built from its answer only)
	if code, _, b := e.autoNew(t, ck, "source=local.smoke&n=1&mode=due_first"); code != 400 {
		t.Errorf("pick for local.smoke: %d %s", code, b)
	}
	if e.brain.count("pick") != 0 {
		t.Errorf("pick called for the reserved source: %v", e.brain.calls)
	}
	// media / old sets page: the reserved source is not a brain source
	for _, p := range []string{"/api/media/local.smoke/" + strings.Repeat("a", 64), "/api/sets/local.smoke"} {
		if resp, _ := e.do(t, "GET", p, "", nil, ck); resp.StatusCode != 404 && resp.StatusCode != 400 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	// the registry exposes no brain source for it: the local set cannot be reached through its qref either
	if e.brain.count("media") != 0 || e.brain.count("ticket") != 0 {
		t.Errorf("media path reached the brain: %v", e.brain.calls)
	}
}

// With a configured-but-never-reachable brain and no grade token the local set still works completely.
func TestLocalSetWorksWithDeadBrain(t *testing.T) {
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: localSets, ntfyURL: n.url(), brainURL: "http://127.0.0.1:1"})
	ck := e.session(t)
	if resp, b := e.do(t, "GET", "/api/practice/smoke-test", "", nil, ck); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if resp, b, g := e.submit(t, ck, "smoke-test", smokeBody(cid1, "B")); resp.StatusCode != 200 || g.Mode != "archive_only" || g.BrainNote != "test_set" || !g.Archived || g.Notify != "sent" {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
	if strings.Contains(e.logs.String(), "brain request failed") {
		t.Errorf("a brain call was attempted: %s", e.logs.String())
	}
}
