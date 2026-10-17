package server_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func submitTri(t *testing.T, e *env, ck, cid string) {
	t.Helper()
	_, q := e.qgrade(t, ck, "tri-3", triAnswers(cid))
	if q.Queue == nil || q.Queue.Enqueue != "ok" {
		t.Fatalf("enqueue: %+v", q.Queue)
	}
}

func finish(t *testing.T, e *env, qrefs ...string) {
	t.Helper()
	for _, q := range qrefs {
		must(t, e.brain.q.Advance(0, q, "compared", map[string]string{"answer": "A-" + q, "rationale": "R", "compare": "match"}))
	}
}

func TestPollerOneNotificationPerSetAfterLastItem(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	_, other0 := notices(n)
	finish(t, e, q3)
	pollOnce(t, e)
	finish(t, e, q4)
	pollOnce(t, e)
	if d, _ := notices(n); d != 0 {
		t.Fatalf("notified with 2 of 3 processed")
	}
	finish(t, e, q5)
	pollOnce(t, e)
	pollOnce(t, e)
	pollOnce(t, e)
	d, other := notices(n)
	if d != 1 || other != other0 {
		t.Fatalf("completion notices %d (other %d->%d)", d, other0, other)
	}
	body := n.last().Body
	for _, want := range []string{"AI初评已出，待家长核", "三题套", "3 题有 AI 初评", "attempt 11111111", "北京时间"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q lacks %q", body, want)
		}
	}
	if strings.Contains(body, "A-example") || strings.Contains(body, "free text") {
		t.Errorf("the notification must not carry answers: %q", body)
	}
	if e.brain.q.Acked() == 0 {
		t.Errorf("events were never acked")
	}
}

func TestPollerNeverUsesWriteTokenAndSurvivesNotify403(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true, queueNotifyToken: "wrong-token"})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	for i := 0; i < 3; i++ {
		pollOnce(t, e)
	}
	if d, _ := notices(n); d != 0 {
		t.Errorf("no events could be read, nothing to tell (reconcile only after the deadline)")
	}
	if e.brain.q.Counter("write_token_on_events_endpoint") != 0 {
		t.Errorf("fell back to the write token")
	}
	if e.brain.q.Counter("auth_other_token") == 0 {
		t.Errorf("the wrong token never reached the brain")
	}
	if !strings.Contains(e.logs.String(), "queue notify token rejected") {
		t.Errorf("no warning in the log")
	}
	// the site itself still works
	_, q := e.qgrade(t, ck, "pend-2", answersJSON(cid2, map[string]any{q3: "free text"}))
	if q.enq(q3) != "ok" {
		t.Errorf("site broke: %+v", q.Queue)
	}
	if got := e.qview(t, ck, cid1).item(q3).UI; got != "ai_first_pass" {
		t.Errorf("status page needs no notify token: %s", got)
	}
}

func TestPollerAckFailureStillNotifiesOnceAndRecovers(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	e.brain.q.Set("ack_fail", "2")
	pollOnce(t, e)
	if d, _ := notices(n); d != 1 {
		t.Fatalf("notices %d after a failed ack", d)
	}
	if e.brain.q.Acked() != 0 {
		t.Errorf("acked despite failure")
	}
	pollOnce(t, e) // events come again (cursor kept): same session, no second notice; ack fails again
	pollOnce(t, e) // ack works
	if d, _ := notices(n); d != 1 {
		t.Fatalf("duplicate after ack failure: %d", d)
	}
	if e.brain.q.Acked() == 0 {
		t.Errorf("never acked after recovery")
	}
}

func TestPollerNtfyFailureKeepsStateAndRetries(t *testing.T) {
	now := time.Now()
	clock := &now
	e, n, ck := queueEnv(t, opts{queueNotify: true, now: func() time.Time { return *clock }, retry: time.Millisecond})
	submitTri(t, e, ck, cid1)
	n.setFail(true)
	finish(t, e, q3, q4, q5)
	pollOnce(t, e)
	if d, _ := notices(n); d != 0 {
		t.Fatal("sent while ntfy is down")
	}
	if e.brain.q.Acked() == 0 {
		t.Errorf("ingest/ack must not depend on ntfy")
	}
	n.setFail(false)
	pollOnce(t, e) // backoff not elapsed
	if d, _ := notices(n); d != 0 {
		t.Fatal("retried before the backoff")
	}
	*clock = now.Add(2 * time.Minute)
	pollOnce(t, e)
	pollOnce(t, e)
	if d, _ := notices(n); d != 1 {
		t.Fatalf("notices %d", d)
	}
}

