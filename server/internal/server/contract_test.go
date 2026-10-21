package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the brain "final contract" adapter: grade statuses and errors,
// idempotency, rev, lookup/pick endpoints, degradation and token hygiene.
// Everything runs against the in-process fake brain; no real token is used.

const contractSets = `{"sets":[
 {"id":"demo-1","title":"演示套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"other-2","title":"另一套","source":"` + practiceSrc + `","qrefs":["example.set:math:4"]},
 {"id":"mixed-3","title":"混合套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:4"]},
 {"id":"wide-4","title":"多空套","source":"` + practiceSrc + `","qrefs":["example.set:math:11","example.set:math:2"]},
 {"id":"gone-5","title":"缺题套","source":"` + practiceSrc + `","qrefs":["example.set:math:2","example.set:math:404"]}
]}`

func contractEnv(t *testing.T, o opts) (*env, *ntfyStub, string) {
	n := newNtfyStub(t)
	o.sets, o.grade, o.ntfyURL = contractSets, true, n.url()
	e := newEnv(t, o)
	return e, n, e.session(t)
}

func ans(cid string, rows ...string) string { // rows: qref, answer-json, [rev], …  as "qref|json|rev"
	var list []map[string]any
	for _, r := range rows {
		p := strings.SplitN(r, "|", 3)
		m := map[string]any{"qref": p[0]}
		var a any
		_ = json.Unmarshal([]byte(p[1]), &a)
		m["answer"] = a
		if len(p) == 3 {
			var rv int
			_ = json.Unmarshal([]byte(p[2]), &rv)
			m["rev"] = rv
		}
		list = append(list, m)
	}
	b, _ := json.Marshal(map[string]any{"client_attempt_id": cid, "answers": list})
	return string(b)
}

var q1, q2, q3, q4 = "example.set:math:1", "example.set:math:2", "example.set:math:3", "example.set:math:4"

func TestContractStatusesMapAndRecordedFlags(t *testing.T) {
	e, n, ck := contractEnv(t, opts{})
	// needs_review: only the answer is recorded; no verdict
	resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q1+`|["1","2","~review~"]`, q2+`|"B"`, q3+`|"free"`))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	got := []string{}
	for _, r := range g.Results {
		got = append(got, r.Status+"/"+map[bool]string{true: "rec", false: "norec"}[r.Recorded])
		if r.Status != "graded" && r.Correct != nil {
			t.Errorf("only graded carries correct: %+v", r)
		}
	}
	if strings.Join(got, ",") != "needs_review/rec,graded/rec,pending_key/rec" || g.Summary.Graded != 1 || g.Summary.Correct != 1 || g.Summary.Pending != 2 || g.RecordedCount != 3 {
		t.Errorf("%v %+v", got, g)
	}
	if !strings.Contains(n.last().Body, "待核对 2") {
		t.Errorf("ntfy: %q", n.last().Body)
	}

	// unsupported: recorded:false, no attempt id, never "recorded in the brain"
	resp, b, g = e.submit(t, ck, "other-2", ans(cid2, q4+`|["x"]`))
	if resp.StatusCode != 200 || g.Mode != "graded" || g.BrainRecorded || g.RecordedCount != 0 || g.Summary.Unsupported != 1 || g.Summary.Pending != 0 || g.Summary.Recorded != 0 {
		t.Fatalf("unsupported: %d %s", resp.StatusCode, b)
	}
	if r := g.Results[0]; r.Status != "unsupported" || r.Recorded || r.AttemptID != 0 {
		t.Errorf("%+v", r)
	}
	if !strings.Contains(n.last().Body, "不自动判 1") || !strings.Contains(n.last().Body, "未记入大脑") || strings.Contains(n.last().Body, "已提交。") {
		t.Errorf("ntfy for all-unsupported: %q", n.last().Body)
	}
	// the archive keeps recorded:false for it
	raw, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var rec struct {
		Items []struct {
			Status    string `json:"status"`
			Recorded  bool   `json:"recorded"`
			AttemptID int64  `json:"attempt_id"`
		} `json:"items"`
	}
	must(t, json.Unmarshal([]byte(lines[1]), &rec))
	if rec.Items[0].Status != "unsupported" || rec.Items[0].Recorded || rec.Items[0].AttemptID != 0 {
		t.Errorf("archive: %s", lines[1])
	}
	// and a replay of it still says recorded:false
	_, _, gr := e.submit(t, ck, "other-2", ans(cid2, q4+`|["x"]`))
	if !gr.Replayed || gr.BrainRecorded || gr.Results[0].Recorded {
		t.Errorf("replay: %+v", gr)
	}

	// partial: one recorded, one unsupported
	_, _, g = e.submit(t, ck, "mixed-3", ans(uuid(3), q1+`|["1","2","3"]`, q4+`|["y"]`))
	if !g.BrainRecorded || g.RecordedCount != 1 || g.Summary.Recorded != 1 || g.Summary.Unsupported != 1 || g.Summary.Correct != 1 {
		t.Errorf("partial: %+v", g)
	}
	if !strings.Contains(n.last().Body, "1 题记入大脑，其余仅留档") {
		t.Errorf("ntfy partial: %q", n.last().Body)
	}
}

