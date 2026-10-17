package server_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Tests for the corrections made against the brain's interface document
// (练习站自动方案接口.md) and the brain developers' answers: how 404 is told
// apart, the real 429/409 shapes, a strict request body, aliases, n limits.

const aliasSets = `{"sets":[
 {"id":"upper-1","title":"大写套","source":"` + practiceSrc + `","qrefs":["EXAMPLE.SET:MATH:2","Example.Set:Math:1"]},
 {"id":"alias-2","title":"别名套","source":"` + practiceSrc + `","qrefs":["example.set:math:2-old","example.set:math:1"]},
 {"id":"free-3","title":"自由作答","source":"` + practiceSrc + `","qrefs":["example.set:math:3"]}
]}`

func aliasEnv(t *testing.T) (*env, *ntfyStub, string) {
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: aliasSets, grade: true, ntfyURL: n.url()})
	return e, n, e.session(t)
}

// A 404 of the grade URL is "question not found" only when it carries
// details.qrefs. Everything else (also an old-style unknown_qref code, or the
// catch-all not_found / "not found") is a brain without the route: archive-only.
func TestDocGrade404IsToldApartByDetailsNotByCode(t *testing.T) {
	for mode, wantUnknown := range map[string]bool{"unknown": true, "unknown-nodetails": false, "off-json": false, "off": false} {
		t.Run(mode, func(t *testing.T) {
			e, _, ck := contractEnv(t, opts{})
			e.brain.setGradeMode(mode)
			resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
			if wantUnknown {
				if resp.StatusCode != 404 || !strings.Contains(string(b), `"error":"unknown_qref"`) {
					t.Fatalf("%d %s", resp.StatusCode, b)
				}
				return
			}
			if resp.StatusCode != 200 || g.Mode != "archive_only" || g.BrainRecorded || g.BrainNote != "not_ready" {
				t.Fatalf("a 404 without details.qrefs must degrade: %d %s", resp.StatusCode, b)
			}
		})
	}
}

func TestDocRateLimitedKeepsRetryAfterAndOneMessage(t *testing.T) {
	e, n, ck := contractEnv(t, opts{})
	e.brain.setGradeMode("429")
	resp, b, _ := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "3600" || !strings.Contains(string(b), "今天这题提交次数到上限") || strings.Contains(string(b), "example.set") {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, b)
	}
	if countLines(t, e.attDir) != 0 || n.count() != 0 {
		t.Error("a refused batch must leave no trace")
	}
}

// Every optional field that is outside the contract is left out, every
// required one is checked: nothing dirty reaches the brain (the fake answers
// 400 to what the real one would refuse, which would show as archive_only).
func TestDocRequestIsCleanLowercaseAndKnownFieldsOnly(t *testing.T) {
	e, _, ck := aliasEnv(t)
	resp, b, g := e.submit(t, ck, "upper-1", ans(cid1, "EXAMPLE.SET:MATH:2|\"B\"", "Example.Set:Math:1|[\"1\",\"2\",\"3\"]"))
	if resp.StatusCode != 200 || g.Mode != "graded" || !g.BrainRecorded || g.RecordedCount != 2 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// the page's own qref strings are echoed back (the brain's lower-case ones stay on the wire)
	if g.Results[0].QRef != "EXAMPLE.SET:MATH:2" || g.Results[1].QRef != "Example.Set:Math:1" || g.Results[0].Correct == nil || !*g.Results[0].Correct {
		t.Errorf("%+v", g.Results)
	}
	m := e.brain.lastGradeReq()
	allowed := map[string]bool{"qref": true, "answer": true, "rev": true, "attempt_kind": true, "answered_at": true, "session_ref": true, "payload": true}
	for _, raw := range m["items"].([]any) {
		it := raw.(map[string]any)
		for k := range it {
			if !allowed[k] {
				t.Errorf("unknown field %q sent to the brain", k)
			}
		}
		if q := it["qref"].(string); q != strings.ToLower(q) {
			t.Errorf("qref not lower-cased on the wire: %s", q)
		}
	}
	for k := range m {
		if k != "brain" && k != "client_attempt_id" && k != "items" {
			t.Errorf("unknown top-level field %q", k)
		}
	}
	// the page items use the set's own strings, lookups were lower-cased
	_, p, _ := getPractice(t, e, ck, "upper-1")
	if strings.Join(qrefsOf(p), ",") != "EXAMPLE.SET:MATH:2,Example.Set:Math:1" {
		t.Errorf("page qrefs: %v", qrefsOf(p))
	}
	for _, raw := range e.brain.lookupReqs {
		if strings.ContainsAny(string(raw), "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Errorf("lookup sent upper-case: %s", raw)
		}
	}
	if e.brain.lastGradeQuery() != "" {
		t.Errorf("grade must not carry a query string: %q", e.brain.lastGradeQuery())
	}
}

