package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Daily limit UX: the brain's 429 daily_cap is archived (not recorded in the
// brain, not announced), 429s are told apart, and GET .../quota estimates the
// questions' use today from this site's own archive.

type capResp struct {
	Error         string `json:"error"`
	Message       string `json:"message"`
	Mode          string `json:"mode"`
	BrainNote     string `json:"brain_note"`
	BrainRecorded bool   `json:"brain_recorded"`
	RecordedCount int    `json:"recorded_count"`
	Archived      bool   `json:"archived"`
	Notify        string `json:"notify"`
	Replayed      bool   `json:"replayed"`
	Results       []struct {
		QRef     string `json:"qref"`
		Status   string `json:"status"`
		Recorded bool   `json:"recorded"`
		Capped   bool   `json:"capped"`
	} `json:"results"`
	Summary struct{ Recorded, Pending int } `json:"summary"`
}

type quotaResp struct {
	SetID string         `json:"set_id"`
	Day   string         `json:"day"`
	Limit int            `json:"limit"`
	Basis string         `json:"basis"`
	Local bool           `json:"local"`
	Used  map[string]int `json:"used"`
}

func (e *env) quota(t *testing.T, ck, set string) (int, quotaResp) {
	t.Helper()
	resp, b := e.do(t, "GET", "/api/practice/"+set+"/quota", "", nil, ck)
	var q quotaResp
	if resp.StatusCode == 200 {
		must(t, json.Unmarshal(b, &q))
	}
	return resp.StatusCode, q
}

func archiveLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "attempts.jsonl"))
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		must(t, json.Unmarshal([]byte(l), &m))
		out = append(out, m)
	}
	return out
}

func waitNtfy(t *testing.T, n *ntfyStub, want int) {
	t.Helper()
	for i := 0; i < 100 && n.count() < want; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if n.count() != want {
		t.Fatalf("ntfy posts: %d, want %d", n.count(), want)
	}
}

func TestDailyCapThreeOkFourthRefusedButArchived(t *testing.T) {
	e, n, ck := contractEnv(t, opts{maxPerHour: 50})
	e.brain.setGradeMode("cap3")
	for i := 1; i <= 3; i++ {
		resp, b, g := e.submit(t, ck, "demo-1", answersJSON(uuid(i), goodAnswers))
		if resp.StatusCode != 200 || !g.BrainRecorded || g.RecordedCount != 3 {
			t.Fatalf("submission %d: %d %s", i, resp.StatusCode, b)
		}
	}
	waitNtfy(t, n, 3)
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 3 || q.Used[q2] != 3 || q.Used[q3] != 3 || q.Limit != 3 || q.Basis != "site_archive" {
		t.Fatalf("after 3 submissions: %+v", q)
	}
	calls := e.brain.gradeCalls()
	// 4th: refused by the brain -> 429, but archived; not recorded; no heads-up
	resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(uuid(4), goodAnswers))
	var c capResp
	must(t, json.Unmarshal(b, &c))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "3600" || c.Error != "daily_cap" ||
		!strings.Contains(c.Message, "今天这题的次数已用完，已留档，未记入大脑") {
		t.Fatalf("4th: %d %v %s", resp.StatusCode, resp.Header, b)
	}
	if c.Mode != "archive_only" || c.BrainNote != "daily_cap" || c.BrainRecorded || c.RecordedCount != 0 || !c.Archived || c.Notify != "off" || c.Replayed {
		t.Fatalf("4th body: %s", b)
	}
	if len(c.Results) != 3 || c.Summary.Recorded != 0 || c.Summary.Pending != 3 {
		t.Fatalf("results: %s", b)
	}
	for _, r := range c.Results {
		if r.Recorded || r.Status != "pending_key" || !r.Capped {
			t.Errorf("a refused answer must not look recorded: %+v", r)
		}
	}
	lines := archiveLines(t, e.attDir)
	if len(lines) != 4 {
		t.Fatalf("archive lines %d, want 4", len(lines))
	}
	last := lines[3]
	if last["mode"] != "archive_only" || last["brain_note"] != "daily_cap" || last["client_attempt_id"] != uuid(4) {
		t.Fatalf("archived line: %v", last)
	}
	for _, it := range last["items"].([]any) {
		m := it.(map[string]any)
		if m["recorded"] != false || m["attempt_id"] != nil {
			t.Errorf("archived item marked recorded: %v", m)
		}
	}
	if n.count() != 3 {
		t.Errorf("a daily_cap attempt must not be announced (ntfy %d)", n.count())
	}
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 3 { // the refused one does not count
		t.Errorf("refused attempt counted: %+v", q)
	}
	// replay of the refused id: the same 429 + body, no brain call, no second line
	calls = e.brain.gradeCalls()
	resp, b, _ = e.submit(t, ck, "demo-1", answersJSON(uuid(4), goodAnswers))
	var rp capResp
	must(t, json.Unmarshal(b, &rp))
	if resp.StatusCode != 429 || !rp.Replayed || rp.Error != "daily_cap" || rp.BrainNote != "daily_cap" || e.brain.gradeCalls() != calls || len(archiveLines(t, e.attDir)) != 4 {
		t.Fatalf("replay: %d %s calls %d->%d lines %d", resp.StatusCode, b, calls, e.brain.gradeCalls(), len(archiveLines(t, e.attDir)))
	}
	// a NEW id with the same answers is asked again (and refused again, archived again)
	resp, _, _ = e.submit(t, ck, "demo-1", answersJSON(uuid(5), goodAnswers))
	if resp.StatusCode != 429 || len(archiveLines(t, e.attDir)) != 5 || e.brain.gradeCalls() != calls+1 {
		t.Errorf("new id: %d lines %d", resp.StatusCode, len(archiveLines(t, e.attDir)))
	}
	// after a restart the refused id still replays as refused
	n2 := newNtfyStub(t)
	e2 := newEnv(t, opts{sets: contractSets, grade: true, ntfyURL: n2.url(), reuse: e, maxPerHour: 50})
	ck2 := e2.session(t)
	resp, b, _ = e2.submit(t, ck2, "demo-1", answersJSON(uuid(4), goodAnswers))
	if resp.StatusCode != 429 || !strings.Contains(string(b), `"replayed":true`) {
		t.Errorf("after restart: %d %s", resp.StatusCode, b)
	}
	if _, q := e2.quota(t, ck2, "demo-1"); q.Used[q1] != 3 {
		t.Errorf("after restart the estimate is rebuilt from the archive: %+v", q)
	}
}

