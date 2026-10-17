package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGradeDefaultsAreSafe(t *testing.T) {
	c, err := Load(env(valid(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.BrainGradeToken != "" || c.BrainGradePath != "" || c.BrainLookupPath != "" || c.BrainPickPath != "" || c.SkipDeferred || c.AttemptsDir != "/var/lib/ruyi-site/attempts" || c.GradeMaxPerHour != 5 ||
		len(c.AutoSources) != 0 || c.NtfyBaseURL != "https://ntfy.sh/" || c.NtfyRetryDelay != time.Minute || c.AutoTTL != 6*time.Hour {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestGradeEnvParsing(t *testing.T) {
	m := valid(t)
	m["RUYI_BRAIN_GRADE_TOKEN"] = "grade-token-1"
	m["RUYI_BRAIN_GRADE_PATH"] = "/v2/attempts/grade"
	m["RUYI_BRAIN_LOOKUP_PATH"] = "/v2/lookup"
	m["RUYI_BRAIN_PICK_PATH"] = "/v2/pick"
	m["RUYI_PICK_SKIP_DEFERRED"] = "1"
	m["RUYI_ATTEMPTS_DIR"] = "/srv/attempts"
	m["RUYI_GRADE_MAX_PER_HOUR"] = "9"
	m["RUYI_NTFY_BASE_URL"] = "http://127.0.0.1:9999"
	m["RUYI_NTFY_RETRY_DELAY"] = "250ms"
	m["RUYI_AUTO_SOURCES"] = "src.one, src.two"
	m["RUYI_AUTO_TTL"] = "30m"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.BrainGradeToken != "grade-token-1" || c.BrainGradePath != "/v2/attempts/grade" || c.BrainLookupPath != "/v2/lookup" || c.BrainPickPath != "/v2/pick" || !c.SkipDeferred || c.AttemptsDir != "/srv/attempts" || c.GradeMaxPerHour != 9 ||
		c.NtfyBaseURL != "http://127.0.0.1:9999/" || c.NtfyRetryDelay != 250*time.Millisecond || strings.Join(c.AutoSources, ",") != "src.one,src.two" || c.AutoTTL != 30*time.Minute {
		t.Fatalf("%+v", c)
	}
	found := false
	for _, s := range c.Secrets() {
		if s == "grade-token-1" {
			found = true
		}
	}
	if !found {
		t.Error("the grade token must be in Secrets() so it is redacted from logs")
	}
}

func TestGradeEnvRejects(t *testing.T) {
	for k, v := range map[string]string{
		"RUYI_BRAIN_GRADE_TOKEN":  "has space",
		"RUYI_BRAIN_GRADE_PATH":   "v1/grade",
		"RUYI_BRAIN_LOOKUP_PATH":  "lookup",
		"RUYI_BRAIN_PICK_PATH":    "/a/../b",
		"RUYI_PICK_SKIP_DEFERRED": "yes",
		"RUYI_ATTEMPTS_DIR":       "relative/dir",
		"RUYI_GRADE_MAX_PER_HOUR": "0",
		"RUYI_NTFY_BASE_URL":      "ftp://x/",
		"RUYI_NTFY_RETRY_DELAY":   "1ms",
		"RUYI_AUTO_SOURCES":       "ok.src,BAD SRC",
		"RUYI_AUTO_TTL":           "0s",
	} {
		m := valid(t)
		m[k] = v
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), k) || len(v) > 3 && strings.Contains(err.Error(), v) {
			t.Errorf("%s: %v", k, err)
		}
	}
	m := valid(t)
	m["RUYI_BRAIN_GRADE_TOKEN"] = m["RUYI_BRAIN_TOKEN"]
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "RUYI_BRAIN_GRADE_TOKEN") || strings.Contains(err.Error(), m["RUYI_BRAIN_TOKEN"]) {
		t.Errorf("grade token equal to the read token: %v", err)
	}
}

func TestNtfyBaseURLTrailingSlash(t *testing.T) {
	for _, in := range []string{"https://ntfy-me.com", "https://ntfy-me.com/"} {
		m := valid(t)
		m["RUYI_NTFY_BASE_URL"] = in
		c, err := Load(env(m))
		if err != nil {
			t.Fatal(err)
		}
		if c.NtfyBaseURL != "https://ntfy-me.com/" {
			t.Errorf("%q -> %q", in, c.NtfyBaseURL)
		}
	}
}

// The source reserved for local test sets can never be a brain source:
// not allowed for the old /api/sets page, not auto-pickable.
func TestReservedLocalSourceRefused(t *testing.T) {
	for _, name := range []string{"RUYI_AUTO_SOURCES", "RUYI_ALLOWED_SOURCES"} {
		for _, v := range []string{"local.smoke", "src.one,local.smoke", "local.smoke, src.one"} {
			m := valid(t)
			m[name] = v
			_, err := Load(env(m))
			if err == nil {
				t.Errorf("%s=%q must be refused", name, v)
				continue
			}
			if !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), "local.smoke") {
				t.Errorf("%s: error must name the variable and not echo the value: %v", name, err)
			}
		}
		m := valid(t)
		m[name] = "src.one,local.smoked" // a different key is fine
		if _, err := Load(env(m)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A bad local set in RUYI_SETS_FILE stops the service at startup, naming only the variable and the position.
func TestSetsFileLocalSetFailsAtLoad(t *testing.T) {
	dir := t.TempDir()
	good := `{"id":"smoke-test","local":true,"title":"T","questions":[{"qref":"smoke:q1","qtype":"single_choice","stem":"s","options":[{"key":"A","text":"1"},{"key":"B","text":"2"}],"answer":["B"]}]}`
	try := func(body string) error {
		p := filepath.Join(dir, "s.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m := valid(t)
		m["RUYI_SETS_FILE"] = p
		_, err := Load(env(m))
		return err
	}
	if err := try(`{"sets":[` + good + `]}`); err != nil {
		t.Fatalf("good local set: %v", err)
	}
	for name, body := range map[string]string{
		"wrong id":   strings.Replace(good, `"smoke-test"`, `"demo-9"`, 1),
		"no qs":      `{"id":"smoke-test","local":true,"title":"T"}`,
		"with qrefs": strings.Replace(good, `"local":true,`, `"local":true,"qrefs":["smoke:q1"],`, 1),
		"bad qref":   strings.Replace(good, `smoke:q1`, `SECRET-QREF-MARK`, 1),
	} {
		err := try(`{"sets":[` + body + `]}`)
		if err == nil || !strings.Contains(err.Error(), "RUYI_SETS_FILE") || strings.Contains(err.Error(), "SECRET-QREF-MARK") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
