package server_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A slow ntfy must not hold up the submission: the response comes after the
// short wait with notify=queued, the grade/archive are unaffected, a replay does
// not notify again, and the one background send still lands afterwards.
func TestNotifyIsAsyncAndDoesNotBlockSubmit(t *testing.T) {
	n := newNtfyStub(t)
	n.hang = make(chan struct{})
	e := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), ntfyWait: 150 * time.Millisecond})
	ck := e.session(t)
	t0 := time.Now()
	resp, b, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if el := time.Since(t0); el > 2*time.Second {
		t.Errorf("submit blocked on ntfy for %v", el)
	}
	if resp.StatusCode != 200 || g.Notify != "queued" || g.Mode != "graded" || !g.Archived || g.Summary.Correct != 1 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// same attempt again while ntfy is still hanging: replay, no second send, no wait
	t1 := time.Now()
	_, _, g2 := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if !g2.Replayed || g2.Notify != "" || time.Since(t1) > time.Second {
		t.Errorf("replay: %+v in %v", g2, time.Since(t1))
	}
	close(n.hang)
	waitFor(t, "the queued notification", 3*time.Second, func() bool { return n.count() == 1 })
	dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.app.Drain(dctx); err != nil {
		t.Errorf("drain: %v", err)
	}
	if n.count() != 1 || n.hitCount() != 1 {
		t.Errorf("exactly one send expected: posts=%d hits=%d", n.count(), n.hitCount())
	}
}

// A hanging ntfy: every attempt times out (kind=timeout, phase=response), at
// most 3 attempts are made, and the logs name the kind and the milliseconds but
// never the topic, the address, or the message text.
func TestNotifyTimeoutIsClassifiedAndBounded(t *testing.T) {
	n := newNtfyStub(t)
	n.hang = make(chan struct{})
	defer close(n.hang)
	e := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), retry: 20 * time.Millisecond,
		ntfyTimeout: 150 * time.Millisecond, ntfyWait: 50 * time.Millisecond})
	ck := e.session(t)
	_, _, g := e.submit(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if g.Notify != "queued" || g.Mode != "graded" {
		t.Fatalf("%+v", g)
	}
	waitFor(t, "giving up", 5*time.Second, func() bool { return strings.Contains(e.logs.String(), "giving up") })
	time.Sleep(200 * time.Millisecond)
	logs := e.logs.String()
	if c := strings.Count(logs, `msg="ntfy attempt failed"`); c != ntfyMaxAttemptsForTest {
		t.Errorf("attempts logged = %d, want %d:\n%s", c, ntfyMaxAttemptsForTest, logs)
	}
	if n.hitCount() != ntfyMaxAttemptsForTest {
		t.Errorf("hits = %d", n.hitCount())
	}
	for _, want := range []string{"kind=timeout", "phase=response", "attempt=3", "ms="} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	for _, bad := range []string{e.topic, strings.TrimSuffix(strings.TrimPrefix(n.url(), "http://"), "/"), "http://", "如意练习", "attempt 11111111", e.brain.gradeToken, e.brain.token, e.code} {
		if strings.Contains(logs, bad) {
			t.Errorf("log contains %q:\n%s", bad, logs)
		}
	}
}

func TestNotifyDialAndStatusKindsInLogs(t *testing.T) {
	e := newEnv(t, opts{sets: gradeSets, grade: true, retry: 10 * time.Millisecond}) // dead ntfy address
	_, _, g := e.submit(t, e.session(t), "demo-1", answersJSON(cid1, goodAnswers))
	if g.Notify != "retrying" {
		t.Errorf("notify = %q", g.Notify)
	}
	waitFor(t, "giving up", 3*time.Second, func() bool { return strings.Contains(e.logs.String(), "giving up") })
	logs := e.logs.String()
	if !strings.Contains(logs, "kind=dial") || strings.Contains(logs, e.topic) || strings.Contains(logs, "127.0.0.1:1") {
		t.Errorf("dial logs:\n%s", logs)
	}
	n := newNtfyStub(t)
	n.setFail(true)
	e2 := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), retry: 10 * time.Millisecond})
	e2.submit(t, e2.session(t), "demo-1", answersJSON(cid1, goodAnswers))
	waitFor(t, "giving up", 3*time.Second, func() bool { return strings.Contains(e2.logs.String(), "giving up") })
	l2 := e2.logs.String()
	if !strings.Contains(l2, "kind=http_status status=503") || strings.Contains(l2, e2.topic) {
		t.Errorf("status logs:\n%s", l2)
	}
}

// A retry that is only waiting for its turn is abandoned at shutdown (Drain
// returns at once and says so); it is not sent early and not lost silently.
func TestNotifyDrainAbandonsWaitingRetry(t *testing.T) {
	n := newNtfyStub(t)
	n.setFail(true)
	e := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), retry: time.Hour})
	e.submit(t, e.session(t), "demo-1", answersJSON(cid1, goodAnswers))
	dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := e.app.Drain(dctx); err != nil || time.Since(t0) > time.Second {
		t.Errorf("drain: %v after %v", err, time.Since(t0))
	}
	if n.hitCount() != 1 || !strings.Contains(e.logs.String(), "retry abandoned at shutdown") {
		t.Errorf("hits=%d logs:\n%s", n.hitCount(), e.logs.String())
	}
}

// Drain waits for a send that is in flight (until its context ends).
func TestNotifyDrainWaitsForInflightSend(t *testing.T) {
	n := newNtfyStub(t)
	n.hang = make(chan struct{})
	e := newEnv(t, opts{sets: gradeSets, grade: true, ntfyURL: n.url(), ntfyWait: 20 * time.Millisecond})
	e.submit(t, e.session(t), "demo-1", answersJSON(cid1, goodAnswers))
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := e.app.Drain(short); err == nil {
		t.Error("drain should report that a send is still running")
	}
	close(n.hang)
	long, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := e.app.Drain(long); err != nil || n.count() != 1 {
		t.Errorf("drain=%v posts=%d", err, n.count())
	}
}
