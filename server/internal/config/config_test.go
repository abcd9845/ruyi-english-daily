package config

import (
	"os"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid(t *testing.T) map[string]string {
	return map[string]string{
		"RUYI_STATIC_DIR":     t.TempDir(),
		"RUYI_FAMILY_CODE":    "family-code-xyz",
		"RUYI_SESSION_SECRET": "0123456789abcdef0123456789abcdef",
		"RUYI_BRAIN_BASE_URL": "https://127.0.0.1:18080",
		"RUYI_BRAIN_TOKEN":    "token-value-123",
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(valid(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":18082" || c.SessionTTL.Hours() != 720 || c.CookieSecure || c.BrainInsecure {
		t.Fatalf("defaults: %+v", c)
	}
	if len(c.AllowedSource) != 1 || c.AllowedSource[0] != DefaultAllowedSource {
		t.Fatalf("sources: %v", c.AllowedSource)
	}
}

func TestMissingRequiredNamesOnly(t *testing.T) {
	for _, k := range []string{"RUYI_STATIC_DIR", "RUYI_FAMILY_CODE", "RUYI_SESSION_SECRET", "RUYI_BRAIN_BASE_URL", "RUYI_BRAIN_TOKEN"} {
		m := valid(t)
		delete(m, k)
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s: err=%v", k, err)
			continue
		}
		for name, v := range m {
			if name != "RUYI_STATIC_DIR" && strings.Contains(err.Error(), v) {
				t.Errorf("error leaks value of %s", name)
			}
		}
	}
}

func TestErrorsNeverContainValues(t *testing.T) {
	m := valid(t)
	m["RUYI_FAMILY_CODE"] = "zq9a"                // too short
	m["RUYI_SESSION_SECRET"] = "zq9b"             // too short
	m["RUYI_BRAIN_TOKEN"] = "has space LEAKTOKEN" // whitespace
	m["RUYI_COOKIE_SECURE"] = "yes-LEAKFLAG"
	m["RUYI_NTFY_TOPIC"] = "bad topic!LEAKTOPIC"
	m["RUYI_BRAIN_BASE_URL"] = "https://user:LEAKPASS@host:1/x?k=LEAKQ"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, leak := range []string{"zq9a", "zq9b", "LEAK", "has space", "yes-"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}
}

func TestInsecureTLSRules(t *testing.T) {
	m := valid(t)
	m["RUYI_BRAIN_INSECURE_TLS"] = "1"
	if _, err := Load(env(m)); err != nil {
		t.Fatalf("loopback https must be allowed: %v", err)
	}
	m["RUYI_BRAIN_BASE_URL"] = "https://brain.example.com"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("non-loopback must be rejected")
	}
	m["RUYI_BRAIN_BASE_URL"] = "http://127.0.0.1:18080"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("http with insecure flag must be rejected")
	}
	m = valid(t)
	m["RUYI_BRAIN_INSECURE_TLS"] = "1"
	f := t.TempDir() + "/ca.pem"
	_ = os.WriteFile(f, []byte("x"), 0o600)
	m["RUYI_BRAIN_CA_FILE"] = f
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("both set must fail: %v", err)
	}
}

func TestOtherValidation(t *testing.T) {
	for k, v := range map[string]string{
		"RUYI_LISTEN": "nonsense", "RUYI_SESSION_TTL": "forever", "RUYI_LOG_LEVEL": "loud",
		"RUYI_BRAIN_BASE_URL": "ftp://x", "RUYI_ALLOWED_SOURCES": "ok.one,Bad/One", "RUYI_STATIC_DIR": "/nonexistent-dir-xyz",
	} {
		m := valid(t)
		m[k] = v
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s=%q accepted", k, v)
		}
	}
	m := valid(t)
	m["RUYI_COOKIE_SECURE"] = "1"
	m["RUYI_SESSION_TTL"] = "24h"
	m["RUYI_LOG_LEVEL"] = "debug"
	m["RUYI_NTFY_TOPIC"] = "abc_DEF-123"
	c, err := Load(env(m))
	if err != nil || !c.CookieSecure || c.SessionTTL.Hours() != 24 || c.NtfyTopic != "abc_DEF-123" {
		t.Fatalf("%v %+v", err, c)
	}
	if len(c.Secrets()) != 4 {
		t.Fatalf("secrets: %d", len(c.Secrets()))
	}
}

func TestFamilyCodeMinLength(t *testing.T) {
	// 5 characters pass; 4 fail, and the error never echoes the value.
	for _, ok := range []string{"a1b2c", "测试家庭码五", "12345"} {
		m := valid(t)
		m["RUYI_FAMILY_CODE"] = ok
		if _, err := Load(env(m)); err != nil {
			t.Errorf("len %d should pass: %v", len([]rune(ok)), err)
		}
	}
	for _, bad := range []string{"qz9k", "测试家庭", "1234", "x"} {
		m := valid(t)
		m["RUYI_FAMILY_CODE"] = bad
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "RUYI_FAMILY_CODE") || !strings.Contains(err.Error(), "min 5") {
			t.Errorf("%d-char code must fail naming the variable and min 5: %v", len([]rune(bad)), err)
			continue
		}
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes the code value: %v", err)
		}
	}
	m := valid(t)
	m["RUYI_FAMILY_CODE"] = ""
	if _, err := Load(env(m)); err == nil {
		t.Error("empty must fail")
	}
}
