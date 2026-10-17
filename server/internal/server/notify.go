package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
)

// ---- parent notification (a heads-up, not an approval step) ---------------------
//
// The summary is posted to ntfy by ONE background goroutine per submission:
// first attempt, then (on failure) up to ntfyMaxAttempts-1 retries, all in the
// same goroutine. The request that triggered it waits at most ntfyRequestWait
// for the first attempt and reports "sent" / "retrying" / "queued"; it never
// waits for the retries and nothing here can change the grade, the archive or
// the idempotency result. A retry is only started after the previous attempt
// FAILED (timeout, error or non-2xx), never in parallel with it; a single
// submission (client_attempt_id) is notified by at most one goroutine per
// process. Residual risk: an attempt whose request reached ntfy but whose
// response did not come back in time counts as a failure and is retried, so
// the parent can then receive that heads-up twice.

const (
	// ntfyAttemptTimeout bounds ONE attempt (dial + TLS + request + response).
	// The link to the ntfy host is sometimes slow (a handshake of ~4.5 s) and
	// sometimes does not connect within 5 s, so a single attempt gets double that.
	ntfyAttemptTimeout = 10 * time.Second
	// ntfyMaxAttempts: the first send plus two retries.
	ntfyMaxAttempts = 3
	// ntfyRequestWait: how long the submission response waits for the first attempt.
	ntfyRequestWait = time.Second
	notifiedCap     = 5000

	testTag = "【测试】" // starts the ntfy title and body of a local test set's submission
)

// shanghai is UTC+8 all year (no DST), so a fixed zone needs no tzdata.
var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

type notifyJob struct{ first chan bool }

// startNotify starts the background send. It returns a nil job and a status
// when nothing is sent: "off" (no topic) or "duplicate" (this attempt id was
// already notified by this process).
func (s *Server) startNotify(r *attempts.Record) (*notifyJob, string) {
	if s.Topic == "" {
		return nil, "off"
	}
	if !s.markNotified(r.ClientAttemptID) {
		return nil, "duplicate"
	}
	title, body := ntfyTitle(r), ntfyBody(r)
	j := &notifyJob{first: make(chan bool, 1)}
	s.nwg.Add(1)
	go s.runNotify(j, title, body)
	return j, ""
}

// wait reports how the first attempt went, waiting at most d.
func (j *notifyJob) wait(status string, d time.Duration) string {
	if j == nil {
		return status
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case ok := <-j.first:
		if ok {
			return "sent"
		}
		return "retrying"
	case <-t.C:
		return "queued"
	}
}

// markNotified is the in-process de-duplication: true the first time an id is seen.
func (s *Server) markNotified(id string) bool {
	s.nmu.Lock()
	defer s.nmu.Unlock()
	if s.notified[id] {
		return false
	}
	s.notified[id] = true
	s.notifiedOrd = append(s.notifiedOrd, id)
	for len(s.notifiedOrd) > notifiedCap {
		delete(s.notified, s.notifiedOrd[0])
		s.notifiedOrd = s.notifiedOrd[1:]
	}
	return true
}

func (s *Server) runNotify(j *notifyJob, title, body string) {
	defer s.nwg.Done()
	delay := s.NtfyRetryDelay
	for attempt := 1; ; attempt++ {
		ok := s.postNtfy(title, body, attempt)
		if attempt == 1 {
			j.first <- ok // buffered: never blocks
		}
		if ok {
			if attempt > 1 {
				s.Log.Info("ntfy notification delivered after retry", "attempt", attempt)
			}
			return
		}
		if attempt >= ntfyMaxAttempts {
			s.Log.Warn("ntfy notification failed, giving up", "attempts", attempt)
			return
		}
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-s.nstop:
			t.Stop()
			s.Log.Warn("ntfy retry abandoned at shutdown", "attempts", attempt)
			return
		}
		delay *= 2
	}
}

// Drain is called after the HTTP server has stopped: it cuts short the waiting
// between retries (those are abandoned, not sent early) and waits, until ctx
// ends, for the sends that are in flight (each at most ntfyAttemptTimeout).
func (s *Server) Drain(ctx context.Context) error {
	s.nstopOnce.Do(func() { close(s.nstop) })
	done := make(chan struct{})
	go func() { s.nwg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ntfy phases reached by an attempt (for the failure log).
const (
	phDNS int32 = iota
	phConnect
	phTLS
	phRequest
	phResponse
)

var phaseNames = [...]string{"dns", "connect", "tls", "request", "response"}

// classifyNtfyErr turns an error into a fixed vocabulary, so that the log can
// say what went wrong WITHOUT err.Error() (which contains the URL, i.e. the topic).
func classifyNtfyErr(err error, phase int32) string {
	var dnsErr *net.DNSError
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invErr x509.CertificateInvalidError
	var recErr tls.RecordHeaderError
	var opErr *net.OpError
	var nerr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &nerr) && nerr.Timeout():
		return "timeout"
	case errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &invErr) || errors.As(err, &recErr) || phase == phTLS:
		return "tls"
	case errors.As(err, &opErr) && opErr.Op == "dial" || phase <= phConnect:
		return "dial"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "other"
}

