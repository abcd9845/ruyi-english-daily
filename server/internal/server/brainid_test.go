package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abcd9845/ruyi-english-daily/server/internal/config"
)

// Tests for the configurable brain id (RUYI_BRAIN_ID). The fake brain serves exactly one
// brain id (like a real brain token scoped to one brain) and answers 400/404 for any other.

// cfgBrainID loads the real configuration with RUYI_BRAIN_ID=v (unset when v == "") and
// returns the BrainID the service would hand to brain.New.
func cfgBrainID(t *testing.T, v string) string {
	t.Helper()
	m := map[string]string{
		"RUYI_STATIC_DIR":     t.TempDir(),
		"RUYI_FAMILY_CODE":    "family-code-xyz",
		"RUYI_SESSION_SECRET": "0123456789abcdef0123456789abcdef",
		"RUYI_BRAIN_BASE_URL": "http://127.0.0.1:1",
		"RUYI_BRAIN_TOKEN":    "token-value-123",
	}
	if v != "" {
		m["RUYI_BRAIN_ID"] = v
	}
	c, err := config.Load(func(k string) string { return m[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c.BrainID
}

func brainIDEnv(t *testing.T, cfgVal, fakeServes string) (*env, string) {
	t.Helper()
	e, _, ck := contractEnv(t, opts{auto: []string{practiceSrc}, brainID: cfgBrainID(t, cfgVal), fakeBrainID: fakeServes})
	return e, ck
}

// exercise runs one pick, one set grade (which does a lookup first) and returns what the
// fake brain saw.
func exerciseBrainID(t *testing.T, e *env, ck, extraSiteQuery string) {
	t.Helper()
	code, _, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&mode=due_first"+extraSiteQuery)
	if code != 200 {
		t.Fatalf("pick: %d %s", code, b)
	}
	resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q1+`|["1","2","3"]`, q2+`|"B"`))
	if resp.StatusCode != 200 || g.Mode != "graded" || g.RecordedCount != 2 {
		t.Fatalf("grade: %d %s", resp.StatusCode, b)
	}
}

func TestBrainIDConfiguredIsSentToGradeAndPickNotToLookup(t *testing.T) {
	e, ck := brainIDEnv(t, "qa-practice", "qa-practice")
	exerciseBrainID(t, e, ck, "")

	if got := e.brain.lastGradeReq()["brain"]; got != "qa-practice" {
		t.Errorf("grade body brain = %v", got)
	}
	if q := e.brain.lastGradeQuery(); q != "" {
		t.Errorf("grade must not carry a query string, got %q", q)
	}
	pq := e.brain.pickQuery[0]
	if pq.Get("brain") != "qa-practice" || len(pq["brain"]) != 1 {
		t.Errorf("pick query: %v", pq)
	}
	if e.brain.count("lookup") == 0 || len(e.brain.lookupReqs) == 0 {
		t.Fatal("expected lookup calls")
	}
	for _, body := range e.brain.lookupReqs { // lookup has no brain parameter: body = {"qrefs":[...]} only, and no query string
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		if len(m) != 1 || m["qrefs"] == nil || strings.Contains(string(body), "qa-practice") || strings.Contains(string(body), `"brain"`) {
			t.Errorf("lookup body must carry qrefs only: %s", body)
		}
	}
	// the fake records a lookup body only when the request had no query string (else it answers 400)
	if e.brain.count("lookup") != len(e.brain.lookupReqs) {
		t.Error("a lookup carried a query string")
	}
}

func TestBrainIDUnsetKeepsRuyi(t *testing.T) {
	e, ck := brainIDEnv(t, "", "")
	exerciseBrainID(t, e, ck, "")
	if got := e.brain.lastGradeReq()["brain"]; got != "ruyi" {
		t.Errorf("grade body brain = %v", got)
	}
	if got := e.brain.pickQuery[0].Get("brain"); got != "ruyi" {
		t.Errorf("pick brain = %q", got)
	}
	for _, body := range e.brain.lookupReqs {
		if strings.Contains(string(body), `"brain"`) {
			t.Errorf("lookup body: %s", body)
		}
	}
}

// A brain id (or any other) parameter on the site's own URLs never reaches the brain.
func TestBrainIDOnSiteURLIsIgnored(t *testing.T) {
	for _, tc := range []struct{ cfg, fake string }{{"qa-practice", "qa-practice"}, {"", ""}} {
		e, ck := brainIDEnv(t, tc.cfg, tc.fake)
		want := tc.cfg
		if want == "" {
			want = "ruyi"
		}
		// extra brain= on /api/practice/new
		exerciseBrainID(t, e, ck, "&brain=evil&brain=ruyi")
		if got := e.brain.pickQuery[0]["brain"]; len(got) != 1 || got[0] != want {
			t.Errorf("cfg=%q: pick brain = %v", tc.cfg, got)
		}
		// and on the grade URL
		resp, b := e.do(t, "POST", "/api/practice/demo-1/grade?brain=evil", ans(cid2, q2+`|"B"`), map[string]string{"Content-Type": "application/json"}, ck)
		if resp.StatusCode != 200 {
			t.Fatalf("grade with extra brain: %d %s", resp.StatusCode, b)
		}
		if got := e.brain.lastGradeReq()["brain"]; got != want {
			t.Errorf("cfg=%q: grade body brain = %v after an extra brain on the site URL", tc.cfg, got)
		}
		if q := e.brain.lastGradeQuery(); q != "" {
			t.Errorf("cfg=%q: grade query reached the brain: %q", tc.cfg, q)
		}
	}
}

// Scope mismatch: a site left on the default "ruyi" against a brain that serves only
// "qa-practice" (or the reverse) is refused by the brain and never claims a brain write.
func TestBrainIDMismatchIsRefusedByTheBrain(t *testing.T) {
	for _, tc := range []struct{ cfg, fake string }{{"", "qa-practice"}, {"qa-practice", ""}} {
		e, ck := brainIDEnv(t, tc.cfg, tc.fake)
		if code, _, _ := e.autoNew(t, ck, "source="+practiceSrc+"&n=3&mode=due_first"); code == 200 {
			t.Errorf("cfg=%q fake=%q: pick for a foreign brain must not succeed", tc.cfg, tc.fake)
		}
		resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q2+`|"B"`))
		if resp.StatusCode == 200 && (g.Mode == "graded" || g.RecordedCount > 0) {
			t.Errorf("cfg=%q fake=%q: grade must not be recorded: %s", tc.cfg, tc.fake, b)
		}
	}
}

