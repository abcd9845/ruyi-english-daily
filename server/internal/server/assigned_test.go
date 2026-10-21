package server_test

import (
	"strings"
	"testing"
)

// 大脑 PR #92: pick-practice?assigned=1 puts open assignments (possibly of another source) first.
func TestAutoSetsAssignedFirstWithFigure(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true, auto: []string{practiceSrc}})
	ck := e.session(t)

	// no assignment: the pick is unchanged, but assigned=1 is always sent
	code, a0, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&mode=due_first")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	_, p0, _ := getPractice(t, e, ck, a0.SetID)
	e.brain.mu.Lock()
	lastQ := e.brain.pickQuery[len(e.brain.pickQuery)-1]
	e.brain.mu.Unlock()
	if lastQ.Get("assigned") != "1" {
		t.Errorf("assigned not sent: %v", lastQ)
	}
	if got := strings.Join(qrefsOf(p0), ","); got != "example.set:math:2,example.set:math:1,example.set:math:3" {
		t.Errorf("no-assignment pick changed: %s", got)
	}

	// one assignment from another source (with a figure) plus one the bank does not know
	e.brain.mu.Lock()
	e.brain.assigned = []string{"math:q01", "nope:gone:1"}
	e.brain.mu.Unlock()
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&mode=due_first")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	code, p, pb := getPractice(t, e, ck, a.SetID)
	if code != 200 || len(p.Items) == 0 || p.Items[0].QRef != "math:q01" || p.Items[0].Origin != "assigned" {
		t.Fatalf("assigned not first: %d %s", code, pb)
	}
	for _, q := range qrefsOf(p) {
		if q == "nope:gone:1" {
			t.Error("unresolvable assignment kept")
		}
	}
	if len(p.Items[0].Media) != 1 || !strings.Contains(p.Items[0].Media[0].URL, "/api/media/"+testSource+"/") {
		t.Fatalf("figure of the assigned item: %+v", p.Items[0].Media)
	}
	if resp, _ := e.do(t, "GET", p.Items[0].Media[0].URL, "", nil, ck); resp.StatusCode != 200 {
		t.Errorf("figure fetch: %d", resp.StatusCode)
	}
	// the dynamic set still refuses figures of that source outside the assigned qref
	if resp, _ := e.do(t, "GET", "/api/media/"+testSource+"/"+strings.Repeat("2", 64), "", nil, ck); resp.StatusCode == 200 {
		t.Error("foreign figure served")
	}
}