func TestContractFillOverTenBlanksIsNotSentToTheBrain(t *testing.T) {
	e, _, ck := contractEnv(t, opts{})
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "v"
	}
	b11, _ := json.Marshal(eleven)
	resp, b, g := e.submit(t, ck, "wide-4", ans(cid1, "example.set:math:11|"+string(b11), q2+`|"B"`))
	if resp.StatusCode != 200 || g.RecordedCount != 1 || g.Results[0].Status != "unsupported" || g.Results[0].Recorded || g.Results[1].Status != "graded" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if strings.Contains(string(e.brain.gradeReqs[0]), "example.set:math:11") {
		t.Errorf("a >10-blank answer must not be sent: %s", e.brain.gradeReqs[0])
	}
	// only such an answer: nothing is sent at all, archive-only
	resp, b, g = e.submit(t, ck, "wide-4", ans(cid2, "example.set:math:11|"+string(b11)))
	if resp.StatusCode != 200 || g.Mode != "archive_only" || g.BrainRecorded || e.brain.gradeCalls() != 1 {
		t.Errorf("%d %s calls=%d", resp.StatusCode, b, e.brain.gradeCalls())
	}
}

func TestContractRequestBodyCarriesRevKindAnsweredAtSessionRef(t *testing.T) {
	e, _, ck := contractEnv(t, opts{})
	_, p, _ := getPractice(t, e, ck, "demo-1")
	for _, it := range p.Items {
		if it.Rev != 1 {
			t.Fatalf("lookup rev not exposed to the page: %+v", it)
		}
	}
	e.brain.setRev(q2, 4)
	_, p, _ = getPractice(t, e, ck, "demo-1")
	if p.Items[1].Rev != 4 {
		t.Fatalf("rev: %+v", p.Items[1])
	}
	body := `{"client_attempt_id":"` + cid1 + `","attempt_kind":"correction","answered_at":"` + nowRFC(t) + `","answers":[` +
		`{"qref":"` + q1 + `","answer":["1","2","3"],"rev":1},{"qref":"` + q2 + `","answer":"B","rev":4,"attempt_kind":"variant"}]}`
	resp, b, g := e.submit(t, ck, "demo-1", body)
	if resp.StatusCode != 200 || g.RecordedCount != 2 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	m := e.brain.lastGradeReq()
	items := m["items"].([]any)
	i1, i2 := items[0].(map[string]any), items[1].(map[string]any)
	if m["brain"] != "ruyi" || m["client_attempt_id"] != cid1 || i1["rev"].(float64) != 1 || i2["rev"].(float64) != 4 ||
		i1["attempt_kind"] != "correction" || i2["attempt_kind"] != "variant" || i1["session_ref"] != cid1 || i2["session_ref"] != cid1 || i1["answered_at"] == nil || i1["answered_at"] != i2["answered_at"] {
		t.Errorf("request: %v", m)
	}
	// bad inputs are refused before the brain is asked
	calls := e.brain.gradeCalls()
	for name, bad := range map[string]string{
		"kind":      `{"client_attempt_id":"` + uuid(5) + `","attempt_kind":"cheat","answers":[{"qref":"` + q2 + `","answer":"B"}]}`,
		"rev0":      `{"client_attempt_id":"` + uuid(6) + `","answers":[{"qref":"` + q2 + `","answer":"B","rev":0}]}`,
		"revhuge":   `{"client_attempt_id":"` + uuid(7) + `","answers":[{"qref":"` + q2 + `","answer":"B","rev":99999999999}]}`,
		"timefmt":   `{"client_attempt_id":"` + uuid(8) + `","answered_at":"yesterday","answers":[{"qref":"` + q2 + `","answer":"B"}]}`,
		"dupe":      `{"client_attempt_id":"` + uuid(9) + `","answers":[{"qref":"` + q2 + `","answer":"B"},{"qref":"` + q2 + `","answer":"A"}]}`,
		"answerkey": `{"client_attempt_id":"` + uuid(10) + `","answers":[{"qref":"` + q2 + `","answer":"B","correct":true}]}`,
	} {
		if resp, _, _ := e.submit(t, ck, "demo-1", bad); resp.StatusCode != 400 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if e.brain.gradeCalls() != calls {
		t.Error("refused requests reached the brain")
	}
	// an answered_at far in the past or future is simply not forwarded
	for i, at := range []string{"2001-01-01T00:00:00Z", "2999-01-01T00:00:00Z"} {
		e.submit(t, ck, "demo-1", `{"client_attempt_id":"`+uuid(20+i)+`","answered_at":"`+at+`","answers":[{"qref":"`+q2+`","answer":"B"}]}`)
		if e.brain.lastGradeReq()["items"].([]any)[0].(map[string]any)["answered_at"] != nil {
			t.Errorf("out-of-window answered_at forwarded: %s", at)
		}
	}
}

func TestContractBrainErrorsMapToPageStates(t *testing.T) {
	type tc struct {
		mode, code, errID, msg string
		archived               bool
	}
	for _, c := range []tc{
		{"rl-other", "429", "brain_rate_limited", "大脑这会儿请求太多", false},
		{"rev", "409", "rev_mismatch", "题目已更新，请刷新", false},
		{"idem", "409", "idempotency_conflict", "换新编号", false},
		{"unknown", "404", "unknown_qref", "找不到了", false},
	} {
		t.Run(c.mode, func(t *testing.T) {
			e, n, ck := contractEnv(t, opts{})
			e.brain.setGradeMode(c.mode)
			resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
			if got := resp.Status[:3]; got != c.code || !strings.Contains(string(b), `"error":"`+c.errID+`"`) || !strings.Contains(string(b), c.msg) {
				t.Fatalf("%s %s", resp.Status, b)
			}
			if c.errID == "idempotency_conflict" && !strings.Contains(string(b), `"retry_new_id":true`) {
				t.Errorf("page needs retry_new_id: %s", b)
			}
			// nothing written anywhere: no archive, no heads-up, no stale idempotency lock
			if countLines(t, e.attDir) != 0 || n.count() != 0 {
				t.Errorf("archive=%d ntfy=%d", countLines(t, e.attDir), n.count())
			}
			e.brain.setGradeMode("")
			resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
			if resp.StatusCode != 200 || g.Replayed || !g.BrainRecorded {
				t.Errorf("the same id works once the brain accepts it: %d %s", resp.StatusCode, b)
			}
		})
	}
}

func TestContractStaleRevAgainstFakeBrain(t *testing.T) {
	e, _, ck := contractEnv(t, opts{})
	_, p, _ := getPractice(t, e, ck, "demo-1")
	rev := p.Items[1].Rev
	e.brain.setRev(q2, rev+1) // the question was revised after the page was served
	resp, b, _ := e.submit(t, ck, "demo-1", ans(cid1, q2+`|"B"|`+itoa(rev)))
	if resp.StatusCode != 409 || !strings.Contains(string(b), `"error":"rev_mismatch"`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// after a refresh the page carries the new rev and the same answer is accepted
	_, p, _ = getPractice(t, e, ck, "demo-1")
	resp, b, g := e.submit(t, ck, "demo-1", ans(uuid(2), q2+`|"B"|`+itoa(p.Items[1].Rev)))
	if resp.StatusCode != 200 || !g.BrainRecorded {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestContractIdempotencyAcrossRestartsAndNetworkRetries(t *testing.T) {
	e, _, ck := contractEnv(t, opts{noAttempts: true})
	_, _, g1 := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	// "restart" without an archive: the site forgot the id, the BRAIN recognises it
	e2, _, ck2 := contractEnv(t, opts{noAttempts: true, reuse: e})
	resp, b, g2 := e2.submit(t, ck2, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 200 || g2.Replayed {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	for i, r := range g2.Results {
		if !r.Duplicate || !r.Recorded || r.AttemptID != g1.Results[i].AttemptID || r.AttemptID == 0 {
			t.Errorf("duplicate delivery %d: %+v vs first %+v", i, r, g1.Results[i])
		}
	}
	if e.brain.count("grade_new_item") != 3 || e.brain.count("grade_dup_item") != 3 {
		t.Errorf("brain wrote %d new, %d dup", e.brain.count("grade_new_item"), e.brain.count("grade_dup_item"))
	}
	// same id, another answer, site has no memory: the BRAIN says idempotency_conflict -> 409 + new id advice
	resp, b, _ = e2.submit(t, ck2, "demo-1", ans(cid1, q2+`|"A"`))
	if resp.StatusCode != 409 || !strings.Contains(string(b), `"error":"idempotency_conflict"`) || !strings.Contains(string(b), `"retry_new_id":true`) {
		t.Errorf("brain-side conflict: %d %s", resp.StatusCode, b)
	}
	// ... and the NEW id (answer changed -> new id) is accepted
	resp, _, g := e2.submit(t, ck2, "demo-1", ans(uuid(77), q2+`|"A"`))
	if resp.StatusCode != 200 || !g.BrainRecorded || g.Results[0].Duplicate {
		t.Errorf("new id: %d %+v", resp.StatusCode, g)
	}
}

func TestContractUpstreamRetryReusesTheSameClientAttemptID(t *testing.T) {
	for _, mode := range []string{"fail-once", "drop-once"} {
		t.Run(mode, func(t *testing.T) {
			e, _, ck := contractEnv(t, opts{})
			e.brain.setGradeMode(mode)
			resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
			if resp.StatusCode != 200 || g.Mode != "graded" || !g.BrainRecorded {
				t.Fatalf("%d %s", resp.StatusCode, b)
			}
			if e.brain.gradeCalls() != 2 {
				t.Fatalf("grade calls %d, want 2 (one retry)", e.brain.gradeCalls())
			}
			var a, c map[string]any
			_ = json.Unmarshal(e.brain.gradeReqs[0], &a)
			_ = json.Unmarshal(e.brain.gradeReqs[1], &c)
			if a["client_attempt_id"] != cid1 || c["client_attempt_id"] != cid1 || string(e.brain.gradeReqs[0]) != string(e.brain.gradeReqs[1]) {
				t.Errorf("a retry must resend the identical request (same client_attempt_id)")
			}
		})
	}
	// a permanently failing brain: exactly one retry, then archive-only
	e, _, ck := contractEnv(t, opts{})
	e.brain.setGradeMode("fail")
	_, _, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if g.Mode != "archive_only" || e.brain.gradeCalls() != 2 {
		t.Errorf("%+v calls=%d", g, e.brain.gradeCalls())
	}
	// the endpoint missing (404/501) is not retried
	e, _, ck = contractEnv(t, opts{})
	e.brain.setGradeMode("off")
	e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if e.brain.gradeCalls() != 1 {
		t.Errorf("a missing endpoint was retried: %d", e.brain.gradeCalls())
	}
}

func TestContractUnknownQrefAtLookup(t *testing.T) {
	e, _, ck := contractEnv(t, opts{})
	code, p, _ := getPractice(t, e, ck, "gone-5")
	if code != 200 || len(p.Items) != 1 || len(p.MissingQRefs) != 1 || p.MissingQRefs[0] != "example.set:math:404" {
		t.Fatalf("missing qref must be reported, not crash: %d %+v", code, p)
	}
	resp, b, _ := e.submit(t, ck, "gone-5", ans(cid1, "example.set:math:404|\"B\""))
	if resp.StatusCode != 404 || !strings.Contains(string(b), `"error":"unknown_qref"`) || e.brain.gradeCalls() != 0 {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

func TestContractLookupFallbackWhenNotDeployed(t *testing.T) {
	for _, mode := range []string{"off", "501"} {
		e, _, ck := contractEnv(t, opts{lookup: mode})
		code, p, _ := getPractice(t, e, ck, "demo-1")
		if code != 200 || len(p.Items) != 3 {
			t.Fatalf("[%s] %d %+v", mode, code, p)
		}
		for _, it := range p.Items {
			if it.Rev != 0 {
				t.Errorf("no rev without a lookup reply: %+v", it)
			}
		}
		// the not-deployed answer is remembered: the second page load does not probe again
		getPractice(t, e, ck, "demo-1")
		if e.brain.count("lookup") != 1 || e.brain.count("list") == 0 {
			t.Errorf("[%s] lookup probes=%d list=%d", mode, e.brain.count("lookup"), e.brain.count("list"))
		}
		// grading still works through the list-based lookup, without rev
		if resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers)); resp.StatusCode != 200 || !g.BrainRecorded {
			t.Errorf("[%s] grade: %d %s", mode, resp.StatusCode, b)
		}
	}
	// a failing (500) lookup is an error, not a silent fallback
	e, _, ck := contractEnv(t, opts{lookup: "fail"})
	if code, _, _ := getPractice(t, e, ck, "demo-1"); code != 502 {
		t.Errorf("failing lookup: %d", code)
	}
}

func TestContractPickPracticeModes(t *testing.T) {
	e, _, ck := contractEnv(t, opts{auto: []string{practiceSrc}})
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=6&mode=due_first")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	q := e.brain.pickQuery[0]
	if q.Get("source") != practiceSrc || q.Get("n") != "6" || q.Get("mode") != "due_first" || q.Get("brain") != "ruyi" || q.Get("assigned") != "1" || len(q) != 5 {
		t.Errorf("pick query: %v", q)
	}
	if e.brain.pickAuths[0] != "Bearer "+e.brain.gradeToken {
		t.Error("pick-practice must use the grade token (the brain's write scope), not the read token")
	}
	_, p, _ := getPractice(t, e, ck, a.SetID)
	// the brain's duplicate (math:2 twice) is dropped; the order of the brain is kept
	if strings.Join(qrefsOf(p), ",") != "example.set:math:2,example.set:math:1,example.set:math:3,example.set:math:4,example.set:math:5" {
		t.Fatalf("order/dedupe: %v", qrefsOf(p))
	}
	want := map[string][2]string{q2: {"review", ""}, q1: {"wrong", ""}, q3: {"wrong", "pending_key"}, q4: {"wrong", "unsupported_qtype"}, "example.set:math:5": {"", "figure_missing"}}
	for _, it := range p.Items {
		if w := want[it.QRef]; it.Origin != w[0] || it.RefPending != w[1] {
			t.Errorf("%s: origin=%q ref_pending=%q want %v", it.QRef, it.Origin, it.RefPending, w)
		}
	}
	if a.Title != "复习（5 题）" || !strings.Contains(a.Note, "3 题参考答案待核，不自动判") {
		t.Errorf("%+v", a)
	}
	// deferred questions are still gradable (the brain answers pending_key / unsupported), marked in the reply
	if resp, b, g := e.submit(t, ck, a.SetID, ans(cid1, q3+`|"free"`, q4+`|["x"]`)); resp.StatusCode != 200 || g.RecordedCount != 1 {
		t.Errorf("%d %s", resp.StatusCode, b)
	}

	// wrong_only: the brain's list minus the first review item; legacy flag spelling means the same
	_, w1, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=2&mode=wrong_only")
	_, w2, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=2&wrong_only=1")
	_, pw1, _ := getPractice(t, e, ck, w1.SetID)
	_, pw2, _ := getPractice(t, e, ck, w2.SetID)
	if strings.Join(qrefsOf(pw1), ",") != "example.set:math:1,example.set:math:3" || strings.Join(qrefsOf(pw2), ",") != strings.Join(qrefsOf(pw1), ",") || w1.Title != "错题订正（2 题）" {
		t.Errorf("wrong_only: %v %v %q", qrefsOf(pw1), qrefsOf(pw2), w1.Title)
	}
	if got := e.brain.pickQuery[len(e.brain.pickQuery)-1].Get("mode"); got != "wrong_only" {
		t.Errorf("legacy flag mapped to %q", got)
	}
	for _, bad := range []string{"mode=cheat", "wrong_only=1&due_first=1"} {
		if code, _, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=2&"+bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// no mode = the legacy local pick: no pick-practice call at all
	before := e.brain.count("pick")
	if code, _, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=2"); code != 200 || e.brain.count("pick") != before {
		t.Error("legacy random pick must not call pick-practice")
	}
	// the reasons text never reaches the page (only the sanitized deferred code does)
	if strings.Contains(string(mustGet(t, e, ck, a.SetID)), "<script>") {
		t.Error("reason text leaked to the page")
	}
}

func mustGet(t *testing.T, e *env, ck, id string) []byte {
	_, _, b := getPractice(t, e, ck, id)
	return b
}

func TestContractPickSkipDeferredIsConfigurable(t *testing.T) {
	e, _, ck := contractEnv(t, opts{auto: []string{practiceSrc}, skipDeferred: true})
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=6&mode=due_first")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	_, p, _ := getPractice(t, e, ck, a.SetID)
	if strings.Join(qrefsOf(p), ",") != "example.set:math:2,example.set:math:1" || a.Count != 2 {
		t.Errorf("deferred picks must be skipped: %v %+v", qrefsOf(p), a)
	}
	for _, it := range p.Items {
		if it.RefPending != "" {
			t.Errorf("%+v", it)
		}
	}
}

// The grade token and the read token must not show up in anything the site
// emits: responses (incl. headers), logs, the attempts archive, the heads-up.
func TestContractTokensNeverLeak(t *testing.T) {
	e, n, ck := contractEnv(t, opts{auto: []string{practiceSrc}})
	var seen []string
	grab := func(status int, hdr map[string][]string, b []byte) {
		seen = append(seen, string(b))
		for k, v := range hdr {
			seen = append(seen, k+": "+strings.Join(v, ","))
		}
	}
	for i, mode := range []string{"", "429", "rev", "idem", "unknown", "fail", "off", "norecord"} {
		e.brain.setGradeMode(mode)
		resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(uuid(100+i), goodAnswers))
		grab(resp.StatusCode, resp.Header, b)
	}
	e.brain.setGradeMode("")
	for _, p := range []string{"/api/practice/demo-1", "/api/practice/new?source=" + practiceSrc + "&n=3&mode=wrong_only", "/api/config", "/api/session"} {
		resp, b := e.do(t, "GET", p, "", nil, ck)
		grab(resp.StatusCode, resp.Header, b)
	}
	raw, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	seen = append(seen, string(raw), e.logs.String())
	n.mu.Lock()
	for _, p := range n.posts {
		seen = append(seen, p.Body)
		for k, v := range p.Header {
			seen = append(seen, k+": "+strings.Join(v, ","))
		}
	}
	n.mu.Unlock()
	all := strings.Join(seen, "\n")
	for name, tok := range map[string]string{"grade token": e.brain.gradeToken, "read token": e.brain.token, "session secret": e.secret, "family code": e.code} {
		if strings.Contains(all, tok) {
			t.Errorf("%s leaked somewhere in responses/logs/archive/ntfy", name)
		}
	}
	if !strings.Contains(all, "retry_new_id") { // control: the corpus really contains the error responses
		t.Error("control failed: error responses missing from the corpus")
	}
	// each token only ever travelled on its own calls
	for _, a := range e.brain.gradeAuths {
		if a != "Bearer "+e.brain.gradeToken {
			t.Errorf("grade call with %q", a)
		}
	}
	for _, a := range e.brain.pickAuths {
		if a != "Bearer "+e.brain.gradeToken {
			t.Errorf("pick call with a non-grade token")
		}
	}
	if e.brain.pickBank != 0 || e.brain.gradeBank != 0 || len(e.brain.gradeAuths) == 0 || len(e.brain.pickAuths) == 0 {
		t.Errorf("grade calls with the read token: %d (calls %d/%d)", e.brain.gradeBank, len(e.brain.gradeAuths), len(e.brain.pickAuths))
	}
}

func nowRFC(t *testing.T) string { return time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) }

// Archives written before the final contract (v1: one brain_attempt_id per attempt,
// no per-item recorded) still replay, and keep saying the brain recorded them.
func TestContractV1ArchiveStillReplays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempts")
	must(t, os.MkdirAll(dir, 0o700))
	line := `{"v":1,"client_attempt_id":"` + cid1 + `","set_id":"demo-1","set_title":"演示套","source":"` + practiceSrc + `","submitted_at":"2026-10-01T00:00:00Z","mode":"graded","brain_attempt_id":"old-1",` +
		`"items":[{"qref":"` + q1 + `","answer":["1","2","3"],"status":"graded","correct":true},{"qref":"` + q3 + `","answer":"x","status":"pending_key","correct":null}],` +
		`"summary":{"total":3,"graded":1,"correct":1,"pending":1,"unanswered":1},"client":{}}` + "\n"
	must(t, os.WriteFile(filepath.Join(dir, "attempts.jsonl"), []byte(line), 0o600))
	e, _, ck := contractEnv(t, opts{attemptsDir: dir})
	resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q1+`|["1","2","3"]`))
	if resp.StatusCode != 200 || !g.Replayed || !g.BrainRecorded || g.RecordedCount != 2 || !g.Results[0].Recorded || !g.Results[1].Recorded {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if e.brain.gradeCalls() != 0 {
		t.Error("a replay must not call the brain")
	}
}