// restart: crash at each stage, start a second process on the same state dir.
func TestPollerRestartResendsNothingLostAndDuplicatesOnlyInTheDocumentedWindow(t *testing.T) {
	for _, c := range []struct {
		stage string
		want  int // completion notices after the restart
	}{
		{"after_ingest_save", 1},
		{"after_ack", 1},
		{"before_ntfy", 1},
		{"after_ntfy", 2}, // the one documented window: ntfy said 2xx, "sent" was not yet persisted
		{"after_sent_log", 1},
	} {
		t.Run(c.stage, func(t *testing.T) {
			var armed atomic.Bool
			e, n, ck := queueEnv(t, opts{queueNotify: true, queueHook: func(s string) bool { return s == c.stage && armed.CompareAndSwap(true, false) }})
			submitTri(t, e, ck, cid1)
			finish(t, e, q3, q4, q5)
			armed.Store(true)
			pollOnce(t, e) // "crashes" at the stage
			e.app.CloseQueue()
			e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
			pollOnce(t, e2)
			pollOnce(t, e2)
			if d, _ := notices(n); d != c.want {
				t.Fatalf("stage %s: %d completion notices, want %d", c.stage, d, c.want)
			}
		})
	}
}

func TestPollerLocalStateLossIsReconciledFromTheBrain(t *testing.T) {
	var armed atomic.Bool
	e, n, ck := queueEnv(t, opts{queueNotify: true, queueHook: func(s string) bool { return s == "after_ack" && armed.CompareAndSwap(true, false) }})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	armed.Store(true)
	pollOnce(t, e) // events acked, notification not yet sent, then "crash"
	e.app.CloseQueue()
	if err := os.Remove(filepath.Join(e.qDir, "notify.json")); err != nil {
		t.Fatal(err)
	}
	if e.brain.q.DropEvents() != 0 { // acked events are not re-delivered
		t.Log("(some events were still unacked)")
	}
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
	pollOnce(t, e2)
	pollOnce(t, e2)
	if d, _ := notices(n); d != 1 {
		t.Fatalf("after a lost notify.json: %d notices (reconcile must send exactly one)", d)
	}
	if !strings.Contains(e2.logs.String(), "reconciling") {
		t.Errorf("the loss was not logged")
	}
}

func TestPollerCorruptStateFileIsMovedAsideNotDeleted(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	e.brain.q.Set("events_fail", "1000") // nothing readable, so nothing is saved by the first process
	pollOnce(t, e)
	e.app.CloseQueue()
	must(t, os.WriteFile(filepath.Join(e.qDir, "notify.json"), []byte("{not json"), 0o600))
	e.brain.q.Set("events_fail", "0")
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
	pollOnce(t, e2)
	pollOnce(t, e2)
	m, _ := filepath.Glob(filepath.Join(e.qDir, "notify.json.corrupt-*"))
	if len(m) != 1 {
		t.Errorf("corrupt file not kept aside: %v", m)
	}
	if d, _ := notices(n); d != 1 {
		t.Errorf("notices %d", d)
	}
}

func TestPollerLostEventsStillCompleteViaReconcile(t *testing.T) {
	now := time.Now()
	clock := &now
	e, n, ck := queueEnv(t, opts{queueNotify: true, now: func() time.Time { return *clock }})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	if e.brain.q.DropEvents() == 0 {
		t.Fatal("no events to drop")
	}
	pollOnce(t, e)
	if d, _ := notices(n); d != 0 {
		t.Fatal("told without any event before the deadline")
	}
	*clock = now.Add(30 * time.Hour) // way past the 24 h deadline
	pollOnce(t, e)
	pollOnce(t, e)
	if d, _ := notices(n); d != 1 {
		t.Fatalf("lost events never reconciled: %d", d)
	}
}

