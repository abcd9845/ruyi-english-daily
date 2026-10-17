package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// answered_at is part of the idempotency key: the value first sent with grade is stored in
// links.jsonl and every later request (lost-reply retry, drop-and-resubmit, after a restart)
// carries exactly it. The mock refuses (409) a replay that differs.
func TestQueueAnsweredAtIsFixedAcrossRetryResubmitAndRestart(t *testing.T) {
	at := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	withAt := func(body string) string {
		return strings.Replace(body, `{`, `{"answered_at":"`+at.Format(time.RFC3339)+`",`, 1)
	}
	allAt := func(e *env) []string {
		var out []string
		for _, b := range e.brain.q.Requests() {
			var m struct {
				Items []struct {
					AnsweredAt string `json:"answered_at"`
				} `json:"items"`
			}
			must(t, json.Unmarshal(b, &m))
			for _, it := range m.Items {
				out = append(out, it.AnsweredAt)
			}
		}
		return out
	}
	want := at.Format(time.RFC3339)

	// (a) first reply lost, retry by page POST, then a restart and another retry
	e, n, ck := queueEnv(t, opts{queueNoHint: true})
	e.brain.q.Set("enqueue_mode", "lost")
	e.qgrade(t, ck, "tri-3", withAt(triAnswers(cid1)))
	if l := e.app.TLink(cid1); !l.AnsweredAt.Equal(at) {
		t.Fatalf("the sent answered_at is not stored with the link: %v", l.AnsweredAt)
	}
	e.brain.q.Set("enqueue_mode", "")
	if postEnqueue(t, e, ck, cid1) != 200 {
		t.Fatal("retry")
	}
	e.app.CloseQueue()
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNoHint: true})
	if l := e2.app.TLink(cid1); !l.AnsweredAt.Equal(at) {
		t.Fatalf("after a restart: %v", l.AnsweredAt)
	}
	// the brain-side items exist (replay): every request that ever reached the brain said the same time
	for i, v := range allAt(e) {
		if v != want {
			t.Errorf("request item %d carries answered_at %q, want %q", i, v, want)
		}
	}
	if c := e.brain.q.Counter("enqueue_conflict"); c != 0 {
		t.Errorf("the brain saw %d conflicting replays", c)
	}

	// (b) down first, then one kind switched off: the batch is refused and resubmitted without it
	e3, _, ck3 := queueEnv(t, opts{queueNoHint: true})
	e3.brain.q.Set("enqueue_mode", "500")
	e3.qgrade(t, ck3, "tri-3", withAt(triAnswers(cid1)))
	e3.brain.q.Set("enqueue_mode", "")
	e3.brain.q.SetKind(q5, "judged")
	postEnqueue(t, e3, ck3, cid1)
	vs := allAt(e3)
	if len(vs) < 4 {
		t.Fatalf("expected a refused batch and a resubmit: %v", vs)
	}
	for _, v := range vs {
		if v != want {
			t.Errorf("resubmit changed answered_at: %v", vs)
			break
		}
	}
	if c := e3.brain.q.Counter("enqueue_conflict"); c != 0 {
		t.Errorf("conflicts %d", c)
	}

	// (c) an answer without answered_at (page value missing/out of window) stays without it on every request
	e4, _, ck4 := queueEnv(t, opts{queueNoHint: true})
	e4.brain.q.Set("enqueue_mode", "lost")
	e4.qgrade(t, ck4, "tri-3", triAnswers(cid1))
	e4.brain.q.Set("enqueue_mode", "")
	postEnqueue(t, e4, ck4, cid1)
	for _, v := range allAt(e4) {
		if v != "" {
			t.Errorf("answered_at appeared on a retry: %q", v)
		}
	}
	if e4.brain.q.Counter("enqueue_conflict") != 0 {
		t.Errorf("conflict without answered_at")
	}

	// (d) the mock does refuse a replay with another answered_at (proves (a)-(c) are not vacuous):
	// damage the stored value, retry -> 409, which is a permanent "not queued", never a loop
	now := time.Now()
	clock := &now
	e5, _, ck5 := queueEnv(t, opts{queueNoHint: true, now: func() time.Time { return *clock }})
	e5.brain.q.Set("enqueue_mode", "lost")
	e5.qgrade(t, ck5, "tri-3", withAt(triAnswers(cid1)))
	e5.brain.q.Set("enqueue_mode", "")
	e5.app.TSetAnsweredAt(cid1, at.Add(-time.Hour))
	postEnqueue(t, e5, ck5, cid1)
	if e5.brain.q.Counter("enqueue_conflict") != 1 {
		t.Fatalf("the mock must refuse a changed answered_at (conflicts %d)", e5.brain.q.Counter("enqueue_conflict"))
	}
	calls := e5.brain.q.Counter("enqueue_calls")
	*clock = clock.Add(3 * time.Hour)
	e5.app.SweepOnce(context.Background())
	postEnqueue(t, e5, ck5, cid1)
	if e5.brain.q.Counter("enqueue_calls") != calls {
		t.Errorf("a conflict must not be retried")
	}
}

