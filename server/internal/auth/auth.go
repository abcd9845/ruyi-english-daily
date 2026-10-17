// Package auth implements the family-code login, signed session cookies and
// the per-IP failed-login limiter.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieName is the session cookie.
const CookieName = "ruyi_sess"

// Authenticator checks family codes and signs/verifies session cookies.
//
// Cookie value: v1.<unix expiry>.<hex HMAC-SHA256>. The MAC covers the version,
// the expiry and SHA-256(family code), keyed by the session secret, so changing
// either the code or the secret invalidates every existing cookie.
type Authenticator struct {
	codeHash [32]byte
	secret   []byte
	ttl      time.Duration
	secure   bool
	now      func() time.Time
}

// New builds an Authenticator. now may be nil (time.Now).
func New(familyCode, sessionSecret string, ttl time.Duration, secure bool, now func() time.Time) *Authenticator {
	if now == nil {
		now = time.Now
	}
	return &Authenticator{
		codeHash: sha256.Sum256([]byte(familyCode)),
		secret:   []byte(sessionSecret),
		ttl:      ttl,
		secure:   secure,
		now:      now,
	}
}

// CheckCode compares in constant time (both sides are hashed first so the
// comparison length does not depend on the guess).
func (a *Authenticator) CheckCode(guess string) bool {
	g := sha256.Sum256([]byte(guess))
	return subtle.ConstantTimeCompare(g[:], a.codeHash[:]) == 1
}

func (a *Authenticator) mac(exp int64) []byte {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte("ruyi-sess|v1|"))
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	m.Write([]byte("|"))
	m.Write(a.codeHash[:])
	return m.Sum(nil)
}

// Issue creates a cookie value that expires at now+ttl.
func (a *Authenticator) Issue() (value string, expires time.Time) {
	expires = a.now().Add(a.ttl).Truncate(time.Second)
	exp := expires.Unix()
	return "v1." + strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(a.mac(exp)), expires
}

// Valid verifies signature and expiry.
func (a *Authenticator) Valid(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || exp <= a.now().Unix() {
		return false
	}
	got, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	return hmac.Equal(got, a.mac(exp))
}

// Cookie builds the Set-Cookie for a fresh session.
func (a *Authenticator) Cookie() *http.Cookie {
	v, exp := a.Issue()
	return &http.Cookie{
		Name: CookieName, Value: v, Path: "/",
		Expires: exp, MaxAge: int(a.ttl / time.Second),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie expires the session cookie.
func (a *Authenticator) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	}
}

// Authenticated reports whether the request carries a valid session cookie.
func (a *Authenticator) Authenticated(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && a.Valid(c.Value)
}

// Limiter allows at most max failures per window per key (the client IP).
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	capKey int
	fails  map[string][]time.Time
	now    func() time.Time
}

// NewLimiter returns a limiter. now may be nil.
func NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{max: max, window: window, capKey: 10000, fails: map[string][]time.Time{}, now: now}
}

func (l *Limiter) prune(key string, t time.Time) []time.Time {
	list := l.fails[key]
	i := 0
	for i < len(list) && t.Sub(list[i]) >= l.window {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = list
	return list
}

// Blocked reports whether key is over the limit, and how long until a slot frees.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	list := l.prune(key, t)
	if len(list) >= l.max {
		return true, list[0].Add(l.window).Sub(t)
	}
	return false, 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	if len(l.fails) >= l.capKey {
		for k := range l.fails { // bounded memory: drop expired, then arbitrary
			l.prune(k, t)
		}
		for k := range l.fails {
			if len(l.fails) < l.capKey {
				break
			}
			delete(l.fails, k)
		}
	}
	l.prune(key, t)
	l.fails[key] = append(l.fails[key], t)
}

// Reset forgets key (after a successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
