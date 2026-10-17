package config

import (
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