// the brain refuses the WHOLE batch when one question is capped; only that one is marked capped
func TestDailyCapWholeBatchRefusedOnlyNamedQuestionMarked(t *testing.T) {
	e, _, ck := contractEnv(t, opts{maxPerHour: 50})
	e.brain.setGradeMode("cap3")
	for i := 1; i <= 3; i++ {
		if resp, b, _ := e.submit(t, ck, "demo-1", ans(uuid(i), q1+`|["1","2","3"]`)); resp.StatusCode != 200 {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
	}
	resp, b, _ := e.submit(t, ck, "demo-1", ans(uuid(9), q1+`|["1","2","3"]`, q2+`|"B"`))
	var c capResp
	must(t, json.Unmarshal(b, &c))
	if resp.StatusCode != 429 || c.RecordedCount != 0 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	for _, r := range c.Results {
		if (r.QRef == q1) != r.Capped || r.Recorded {
			t.Errorf("%+v", r)
		}
	}
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 3 || q.Used[q2] != 0 {
		t.Errorf("q2 was never written: %+v", q)
	}
}

func TestOtherBrain429IsNotADailyCapAndNotArchived(t *testing.T) {
	e, n, ck := contractEnv(t, opts{})
	e.brain.setGradeMode("rl-other")
	resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "30" || !strings.Contains(string(b), `"error":"brain_rate_limited"`) || strings.Contains(string(b), "daily_cap") || strings.Contains(string(b), "已留档") {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, b)
	}
	if len(archiveLines(t, e.attDir)) != 0 || n.count() != 0 {
		t.Error("a non-daily 429 must leave no trace")
	}
	// the site's own hourly limit keeps its own code
	e2, _, ck2 := contractEnv(t, opts{maxPerHour: 1})
	e2.submit(t, ck2, "demo-1", answersJSON(uuid(1), goodAnswers))
	if resp, b, _ := e2.submit(t, ck2, "demo-1", answersJSON(uuid(2), goodAnswers)); resp.StatusCode != 429 || !strings.Contains(string(b), "too_many_submissions") {
		t.Errorf("hourly limit: %d %s", resp.StatusCode, b)
	}
}

func TestQuotaCountsOnlyConfirmedNewWrites(t *testing.T) {
	e, _, ck := contractEnv(t, opts{maxPerHour: 50})
	if code, _ := e.quota(t, "", "demo-1"); code != 401 {
		t.Errorf("quota needs a login: %d", code)
	}
	if code, _ := e.quota(t, ck, "nope-9"); code != 404 {
		t.Errorf("unknown set: %d", code)
	}
	if code, q := e.quota(t, ck, "demo-1"); code != 200 || len(q.Used) != 3 || q.Used[q1] != 0 {
		t.Fatalf("fresh: %d %+v", code, q)
	}
	e.submit(t, ck, "demo-1", ans(uuid(1), q1+`|["1","2","3"]`, q2+`|"B"`))
	e.submit(t, ck, "demo-1", ans(uuid(1), q1+`|["1","2","3"]`, q2+`|"B"`)) // replay: not counted
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 1 || q.Used[q2] != 1 || q.Used[q3] != 0 {
		t.Errorf("replay must not count: %+v", q)
	}
	// unsupported (q4: recorded:false) and a degraded (brain down) submission do not count
	e.submit(t, ck, "other-2", ans(uuid(2), q4+`|"x"`))
	if _, q := e.quota(t, ck, "other-2"); q.Used[q4] != 0 {
		t.Errorf("unsupported counted: %+v", q)
	}
	e.brain.setGradeMode("fail")
	e.submit(t, ck, "demo-1", ans(uuid(3), q3+`|"free"`))
	e.brain.setGradeMode("norecord")
	e.submit(t, ck, "demo-1", ans(uuid(4), q3+`|"free"`))
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q3] != 0 {
		t.Errorf("unrecorded counted: %+v", q)
	}
	// needs_review IS recorded by the brain -> counts
	e.brain.setGradeMode("")
	e.submit(t, ck, "demo-1", ans(uuid(5), q1+`|["1","2","~review~"]`))
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 2 {
		t.Errorf("needs_review must count: %+v", q)
	}
}