// postNtfy makes one attempt. Logged on failure: attempt number, kind
// (dns|dial|tls|timeout|http_status|canceled|other), the phase reached, the HTTP
// status for http_status, and the elapsed ms. Never the topic, URL, body or token.
func (s *Server) postNtfy(title, body string, attempt int) bool {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), s.ntfyTimeout)
	defer cancel()
	var phase atomic.Int32
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { phase.Store(phDNS) },
		ConnectStart:      func(string, string) { phase.CompareAndSwap(phDNS, phConnect) },
		TLSHandshakeStart: func() { phase.Store(phTLS) },
		GotConn:           func(httptrace.GotConnInfo) { phase.Store(phRequest) },
		WroteRequest:      func(httptrace.WroteRequestInfo) { phase.Store(phResponse) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ntfyURL(), strings.NewReader(body))
	if err != nil {
		s.Log.Warn("ntfy attempt failed", "attempt", attempt, "kind", "other", "ms", time.Since(start).Milliseconds())
		return false
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", title)
	req.Header.Set("Tags", "memo")
	resp, err := s.ntfyClient.Do(req)
	if err != nil {
		ph := phase.Load()
		s.Log.Warn("ntfy attempt failed", "attempt", attempt, "kind", classifyNtfyErr(err, ph), "phase", phaseNames[ph], "ms", time.Since(start).Milliseconds())
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		s.Log.Warn("ntfy attempt failed", "attempt", attempt, "kind", "http_status", "status", resp.StatusCode, "ms", time.Since(start).Milliseconds())
		return false
	}
	return true
}

// ntfyTitle is the ntfy Title header. A test submission gets 【测试】 in front;
// the value is RFC 2047 encoded because HTTP headers are ASCII (ntfy decodes it).
// A real set's title stays the plain, unchanged "Ruyi practice".
func ntfyTitle(r *attempts.Record) string {
	if r.Test {
		return mime.QEncoding.Encode("utf-8", testTag+"Ruyi practice")
	}
	return "Ruyi practice"
}

// ntfyBody is one short paragraph: set, counts (right / wrong / to be checked /
// not auto-graded / unanswered), how it was recorded, the attempt id prefix and
// the completion time (Asia/Shanghai). No answers and no JSON: the full record
// is in the server's attempts archive (find it by the attempt id).
func ntfyBody(r *attempts.Record) string {
	return summaryLine(r) + "完成于 " + r.SubmittedAt.In(shanghai).Format("15:04:05") + "（北京时间）。"
}

func summaryLine(r *attempts.Record) string {
	answered := r.Summary.Total - r.Summary.Unanswered
	wrong := r.Summary.Graded - r.Summary.Correct
	var b strings.Builder
	if r.Test {
		return testSummaryLine(r, answered, wrong)
	}
	fmt.Fprintf(&b, "如意练习：%s %d 题，", r.SetTitle, answered)
	if r.Mode == modeGraded {
		fmt.Fprintf(&b, "对 %d，", r.Summary.Correct)
		if wrong > 0 {
			fmt.Fprintf(&b, "错 %d，", wrong)
		}
	}
	if r.Summary.Pending > 0 || r.Mode != modeGraded {
		fmt.Fprintf(&b, "待核对 %d，", r.Summary.Pending)
	}
	if r.Summary.Unsupport > 0 {
		fmt.Fprintf(&b, "不自动判 %d，", r.Summary.Unsupport)
	}
	if r.Summary.Unanswered > 0 {
		fmt.Fprintf(&b, "未答 %d，", r.Summary.Unanswered)
	}
	switch {
	case r.Mode != modeGraded:
		b.WriteString("已提交（未判分）。")
	case r.Summary.Recorded == 0:
		b.WriteString("已提交（未记入大脑，仅留档）。")
	case r.Summary.Recorded < answered:
		fmt.Fprintf(&b, "已提交（%d 题记入大脑，其余仅留档）。", r.Summary.Recorded)
	default:
		b.WriteString("已提交。")
	}
	fmt.Fprintf(&b, "attempt %s。", r.ClientAttemptID[:8])
	return b.String()
}

// testSummaryLine: the summary of a local test set submission. It says plainly
// that nothing went to the brain.
func testSummaryLine(r *attempts.Record, answered, wrong int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s如意练习：%s %d 题，对 %d，", testTag, r.SetTitle, answered, r.Summary.Correct)
	if wrong > 0 {
		fmt.Fprintf(&b, "错 %d，", wrong)
	}
	if r.Summary.Unanswered > 0 {
		fmt.Fprintf(&b, "未答 %d，", r.Summary.Unanswered)
	}
	b.WriteString("已提交（测试，仅留档，未记入大脑）。")
	fmt.Fprintf(&b, "attempt %s。", r.ClientAttemptID[:8])
	return b.String()
}