// An alias resolves to its canonical question: lookup returns the canonical
// qref (the second string of one question is dropped), the page keeps the set's
// alias, the grade reply echoes the string we sent.
func TestDocAliasLookupAndGradeEchoTheRequestedString(t *testing.T) {
	e, _, ck := aliasEnv(t)
	code, p, b := getPractice(t, e, ck, "alias-2")
	if code != 200 || strings.Join(qrefsOf(p), ",") != "example.set:math:2-old,example.set:math:1" {
		t.Fatalf("%d %s", code, b)
	}
	resp, b, g := e.submit(t, ck, "alias-2", ans(cid1, "example.set:math:2-old|\"B\"", "example.set:math:1|[\"1\",\"2\",\"3\"]"))
	if resp.StatusCode != 200 || g.RecordedCount != 2 || g.Results[0].QRef != "example.set:math:2-old" || g.Results[0].Correct == nil || !*g.Results[0].Correct {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(e.brain.gradeReqs[0]), `"qref":"example.set:math:2-old"`) {
		t.Errorf("the alias goes on the wire as the string we hold: %s", e.brain.gradeReqs[0])
	}
}

func TestDocAnsweredAtOnlyInsideTheBrainsWindow(t *testing.T) {
	e, _, ck := contractEnv(t, opts{})
	cases := []struct {
		at   time.Duration
		sent bool
	}{{-3 * 24 * time.Hour, true}, {-6*24*time.Hour - 23*time.Hour, true}, {-8 * 24 * time.Hour, false}, {time.Hour, false}, {10 * time.Second, true}}
	for i, c := range cases {
		at := time.Now().UTC().Add(c.at).Format(time.RFC3339)
		resp, b, g := e.submit(t, ck, "demo-1", `{"client_attempt_id":"`+uuid(40+i)+`","answered_at":"`+at+`","answers":[{"qref":"`+q2+`","answer":"B"}]}`)
		if resp.StatusCode != 200 || !g.BrainRecorded {
			t.Fatalf("%v: %d %s", c.at, resp.StatusCode, b)
		}
		sent := e.brain.lastGradeReq()["items"].([]any)[0].(map[string]any)["answered_at"] != nil
		if sent != c.sent {
			t.Errorf("answered_at %v: sent=%v want %v", c.at, sent, c.sent)
		}
	}
}

// A free-form answer longer than the brain's 500 characters is neither
// truncated nor sent: it is archived (the brain would answer 400).
func TestDocAnswerOver500CharactersIsNotSent(t *testing.T) {
	e, _, ck := aliasEnv(t)
	long := strings.Repeat("字", 501)
	resp, b, g := e.submit(t, ck, "free-3", ans(cid1, q3+`|"`+long+`"`))
	if resp.StatusCode != 200 || g.Mode != "archive_only" || g.BrainRecorded || g.BrainNote != "not_sent" || e.brain.gradeCalls() != 0 {
		t.Fatalf("%d %s calls=%d", resp.StatusCode, b, e.brain.gradeCalls())
	}
	ok500 := strings.Repeat("字", 500)
	resp, b, g = e.submit(t, ck, "free-3", ans(uuid(2), q3+`|"`+ok500+`"`))
	if resp.StatusCode != 200 || e.brain.gradeCalls() != 1 || g.Results[0].Status != "pending_key" || !g.Results[0].Recorded {
		t.Errorf("500 characters is fine: %d %s", resp.StatusCode, b)
	}
}

// Item 7: an answer the brain's grader cannot read (a unit tail it does not
// know) comes back needs_review: shown as 待核对, recorded, never counted wrong.
func TestDocNeedsReviewIsPendingNotWrong(t *testing.T) {
	e, n, ck := contractEnv(t, opts{})
	resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q1+`|["5厘米","2","3"]`, q2+`|"B"`))
	if resp.StatusCode != 200 || g.Results[0].Status != "needs_review" || g.Results[0].Correct != nil || !g.Results[0].Recorded || g.RecordedCount != 2 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var raw struct {
		Summary struct{ Graded, Correct, Pending int } `json:"summary"`
	}
	_ = json.Unmarshal(b, &raw)
	if raw.Summary.Graded != 1 || raw.Summary.Correct != 1 || raw.Summary.Pending != 1 {
		t.Errorf("needs_review must count as pending, not wrong: %+v", raw.Summary)
	}
	if line := n.last().Body; strings.Contains(strings.Split(line, "\n")[0], "错") {
		t.Errorf("heads-up calls it wrong: %s", line)
	}
}

func TestDocPickNIsCappedAtTenAndSourceErrorsAreExplicit(t *testing.T) {
	e, _, ck := contractEnv(t, opts{auto: []string{practiceSrc, "no-such-source", "ambiguous-source"}})
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=15&mode=due_first")
	if code != 200 || e.brain.pickQuery[0].Get("n") != "10" || !strings.Contains(a.Note, "最多 10 题") {
		t.Fatalf("%d %s %v", code, b, e.brain.pickQuery)
	}
	code, _, b = e.autoNew(t, ck, "source=no-such-source&n=3&mode=wrong_only")
	if code != 404 || !strings.Contains(string(b), `"error":"pick_source_not_found"`) || strings.Contains(string(b), "接口未就绪") {
		t.Errorf("a source the brain does not know is not 'not ready': %d %s", code, b)
	}
	code, _, b = e.autoNew(t, ck, "source=ambiguous-source&n=3&mode=wrong_only")
	if code != 400 || !strings.Contains(string(b), `"error":"pick_source_ambiguous"`) {
		t.Errorf("%d %s", code, b)
	}
	e.brain.setPickMode("off-json") // the catch-all route 404: the pick endpoint is not deployed
	code, _, b = e.autoNew(t, ck, "source="+practiceSrc+"&n=3&mode=wrong_only")
	if code != 501 || !strings.Contains(string(b), "大脑接口未就绪") {
		t.Errorf("%d %s", code, b)
	}
}
