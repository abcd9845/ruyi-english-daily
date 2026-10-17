// Package config loads the service configuration from environment variables.
//
// Error messages name the variable and the problem only. They never include
// the variable's value, so a misconfigured secret cannot leak into logs.
package config

import (
	"errors"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultAllowedSource is the only source the page needs in the 18082 trial.
// minFamilyCodeLen is the shortest accepted family code, in characters. The code
// is short by design; protection relies on the per-IP failed-login limiter.
const minFamilyCodeLen = 5

const DefaultAllowedSource = "math-intake.zz-yuekao-numberline-4y"

// Config is the validated runtime configuration.
type Config struct {
	Listen        string
	StaticDir     string
	FamilyCode    string
	SessionSecret string
	SessionTTL    time.Duration
	CookieSecure  bool
	BrainBaseURL  *url.URL
	BrainToken    string
	BrainInsecure bool
	BrainCAFile   string
	LogLevel      slog.Level
	NtfyTopic     string
	AllowedSource []string
}

var (
	topicRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	sourceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{1,127}$`)
)

// SourceRe is the source-key syntax shared with the brain's contract
// (lowercase letters, digits, and _ . : -).
func SourceRe() *regexp.Regexp { return sourceRe }

// Load reads the configuration through getenv (os.Getenv in production).
// All problems are collected so the operator can fix them in one pass.
func Load(getenv func(string) string) (*Config, error) {
	var errs []string
	bad := func(name, why string) { errs = append(errs, name+": "+why) }
	req := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			bad(name, "required but not set")
		}
		return v
	}
	c := &Config{}

	c.Listen = strings.TrimSpace(getenv("RUYI_LISTEN"))
	if c.Listen == "" {
		c.Listen = ":18082"
	} else if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		bad("RUYI_LISTEN", "must be host:port or :port")
	}

	c.StaticDir = req("RUYI_STATIC_DIR")
	if c.StaticDir != "" {
		if st, err := os.Stat(c.StaticDir); err != nil || !st.IsDir() {
			bad("RUYI_STATIC_DIR", "is not an existing directory")
		}
	}

	c.FamilyCode = getenv("RUYI_FAMILY_CODE") // not trimmed: compared verbatim
	if c.FamilyCode == "" {
		bad("RUYI_FAMILY_CODE", "required but not set")
	} else if utf8.RuneCountInString(c.FamilyCode) < minFamilyCodeLen {
		bad("RUYI_FAMILY_CODE", "too short (min "+strconv.Itoa(minFamilyCodeLen)+" characters)")
	}

	c.SessionSecret = req("RUYI_SESSION_SECRET")
	if c.SessionSecret != "" && len(c.SessionSecret) < 16 {
		bad("RUYI_SESSION_SECRET", "too short (min 16 characters)")
	}

	c.SessionTTL = 720 * time.Hour
	if v := strings.TrimSpace(getenv("RUYI_SESSION_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute || d > 8760*time.Hour {
			bad("RUYI_SESSION_TTL", "must be a Go duration between 1m and 8760h")
		} else {
			c.SessionTTL = d
		}
	}

	var ok bool
	if c.CookieSecure, ok = flag01(getenv("RUYI_COOKIE_SECURE")); !ok {
		bad("RUYI_COOKIE_SECURE", "must be 0 or 1")
	}

	raw := req("RUYI_BRAIN_BASE_URL")
	if raw != "" {
		u, err := url.Parse(raw)
		switch {
		case err != nil || u.Host == "" || u.Hostname() == "":
			bad("RUYI_BRAIN_BASE_URL", "not a valid absolute URL")
		case u.Scheme != "http" && u.Scheme != "https":
			bad("RUYI_BRAIN_BASE_URL", "scheme must be http or https")
		case u.User != nil || u.RawQuery != "" || u.Fragment != "":
			bad("RUYI_BRAIN_BASE_URL", "must not contain userinfo, query or fragment")
		default:
			u.Path = strings.TrimRight(u.Path, "/")
			c.BrainBaseURL = u
		}
	}

	c.BrainToken = req("RUYI_BRAIN_TOKEN")
	if strings.ContainsAny(c.BrainToken, " \t\r\n") {
		bad("RUYI_BRAIN_TOKEN", "must not contain whitespace")
	}

	if c.BrainInsecure, ok = flag01(getenv("RUYI_BRAIN_INSECURE_TLS")); !ok {
		bad("RUYI_BRAIN_INSECURE_TLS", "must be 0 or 1")
	}
	c.BrainCAFile = strings.TrimSpace(getenv("RUYI_BRAIN_CA_FILE"))
	if c.BrainInsecure && c.BrainCAFile != "" {
		bad("RUYI_BRAIN_INSECURE_TLS", "cannot be combined with RUYI_BRAIN_CA_FILE (choose one)")
	}
	if c.BrainInsecure && c.BrainBaseURL != nil {
		if c.BrainBaseURL.Scheme != "https" {
			bad("RUYI_BRAIN_INSECURE_TLS", "only meaningful for an https brain address")
		} else if !isLoopback(c.BrainBaseURL.Hostname()) {
			bad("RUYI_BRAIN_INSECURE_TLS", "allowed only when the brain address is loopback")
		}
	}
	if c.BrainCAFile != "" {
		if st, err := os.Stat(c.BrainCAFile); err != nil || st.IsDir() {
			bad("RUYI_BRAIN_CA_FILE", "is not a readable file")
		}
	}

	switch strings.ToLower(strings.TrimSpace(getenv("RUYI_LOG_LEVEL"))) {
	case "", "info":
		c.LogLevel = slog.LevelInfo
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		bad("RUYI_LOG_LEVEL", "must be debug, info, warn or error")
	}

	if t := strings.TrimSpace(getenv("RUYI_NTFY_TOPIC")); t != "" {
		if !topicRe.MatchString(t) {
			bad("RUYI_NTFY_TOPIC", "invalid characters or length")
		} else {
			c.NtfyTopic = t
		}
	}

	c.AllowedSource = []string{DefaultAllowedSource}
	if v := strings.TrimSpace(getenv("RUYI_ALLOWED_SOURCES")); v != "" {
		c.AllowedSource = nil
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if !sourceRe.MatchString(s) {
				bad("RUYI_ALLOWED_SOURCES", "contains an invalid source key")
				break
			}
			c.AllowedSource = append(c.AllowedSource, s)
		}
	}

	if len(errs) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(errs, "; "))
	}
	return c, nil
}

// Secrets lists the values that must never appear in any log line.
func (c *Config) Secrets() []string {
	s := []string{c.FamilyCode, c.SessionSecret, c.BrainToken}
	if c.NtfyTopic != "" {
		s = append(s, c.NtfyTopic)
	}
	return s
}

func flag01(v string) (val, ok bool) {
	switch strings.TrimSpace(v) {
	case "", "0":
		return false, true
	case "1":
		return true, true
	}
	return false, false
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
