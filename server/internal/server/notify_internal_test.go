package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"testing"
)

func TestClassifyNtfyErr(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		phase int32
		want  string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "secret-host"}, phDNS, "dns"},
		{"deadline in tls", context.DeadlineExceeded, phTLS, "timeout"},
		{"deadline wrapped", &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}, phConnect, "timeout"},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, phConnect, "dial"},
		{"cert", x509.UnknownAuthorityError{}, phTLS, "tls"},
		{"tls reset", errors.New("EOF"), phTLS, "tls"},
		{"tls record", tls.RecordHeaderError{}, phRequest, "tls"},
		{"canceled", context.Canceled, phResponse, "canceled"},
		{"other", errors.New("boom"), phResponse, "other"},
	}
	for _, c := range cases {
		if got := classifyNtfyErr(c.err, c.phase); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMarkNotifiedDedupAndBound(t *testing.T) {
	s := New(Deps{})
	if !s.markNotified("a") || s.markNotified("a") {
		t.Error("an id is notified once")
	}
	for i := 0; i < notifiedCap+10; i++ {
		s.markNotified(string(rune('A'+i%26)) + string(rune(i)) + "x")
	}
	if len(s.notified) > notifiedCap || len(s.notifiedOrd) > notifiedCap {
		t.Errorf("unbounded: %d", len(s.notified))
	}
}