// the day is the Asia/Shanghai calendar day of the submission
func TestQuotaShanghaiDayBoundary(t *testing.T) {
	var mu sync.Mutex
	cur := time.Date(2026, 10, 17, 15, 59, 0, 0, time.UTC) // 23:59 Shanghai, 17th
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	e, _, ck := contractEnv(t, opts{maxPerHour: 50, now: now})
	e.submit(t, ck, "demo-1", ans(uuid(1), q2+`|"B"`))
	_, q := e.quota(t, ck, "demo-1")
	if q.Day != "2026-10-17" || q.Used[q2] != 1 {
		t.Fatalf("%+v", q)
	}
	mu.Lock()
	cur = cur.Add(2 * time.Minute) // 00:01 Shanghai, 18th
	mu.Unlock()
	_, q = e.quota(t, ck, "demo-1")
	if q.Day != "2026-10-18" || q.Used[q2] != 0 {
		t.Fatalf("new Shanghai day starts at 0: %+v", q)
	}
	e.submit(t, ck, "demo-1", ans(uuid(2), q2+`|"B"`))
	if _, q = e.quota(t, ck, "demo-1"); q.Used[q2] != 1 {
		t.Errorf("%+v", q)
	}
}

// v1 archive lines and local test sets never count
func TestQuotaIgnoresV1AndTestRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	must(t, os.MkdirAll(dir, 0o700))
	today := time.Now().UTC().Format(time.RFC3339)
	v1 := `{"v":1,"client_attempt_id":"` + cid1 + `","set_id":"demo-1","set_title":"x","source":"` + practiceSrc + `","submitted_at":"` + today + `","mode":"graded","items":[{"qref":"` + q1 + `","status":"graded","correct":true}],"summary":{"total":1},"client":{}}`
	v2test := `{"v":2,"client_attempt_id":"` + uuid(7) + `","set_id":"demo-1","set_title":"x","source":"` + practiceSrc + `","submitted_at":"` + today + `","mode":"archive_only","test":true,"items":[{"qref":"` + q1 + `","status":"graded","correct":true,"recorded":true}],"summary":{"total":1},"client":{}}`
	must(t, os.WriteFile(filepath.Join(dir, "attempts.jsonl"), []byte(v1+"\n"+v2test+"\n"), 0o600))
	e, _, ck := contractEnv(t, opts{attemptsDir: dir})
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q1] != 0 {
		t.Errorf("%+v", q)
	}
}

// Contract change of this feature, spelled out: a refused id replays as refused, so the page drops it;
// a NEW id for the same answers is asked again, and is recorded once the brain accepts (next day).
func TestDailyCapRefusedIdStaysRefusedNewIdIsRecordedNextDay(t *testing.T) {
	e, _, ck := contractEnv(t, opts{maxPerHour: 50})
	e.brain.setGradeMode("cap3")
	for i := 1; i <= 3; i++ {
		e.submit(t, ck, "demo-1", ans(uuid(i), q2+`|"B"`))
	}
	if resp, _, _ := e.submit(t, ck, "demo-1", ans(uuid(4), q2+`|"B"`)); resp.StatusCode != 429 {
		t.Fatalf("4th: %d", resp.StatusCode)
	}
	e.brain.mu.Lock()
	e.brain.gradeDayCnt = map[string]int{} // the brain's Shanghai day rolls over
	e.brain.mu.Unlock()
	calls := e.brain.gradeCalls()
	// the old, refused id still replays the stored refusal (the brain is not asked, nothing is recorded under it)
	resp, b, _ := e.submit(t, ck, "demo-1", ans(uuid(4), q2+`|"B"`))
	if resp.StatusCode != 429 || !strings.Contains(string(b), `"replayed":true`) || e.brain.gradeCalls() != calls {
		t.Fatalf("old id: %d %s", resp.StatusCode, b)
	}
	// a new id (what the page does after a daily_cap) is accepted and recorded
	resp, b, g := e.submit(t, ck, "demo-1", ans(uuid(5), q2+`|"B"`))
	if resp.StatusCode != 200 || !g.BrainRecorded || g.Replayed {
		t.Fatalf("new id: %d %s", resp.StatusCode, b)
	}
	if _, q := e.quota(t, ck, "demo-1"); q.Used[q2] != 4 { // 3 from the first day + 1 now (same Shanghai day on this clock)
		t.Errorf("%+v", q)
	}
}