func TestPollerPurgedEverythingIsSilentAndMixedCountsPurged(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	for _, q := range []string{q3, q4, q5} {
		must(t, e.brain.q.Advance(0, q, "purged", nil))
	}
	pollOnce(t, e)
	pollOnce(t, e)
	if d, _ := notices(n); d != 0 {
		t.Fatalf("everything deleted: nothing to tell, got %d", d)
	}
	e2, n2, ck2 := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e2, ck2, cid1)
	finish(t, e2, q3, q4)
	must(t, e2.brain.q.Advance(0, q5, "purged", nil))
	pollOnce(t, e2)
	if d, _ := notices(n2); d != 1 || !strings.Contains(n2.last().Body, "1 题已被删除") {
		t.Errorf("mixed: %d %q", d, n2.last().Body)
	}
}

func TestPollerFailedItemsSayNeedHuman(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	for _, q := range []string{q3, q4, q5} {
		must(t, e.brain.q.Advance(0, q, "failed", nil))
	}
	pollOnce(t, e)
	if d, _ := notices(n); d != 1 || !strings.Contains(n.last().Body, "AI处理完毕，需要人工") || !strings.Contains(n.last().Body, "3 题待人工") {
		t.Errorf("%d %q", d, n.last().Body)
	}
}

func TestPollerWithoutSessionFieldsFallsBackToLinks(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	e.brain.q.Set("no_session_fields", "1")
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4)
	pollOnce(t, e)
	finish(t, e, q5)
	pollOnce(t, e)
	if d, _ := notices(n); d != 1 {
		t.Errorf("notices %d", d)
	}
}

func TestPollerTwoSetsTwoNotifications(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	e.qgrade(t, ck, "pend-2", answersJSON(cid2, map[string]any{q3: "free text2"}))
	ids := e.brain.q.ItemsOf(cid1)
	ids2 := e.brain.q.ItemsOf(cid2)
	must(t, e.brain.q.Advance(ids[0], "", "compared", nil))
	must(t, e.brain.q.Advance(ids2[0], "", "compared", nil))
	pollOnce(t, e)
	if d, _ := notices(n); d != 2 {
		t.Errorf("notices %d", d)
	}
}

func TestPollerRestartDoesNotRepeatAnAlreadySentSet(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true})
	submitTri(t, e, ck, cid1)
	finish(t, e, q3, q4, q5)
	pollOnce(t, e)
	e.app.CloseQueue()
	e.brain.q.DropEvents()
	os.Remove(filepath.Join(e.qDir, "notify.json")) // even without notify.json the sent log remembers
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
	pollOnce(t, e2)
	pollOnce(t, e2)
	if d, _ := notices(n); d != 1 {
		t.Errorf("repeated: %d", d)
	}
}

func TestPollerSecondProcessOnSameDirRefused(t *testing.T) {
	e, n, _ := queueEnv(t, opts{queueNotify: true})
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true}) // no poller needed to open the links
	_ = e2
	// the flock belongs to whoever called EnableNotify; a second EnableNotify must fail
	if err := e2.app.EnableNotify(); err == nil {
		t.Errorf("two pollers on one state dir")
	}
}

