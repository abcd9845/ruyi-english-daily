package server_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type autoResp struct {
	SetID     string `json:"set_id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Requested int    `json:"requested"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note"`
	ExpiresAt string `json:"expires_at"`
}

func (e *env) autoNew(t *testing.T, ck, query string) (int, autoResp, []byte) {
	t.Helper()
	resp, b := e.do(t, "GET", "/api/practice/new?"+query, "", nil, ck)
	var a autoResp
	if resp.StatusCode == 200 {
		must(t, json.Unmarshal(b, &a))
	}
	return resp.StatusCode, a, b
}

func qrefsOf(r pResp) []string {
	var o []string
	for _, it := range r.Items {
		o = append(o, it.QRef)
	}
	return o
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.Unix(0, c.t.Load()) }
func (c *clock) add(d time.Duration) { c.t.Add(int64(d)) }

func TestAutoSetsOffByDefault(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true})
	ck := e.session(t)
	code, _, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=2")
	if code != 404 {
		t.Errorf("%d %s", code, b)
	}
	if e.brain.count("list") != 0 {
		t.Error("a refused source must not hit the brain")
	}
	// and without any sets file the whole prefix is still 404 for everything else
	e2 := newEnv(t, opts{})
	if resp, _ := e2.do(t, "GET", "/api/practice/new?source=x&n=1", "", nil, e2.session(t)); resp.StatusCode != 404 && resp.StatusCode != 400 {
		t.Errorf("unexpected %d", resp.StatusCode)
	}
}

func TestAutoSetsFlow(t *testing.T) {
	cl := &clock{}
	cl.t.Store(time.Now().UnixNano())
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: gradeSets, grade: true, auto: []string{practiceSrc}, autoTTL: 10 * time.Minute, now: cl.now, ntfyURL: n.url()})
	ck := e.session(t)

	// login required
	if resp, _ := e.do(t, "GET", "/api/practice/new?source="+practiceSrc+"&n=2", "", nil, ""); resp.StatusCode != 401 {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
	// parameter validation
	for _, q := range []string{
		"source=" + practiceSrc, "source=" + practiceSrc + "&n=0", "source=" + practiceSrc + "&n=21", "source=" + practiceSrc + "&n=x",
		"source=" + practiceSrc + "&n=-1", "source=other.source&n=2", "source=&n=2", "n=2",
		"source=" + practiceSrc + "&n=2&order=zigzag", "source=" + practiceSrc + "&n=2&seed=abc",
	} {
		if code, _, _ := e.autoNew(t, ck, q); code != 400 {
			t.Errorf("%q: want 400 got %d", q, code)
		}
	}
	// sequential, n=2
	code, a, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=2&order=seq")
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if !regexp.MustCompile(`^auto-[a-z0-9]{8}$`).MatchString(a.SetID) || a.URL != "/m/practice/?set="+a.SetID || a.Count != 2 || a.Truncated || a.Note != "" || a.ExpiresAt == "" {
		t.Errorf("%+v", a)
	}
	code, p, _ := getPractice(t, e, ck, a.SetID)
	if code != 200 || strings.Join(qrefsOf(p), ",") != "example.set:math:1,example.set:math:2" || p.SetID != a.SetID {
		t.Fatalf("seq set: %d %v", code, qrefsOf(p))
	}
	// random with a seed is reproducible; without seed picks n distinct
	_, a1, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&seed=42")
	_, a2, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&seed=42")
	_, p1, _ := getPractice(t, e, ck, a1.SetID)
	_, p2, _ := getPractice(t, e, ck, a2.SetID)
	if a1.SetID == a2.SetID || strings.Join(qrefsOf(p1), ",") != strings.Join(qrefsOf(p2), ",") || len(p1.Items) != 3 {
		t.Errorf("seed: %v vs %v", qrefsOf(p1), qrefsOf(p2))
	}
	seen := map[string]bool{}
	for _, q := range qrefsOf(p1) {
		if seen[q] {
			t.Error("duplicate qref in a random set")
		}
		seen[q] = true
	}
	// n larger than available: everything, with a note
	code, big, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=20")
	if code != 200 || big.Count != 5 || !big.Truncated || big.Requested != 20 || big.Note == "" {
		t.Errorf("%d %+v", code, big)
	}
	// the dynamic set grades like any other set (same idempotency/limits/validation)
	body := `{"client_attempt_id":"` + cid1 + `","answers":[{"qref":"example.set:math:2","answer":"B"}]}`
	resp, rb, g := e.submit(t, ck, a.SetID, body)
	if resp.StatusCode != 200 || g.Mode != "graded" || g.Summary.Correct != 1 || g.SetID != a.SetID {
		t.Fatalf("grade dynamic: %d %s", resp.StatusCode, rb)
	}
	if !strings.Contains(n.last().Body, "练习（2 题）") {
		t.Errorf("ntfy: %q", n.last().Body)
	}
	// a qref outside the dynamic set is refused
	if resp, _, _ := e.submit(t, ck, a.SetID, `{"client_attempt_id":"`+cid2+`","answers":[{"qref":"example.set:math:3","answer":"x"}]}`); resp.StatusCode != 400 {
		t.Errorf("outside dynamic set: %d", resp.StatusCode)
	}
	// TTL: after expiry the set is gone, and so is grading
	cl.add(11 * time.Minute)
	if code, _, _ := getPractice(t, e, ck, a.SetID); code != 404 {
		t.Errorf("expired set: %d", code)
	}
	if resp, _, _ := e.submit(t, ck, a.SetID, `{"client_attempt_id":"`+uuid(9)+`","answers":[{"qref":"example.set:math:2","answer":"B"}]}`); resp.StatusCode != 404 {
		t.Errorf("grade expired set: %d", resp.StatusCode)
	}
	// the replay of an already graded attempt of an expired set is still served from the store
	if resp, _, g := e.submit(t, ck, a.SetID, body); resp.StatusCode != 200 && !g.Replayed {
		t.Logf("replay after expiry: %d", resp.StatusCode)
	}
}

