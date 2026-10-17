package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /api/config carries "queue":true only when the queue is on: with it off the answer is the
// #22 one byte for byte (the page reads this to word the daily limit).
func TestConfigSaysQueueOnlyWhenItIsOn(t *testing.T) {
	on, _, ck := queueEnv(t, opts{})
	_, b := on.do(t, "GET", "/api/config", "", nil, ck)
	if !strings.Contains(string(b), `"queue":true`) {
		t.Errorf("queue on: %s", b)
	}
	off := newEnv(t, opts{sets: queueSets, grade: true})
	_, b = off.do(t, "GET", "/api/config", "", nil, off.session(t))
	if strings.Contains(string(b), "queue") {
		t.Errorf("queue off must not mention it: %s", b)
	}
}

// P3-B: the page text of retry_exhausted promises nothing about a parent notice (it may be
// delayed until the whole set is processed, or never sent when the poller is off).
func TestRetryExhaustedWordingPromisesNoNotice(t *testing.T) {
	e, _, _ := queueEnv(t, opts{})
	got := e.app.TNotQueuedLabel("retry_exhausted")
	if got != "暂时没能排队，答案已存档" {
		t.Errorf("%q", got)
	}
	for _, w := range []string{"家长会看", "家长会核对", "已通知"} {
		if strings.Contains(got, w) {
			t.Errorf("%q must not say %q", got, w)
		}
	}
}

// P3-D (r4 item 5): a poller that ran on an empty queue (nothing to save, so no notify.json and no
// start marker), was switched off and on again after submissions exist, is a first start, not a loss.
func TestPollerOnEmptyQueueThenOffOnIsInfoNotWarn(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true}) // poller on, empty queue
	pollOnce(t, e)
	e.app.CloseQueue()
	if _, err := os.Stat(filepath.Join(e.qDir, ".notify-started")); err == nil {
		t.Fatal("the start marker must not exist before notify.json was ever written")
	}
	// poller off, a submission comes in
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true})
	ck2 := e2.session(t)
	_ = ck
	submitTri(t, e2, ck2, cid1)
	e2.app.CloseQueue()
	// poller on again
	e3 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
	pollOnce(t, e3)
	l := e3.logs.String()
	if strings.Contains(l, "level=WARN msg=\"queue notify state was missing or damaged") || !strings.Contains(l, "first start of the poller") {
		t.Errorf("a poller that never had anything to save is not a state loss:\n%s", l)
	}
}
