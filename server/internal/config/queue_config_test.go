package config

import (
	"strings"
	"testing"
	"time"
)

func queueEnvMap(t *testing.T) map[string]string {
	m := valid(t)
	m["RUYI_BRAIN_GRADE_TOKEN"] = "write-token-2"
	m["RUYI_BRAIN_NOTIFY_TOKEN"] = "notify-token-3"
	m["RUYI_NTFY_TOPIC"] = "a-topic-value"
	return m
}

func TestQueueDefaultsAreOffAndSafe(t *testing.T) {
	c, err := Load(env(valid(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.QueueEnabled || c.QueueNotifyEnabled || c.BrainNotifyToken != "" || c.QueuePoll != 45*time.Second || c.QueueStateDir != "/var/lib/ruyi-site/queue" || c.BrainPendingPath != "" {
		t.Fatalf("%+v", c)
	}
}

func TestQueueEnvParsingAndSecrets(t *testing.T) {
	m := queueEnvMap(t)
	m["RUYI_QUEUE_ENABLED"], m["RUYI_QUEUE_NOTIFY_ENABLED"], m["RUYI_QUEUE_POLL_SECONDS"] = "1", "1", "90"
	m["RUYI_QUEUE_STATE_DIR"] = "/srv/queue"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !c.QueueEnabled || !c.QueueNotifyEnabled || c.QueuePoll != 90*time.Second || c.QueueStateDir != "/srv/queue" {
		t.Fatalf("%+v", c)
	}
	found := false
	for _, s := range c.Secrets() {
		found = found || s == "notify-token-3"
	}
	if !found {
		t.Error("the notify token must be redacted from logs")
	}
}

func TestQueueThreeIdenticalTokensRefuseStart(t *testing.T) {
	m := valid(t)
	m["RUYI_BRAIN_GRADE_TOKEN"] = m["RUYI_BRAIN_TOKEN"]
	m["RUYI_BRAIN_NOTIFY_TOKEN"] = m["RUYI_BRAIN_TOKEN"]
	m["RUYI_QUEUE_ENABLED"] = "1"
	_, err := Load(env(m))
	if err == nil || !strings.Contains(err.Error(), "RUYI_BRAIN_NOTIFY_TOKEN") || !strings.Contains(err.Error(), "RUYI_BRAIN_GRADE_TOKEN") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), m["RUYI_BRAIN_TOKEN"]) {
		t.Error("the error message must not contain the token")
	}
}

func TestQueueTokensMustPairwiseDiffer(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"notify==read":  {"RUYI_BRAIN_NOTIFY_TOKEN": "token-value-123"},
		"notify==write": {"RUYI_BRAIN_NOTIFY_TOKEN": "write-token-2"},
	} {
		m := queueEnvMap(t)
		for k, v := range set {
			m[k] = v
		}
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestQueueEnvRejects(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"enabled without write token":  {"RUYI_QUEUE_ENABLED": "1", "RUYI_BRAIN_GRADE_TOKEN": ""},
		"notify without queue":         {"RUYI_QUEUE_NOTIFY_ENABLED": "1"},
		"notify without token":         {"RUYI_QUEUE_ENABLED": "1", "RUYI_QUEUE_NOTIFY_ENABLED": "1", "RUYI_BRAIN_NOTIFY_TOKEN": ""},
		"notify without topic":         {"RUYI_QUEUE_ENABLED": "1", "RUYI_QUEUE_NOTIFY_ENABLED": "1", "RUYI_NTFY_TOPIC": ""},
		"poll below 40 s":              {"RUYI_QUEUE_POLL_SECONDS": "39"},
		"poll above 600 s":             {"RUYI_QUEUE_POLL_SECONDS": "601"},
		"poll not a number":            {"RUYI_QUEUE_POLL_SECONDS": "fast"},
		"flag not 0/1":                 {"RUYI_QUEUE_ENABLED": "yes"},
		"relative state dir":           {"RUYI_QUEUE_STATE_DIR": "queue"},
		"whitespace in notify token":   {"RUYI_BRAIN_NOTIFY_TOKEN": "a b"},
		"pending path without a slash": {"RUYI_BRAIN_PENDING_PATH": "pending"},
	} {
		m := queueEnvMap(t)
		for k, v := range set {
			m[k] = v
		}
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	m := queueEnvMap(t)
	m["RUYI_QUEUE_POLL_SECONDS"] = "40"
	if _, err := Load(env(m)); err != nil {
		t.Errorf("poll 40 s is the minimum and must be accepted: %v", err)
	}
}