func TestQueueSecretsNeverReachLogsArchiveStateOrPages(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true, queueNotifyToken: ""})
	submitTri(t, e, ck, cid1)
	_, resp := e.do(t, "GET", "/api/practice/attempts/"+cid1+"/queue", "", nil, ck)
	var seen strings.Builder
	seen.Write(resp)
	finish(t, e, q3, q4, q5)
	pollOnce(t, e)
	_, resp = e.do(t, "GET", "/api/practice/attempts/"+cid1+"/queue", "", nil, ck)
	seen.Write(resp)
	e.brain.q.Set("events_fail", "1")
	pollOnce(t, e) // a failing read is logged too
	for _, f := range []string{"/api/practice/pend-2/quota", "/api/practice/demo-1"} {
		_, b := e.do(t, "GET", f, "", nil, ck)
		seen.Write(b)
	}
	for _, p := range n.posts {
		seen.WriteString(p.Body)
		for k, v := range p.Header {
			seen.WriteString(k + strings.Join(v, ","))
		}
	}
	var files strings.Builder
	for _, dir := range []string{e.qDir, e.attDir} {
		ents, _ := os.ReadDir(dir)
		for _, f := range ents {
			b, _ := os.ReadFile(filepath.Join(dir, f.Name()))
			files.Write(b)
		}
	}
	secrets := map[string]string{"read token": e.brain.token, "write token": e.brain.gradeToken, "notify token": e.brain.notifyToken, "family code": e.code, "cookie secret": e.secret}
	for name, v := range secrets {
		for where, hay := range map[string]string{"log": e.logs.String(), "files": files.String(), "pages/ntfy": seen.String()} {
			if strings.Contains(hay, v) {
				t.Errorf("%s leaked into %s", name, where)
			}
		}
	}
	if strings.Contains(e.logs.String(), e.topic) || strings.Contains(files.String(), e.topic) || strings.Contains(seen.String(), e.topic) {
		t.Errorf("the ntfy topic leaked")
	}
	// the state files are private
	if fi, err := os.Stat(filepath.Join(e.qDir, "links.jsonl")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("links.jsonl perms: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(e.qDir, "notify.json")); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("notify.json perms: %v %v", fi, err)
	}
}

func TestQueueSourceNeverCallsVoidOrStats(t *testing.T) {
	var all strings.Builder
	for _, dir := range []string{"../brain", "."} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range m {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, _ := os.ReadFile(f)
			all.Write(b)
		}
	}
	src := all.String()
	for _, bad := range []string{"/void", "queue/stats", "/reanalyze", "/review"} {
		if strings.Contains(src, bad) {
			t.Errorf("site code mentions %q: S1 never calls it", bad)
		}
	}
}

func TestQueueArchiveV3LinesAreReadableByThePreviousBuild(t *testing.T) {
	// the #22 reader tolerates unknown fields; an S1 archive must stay loadable after a rollback
	e, _, ck := queueEnv(t, opts{})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	e.app.CloseQueue()
	lines := archiveLines(t, e.attDir)
	if len(lines) != 1 {
		t.Fatalf("%d lines", len(lines))
	}
	if v, _ := lines[0]["v"].(float64); v != 3 {
		t.Errorf("archive version %v", lines[0]["v"])
	}
	// reopen with queue OFF (rollback to the switch-off): replay still works, archive not rewritten
	before, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: "http://127.0.0.1:1/"})
	ck2 := e2.session(t)
	resp, b := e2.do(t, "POST", "/api/practice/pend-2/grade", answersJSON(cid1, map[string]any{q3: "free text"}), map[string]string{"Content-Type": "application/json"}, ck2)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"replayed":true`) || strings.Contains(string(b), `"queue"`) {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
	after, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	if string(before) != string(after) {
		t.Errorf("replay rewrote the archive")
	}
}

// P3-1: a failing events read backs off exponentially (45 s base, jitter off in tests) up
// to 5 minutes; the first successful read goes back to the normal period.
func TestPollerEventReadBackoffDoublesCapsAndResets(t *testing.T) {
	e, _, _ := queueEnv(t, opts{queueNotify: true})
	ctx := context.Background()
	e.brain.q.Set("events_fail", "1000")
	want := []time.Duration{90 * time.Second, 180 * time.Second, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := e.app.TPollStep(ctx); got != w {
			t.Fatalf("failure %d: next wait %v, want %v", i+1, got, w)
		}
	}
	e.brain.q.Set("events_fail", "0")
	if got := e.app.TPollStep(ctx); got != 45*time.Second {
		t.Fatalf("after a good read the period is back to 45 s, got %v", got)
	}
	e.brain.q.Set("events_fail", "1")
	if got := e.app.TPollStep(ctx); got != 90*time.Second {
		t.Fatalf("the backoff restarts from the first step, got %v", got)
	}
}