// ---- pick-practice token: the brain wants the target brain's write scope (the grade token) ----

func pickURL(mode string) string { return "source=" + practiceSrc + "&n=3&mode=" + mode }

func TestPickUsesTheGradeTokenAndTheBrainAcceptsIt(t *testing.T) {
	e, _, ck := contractEnv(t, opts{auto: []string{practiceSrc}})
	if code, _, b := e.autoNew(t, ck, pickURL("due_first")); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if len(e.brain.pickAuths) != 1 || e.brain.pickAuths[0] != "Bearer "+e.brain.gradeToken || e.brain.pickBank != 0 {
		t.Errorf("auths=%v bankReads=%d", e.brain.pickAuths, e.brain.pickBank)
	}
	// regression: lookup keeps the read token, grade keeps the grade token
	resp, b, g := e.submit(t, ck, "demo-1", ans(cid1, q2+`|"B"`))
	if resp.StatusCode != 200 || g.Mode != "graded" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if e.brain.count("lookup") == 0 || e.brain.gradeBank != 0 || e.brain.gradeAuths[len(e.brain.gradeAuths)-1] != "Bearer "+e.brain.gradeToken {
		t.Error("lookup/grade token regression")
	}
}

// A grade token without the brain's write scope (simulated by handing the site the read token)
// is refused by the brain with 403: a clear 502 pick_forbidden, no fake pick, no leak.
func TestPickForbiddenIsAClearError(t *testing.T) {
	n := newNtfyStub(t)
	e := newEnv(t, opts{sets: contractSets, grade: true, ntfyURL: n.url(), auto: []string{practiceSrc}, gradeAsRead: true})
	ck := e.session(t)
	for _, mode := range []string{"due_first", "wrong_only"} {
		resp, b := e.do(t, "GET", "/api/practice/new?"+pickURL(mode), "", nil, ck)
		if resp.StatusCode != 502 {
			t.Fatalf("%s: %d %s", mode, resp.StatusCode, b)
		}
		var m map[string]string
		must(t, json.Unmarshal(b, &m))
		if m["error"] != "pick_forbidden" || !strings.Contains(m["message"], "选题令牌无权限") {
			t.Errorf("%s: %s", mode, b)
		}
		for _, tok := range []string{e.brain.token, e.brain.gradeToken} {
			if strings.Contains(string(b), tok) || strings.Contains(e.logs.String(), tok) {
				t.Error("a token leaked")
			}
		}
	}
	if e.brain.pickBank != 2 {
		t.Errorf("fake saw %d read-token picks", e.brain.pickBank)
	}
	// the legacy random pick (no mode) does not call pick-practice and is unaffected
	if code, _, b := e.autoNew(t, ck, "source="+practiceSrc+"&n=2"); code != 200 {
		t.Errorf("random pick: %d %s", code, b)
	}
}

// No grade token configured: pick is "not ready" (501, same shape as the not-deployed case),
// the brain receives no pick request at all, and the read token is never used instead.
func TestPickWithoutGradeTokenIsNotReady(t *testing.T) {
	e := newEnv(t, opts{auto: []string{practiceSrc}}) // grade: false
	ck := e.session(t)
	for mode, code := range map[string]string{"due_first": "due_first_unavailable", "wrong_only": "wrong_only_unavailable"} {
		resp, b := e.do(t, "GET", "/api/practice/new?"+pickURL(mode), "", nil, ck)
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		if resp.StatusCode != 501 || m["error"] != code || !strings.Contains(m["message"], "大脑接口未就绪") || !strings.Contains(m["message"], "判分令牌") {
			t.Errorf("%s: %d %s", mode, resp.StatusCode, b)
		}
		if strings.Contains(string(b), e.brain.token) || strings.Contains(e.logs.String(), e.brain.token) {
			t.Error("read token leaked")
		}
	}
	if e.brain.count("pick") != 0 || len(e.brain.pickAuths) != 0 || e.brain.pickBank != 0 {
		t.Errorf("the brain got pick requests: %d / %v", e.brain.count("pick"), e.brain.pickAuths)
	}
}