// Parameter errors are 400 validation_failed (business reasons are 422, throttling 429): a 400 is
// the site's own bug, never a transient failure -> not queued, warn, no retry, no failed try.
func TestQueue400ValidationFailedIsNotQueuedAndNeverRetried(t *testing.T) {
	now := time.Now()
	clock := &now
	e, _, ck := queueEnv(t, opts{queueNoHint: true, now: func() time.Time { return *clock }})
	e.brain.q.Set("enqueue_mode", "400")
	_, q := e.qgrade(t, ck, "tri-3", triAnswers(cid1))
	if q.enq(q3) != "not_queued" || q.enq(q5) != "not_queued" {
		t.Fatalf("%+v", q.Queue)
	}
	calls := e.brain.q.Counter("enqueue_calls")
	*clock = clock.Add(3 * time.Hour)
	e.app.SweepOnce(context.Background())
	postEnqueue(t, e, ck, cid1)
	if e.brain.q.Counter("enqueue_calls") != calls {
		t.Errorf("400 was retried (%d -> %d)", calls, e.brain.q.Counter("enqueue_calls"))
	}
	for _, it := range e.app.TLink(cid1).Items {
		if it.Enq != "not_queued" || it.Reason != "invalid" || it.Tries != 0 || !it.NextTry.IsZero() {
			t.Errorf("400 must be a plain not-queued, no failed try: %+v", it)
		}
	}
	if !strings.Contains(e.logs.String(), "level=WARN") || !strings.Contains(e.logs.String(), "request rejected as malformed") {
		t.Errorf("a 400 must be warned about")
	}
	if v := e.qview(t, ck, cid1); v.item(q3).UI != "not_queued" || v.Summary.Retrying != 0 {
		t.Errorf("%+v", v.item(q3))
	}
}

// The queue state shown to the child follows the brain's states: "proposed" (the bot has only
// proposed an answer) is still "AI 判卷中"; the first pass starts at compared (and judged).
func TestQueueStatusFollowsComparedNotProposed(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	must(t, e.brain.q.Advance(0, q3, "proposed", map[string]string{"answer": "BOT-DRAFT", "rationale": "BOT-WHY"}))
	it := e.qview(t, ck, cid1).item(q3)
	if it.UI != "ai_working" || it.Result != nil || strings.Contains(it.Label+it.CompareText, "BOT-DRAFT") {
		t.Fatalf("proposed must not show a first pass or any text: %+v", it)
	}
	must(t, e.brain.q.Advance(0, q3, "compared", map[string]string{"compare": "match", "answer": "AI-ANSWER", "rationale": "AI-WHY"}))
	it = e.qview(t, ck, cid1).item(q3)
	if it.UI != "ai_first_pass" || it.Result == nil || it.Result.ProposedAnswer != "AI-ANSWER" {
		t.Fatalf("compared is the first pass: %+v", it)
	}
}
