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

	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
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
	// BrainID is the "brain" value sent to the brain's grade and pick-practice
	// endpoints (RUYI_BRAIN_ID). Empty = unset: the brain package uses its
	// default, "ruyi". The brain tokens must be scoped to the same brain.
	BrainID       string
	LogLevel      slog.Level
	NtfyTopic     string
	AllowedSource []string
	// Sets is the practice-set registry from RUYI_SETS_FILE (nil when unset).
	Sets *sets.Registry

	// P2/P3: grading, archive, notification, dynamic sets.
	BrainGradeToken string
	BrainGradePath  string
	// BrainLookupPath / BrainPickPath override the contract paths of the batch
	// lookup and pick-practice endpoints (empty = the contract default).
	BrainLookupPath string
	BrainPickPath   string
	// SkipDeferred drops picks flagged deferred:* (answer key pending, unsupported
	// type, figure missing) instead of marking them "参考答案待核，不自动判".
	SkipDeferred    bool
	AttemptsDir     string
	GradeMaxPerHour int
	NtfyBaseURL     string
	NtfyRetryDelay  time.Duration
	AutoSources     []string
	AutoTTL         time.Duration
}

// DefaultAttemptsDir is where attempts are archived unless RUYI_ATTEMPTS_DIR says otherwise.
const DefaultAttemptsDir = "/var/lib/ruyi-site/attempts"

var gradePathRe = regexp.MustCompile(`^/[A-Za-z0-9/_.-]{1,100}$`)

var (
	brainIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	topicRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	sourceRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{1,127}$`)
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

	if v := strings.TrimSpace(getenv("RUYI_BRAIN_ID")); v != "" {
		if !brainIDRe.MatchString(v) {
			bad("RUYI_BRAIN_ID", "invalid (lowercase letters, digits, _ and -; 1 to 64 characters; must start with a letter or digit)")
		} else {
			c.BrainID = v
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
			if s == sets.LocalSource {
				bad("RUYI_ALLOWED_SOURCES", "contains a source reserved for local test sets")
				break
			}
			c.AllowedSource = append(c.AllowedSource, s)
		}
	}

	// ---- grading / archive / notification / dynamic sets (all optional) ----
	c.BrainGradeToken = strings.TrimSpace(getenv("RUYI_BRAIN_GRADE_TOKEN"))
	if strings.ContainsAny(c.BrainGradeToken, " \t\r\n") {
		bad("RUYI_BRAIN_GRADE_TOKEN", "must not contain whitespace")
	}
	if c.BrainGradeToken != "" && c.BrainGradeToken == c.BrainToken {
		bad("RUYI_BRAIN_GRADE_TOKEN", "must be a different token from RUYI_BRAIN_TOKEN")
	}
	pathVar := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v != "" && (!gradePathRe.MatchString(v) || strings.Contains(v, "..") || strings.Contains(v, "//")) {
			bad(name, "must be an absolute URL path like /v1/bank/grade")
		}
		return v
	}
	c.BrainGradePath = pathVar("RUYI_BRAIN_GRADE_PATH")
	c.BrainLookupPath = pathVar("RUYI_BRAIN_LOOKUP_PATH")
	c.BrainPickPath = pathVar("RUYI_BRAIN_PICK_PATH")
	if c.SkipDeferred, ok = flag01(getenv("RUYI_PICK_SKIP_DEFERRED")); !ok {
		bad("RUYI_PICK_SKIP_DEFERRED", "must be 0 or 1")
	}
	c.AttemptsDir = strings.TrimSpace(getenv("RUYI_ATTEMPTS_DIR"))
	if c.AttemptsDir == "" {
		c.AttemptsDir = DefaultAttemptsDir
	} else if !strings.HasPrefix(c.AttemptsDir, "/") || strings.ContainsRune(c.AttemptsDir, 0) {
		bad("RUYI_ATTEMPTS_DIR", "must be an absolute path")
	}
	c.GradeMaxPerHour = 5
	if v := strings.TrimSpace(getenv("RUYI_GRADE_MAX_PER_HOUR")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			bad("RUYI_GRADE_MAX_PER_HOUR", "must be an integer from 1 to 1000")
		} else {
			c.GradeMaxPerHour = n
		}
	}
	c.NtfyBaseURL = "https://ntfy.sh/"
	if v := strings.TrimSpace(getenv("RUYI_NTFY_BASE_URL")); v != "" {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			bad("RUYI_NTFY_BASE_URL", "must be an absolute http(s) URL without userinfo/query")
		} else {
			c.NtfyBaseURL = strings.TrimRight(u.String(), "/") + "/"
		}
	}
	c.NtfyRetryDelay = time.Minute
	if v := strings.TrimSpace(getenv("RUYI_NTFY_RETRY_DELAY")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 100*time.Millisecond || d > time.Hour {
			bad("RUYI_NTFY_RETRY_DELAY", "must be a Go duration between 100ms and 1h")
		} else {
			c.NtfyRetryDelay = d
		}
	}
	if v := strings.TrimSpace(getenv("RUYI_AUTO_SOURCES")); v != "" {
		for _, x := range strings.Split(v, ",") {
			x = strings.TrimSpace(x)
			if !sourceRe.MatchString(x) {
				bad("RUYI_AUTO_SOURCES", "contains an invalid source key")
				break
			}
			if x == sets.LocalSource {
				bad("RUYI_AUTO_SOURCES", "contains a source reserved for local test sets")
				break
			}
			c.AutoSources = append(c.AutoSources, x)
		}
	}
	c.AutoTTL = 6 * time.Hour
	if v := strings.TrimSpace(getenv("RUYI_AUTO_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > 48*time.Hour {
			bad("RUYI_AUTO_TTL", "must be a Go duration between 1s and 48h")
		} else {
			c.AutoTTL = d
		}
	}

	if p := strings.TrimSpace(getenv("RUYI_SETS_FILE")); p != "" {
		st, err := os.Stat(p)
		switch {
		case err != nil || !st.Mode().IsRegular():
			bad("RUYI_SETS_FILE", "is not a readable regular file")
		case st.Size() > sets.MaxFileBytes:
			bad("RUYI_SETS_FILE", "file too large")
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				bad("RUYI_SETS_FILE", "is not a readable regular file")
				break
			}
			reg, err := sets.Parse(data, sourceRe.MatchString)
			if err != nil {
				bad("RUYI_SETS_FILE", err.Error())
			} else {
				c.Sets = reg
			}
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
	if c.BrainGradeToken != "" {
		s = append(s, c.BrainGradeToken)
	}
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