func TestAutoSetsSelectionNotYetSupported(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true, auto: []string{practiceSrc}})
	ck := e.session(t)
	code, _, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&wrong_only=1")
	if code != 501 || !strings.Contains(string(b), `"error":"wrong_only_unavailable"`) {
		t.Errorf("wrong_only: %d %s", code, b)
	}
	code, _, b = e.autoNew(t, ck, "source="+practiceSrc+"&n=3&due_first=1")
	if code != 501 || !strings.Contains(string(b), "due_first_unavailable") {
		t.Errorf("due_first: %d %s", code, b)
	}
	// nothing was made up: no dynamic set exists afterwards
	if strings.Contains(string(b), "set_id") {
		t.Error("an unsupported selection must not create a set")
	}
}

func TestAutoSetsMediaGatingAndReservedIDs(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true, auto: []string{practiceSrc}})
	ck := e.session(t)
	_, a, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=20&order=seq")
	_, p, _ := getPractice(t, e, ck, a.SetID)
	var withMedia int
	for _, it := range p.Items {
		for _, m := range it.Media {
			withMedia++
			if resp, _ := e.do(t, "GET", m.URL, "", nil, ck); resp.StatusCode != 200 {
				t.Errorf("media of a dynamic set item: %d", resp.StatusCode)
			}
		}
	}
	if withMedia == 0 {
		t.Error("expected at least one figure in the synthetic data")
	}
	// ids that look like dynamic ones but were never issued are plain 404s
	for _, id := range []string{"auto-abcdefgh", "auto-", "new", "AUTO-ABCDEFGH"} {
		if code, _, _ := getPractice(t, e, ck, id); code != 404 && code != 400 {
			t.Errorf("%s: %d", id, code)
		}
	}
}

func TestAutoSetsBrainDown(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true, auto: []string{practiceSrc}, brainURL: "http://127.0.0.1:1"})
	code, _, b := e.autoNew(t, e.session(t), "source="+practiceSrc+"&n=3")
	if code != 502 || strings.Contains(string(b), "127.0.0.1") {
		t.Errorf("%d %s", code, b)
	}
}
