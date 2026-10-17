package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// S1: pending-answer queue. Everything runs against the in-process fake brain
// whose queue half is internal/mockqueue (contract v1.0).

const queueSets = `{"sets":[
 {"id":"demo-1","title":"演示套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"pend-2","title":"待评套","source":"` + practiceSrc + `","qrefs":["example.set:math:3","example.set:math:4"]},
 {"id":"tri-3","title":"三题套","source":"` + practiceSrc + `","qrefs":["example.set:math:3","example.set:math:4","example.set:math:5"]}
]}`

func queueEnv(t *testing.T, o opts) (*env, *ntfyStub, string) {
	n := newNtfyStub(t)
	o.sets, o.grade, o.ntfyURL, o.queue = queueSets, true, n.url(), true
	e := newEnv(t, o)
	return e, n, e.session(t)
}

type qView struct {
	CID     string `json:"cid"`
	Stale   bool   `json:"stale"`
	Summary struct {
		Total      int  `json:"total"`
		Waiting    int  `json:"waiting"`
		AIWorking  int  `json:"ai_working"`
		FirstPass  int  `json:"ai_first_pass"`
		Reviewed   int  `json:"reviewed"`
		Manual     int  `json:"manual"`
		Purged     int  `json:"purged"`
		NotQueued  int  `json:"not_queued"`
		Retrying   int  `json:"enqueue_retry"`
		Unknown    int  `json:"unknown"`
		AllDone    bool `json:"all_processed"`
		AnyPending bool `json:"any_pending"`
	} `json:"summary"`
	Items []qViewItem `json:"items"`
}
type qViewItem struct {
	QRef             string `json:"qref"`
	UI               string `json:"ui_state"`
	Label            string `json:"label"`
	State            string `json:"state"`
	Kind             string `json:"kind"`
	Hours            int    `json:"hours"`
	CompareText      string `json:"compare_text"`
	ReviewVerdict    string `json:"review_verdict"`
	Reason           string `json:"reason"`
	ReanalyzeEnabled bool   `json:"reanalyze_enabled"`
	Result           *struct {
		ProposedAnswer string `json:"proposed_answer"`
		Rationale      string `json:"rationale"`
	} `json:"result"`
}

func (v qView) item(q string) qViewItem {
	for _, it := range v.Items {
		if it.QRef == q {
			return it
		}
	}
	return qViewItem{}
}

func (e *env) qview(t *testing.T, ck, cid string) qView {
	t.Helper()
	resp, b := e.do(t, "GET", "/api/practice/attempts/"+cid+"/queue", "", nil, ck)
	if resp.StatusCode != 200 {
		t.Fatalf("queue view %d %s", resp.StatusCode, b)
	}
	var v qView
	must(t, json.Unmarshal(b, &v))
	return v
}

type qGrade struct {
	Queue *struct {
		Enqueue string `json:"enqueue"`
		Items   []struct {
			QRef   string `json:"qref"`
			Enq    string `json:"enq"`
			Reason string `json:"reason"`
		} `json:"items"`
	} `json:"queue"`
}

func (e *env) qgrade(t *testing.T, ck, set, body string) (gresp, qGrade) {
	t.Helper()
	resp, b, g := e.submit(t, ck, set, body)
	if resp.StatusCode != 200 {
		t.Fatalf("grade %d %s", resp.StatusCode, b)
	}
	var q qGrade
	must(t, json.Unmarshal(b, &q))
	return g, q
}

func (q qGrade) enq(qref string) string {
	if q.Queue == nil {
		return "-"
	}
	for _, it := range q.Queue.Items {
		if it.QRef == qref {
			return it.Enq
		}
	}
	return "-"
}

func pollOnce(t *testing.T, e *env) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = e.app.PollOnce(ctx)
}

func TestQueueEnqueueAndFlowToFirstPass(t *testing.T) {
	e, _, ck := queueEnv(t, opts{queueNotify: true})
	body := answersJSON(cid1, map[string]any{"example.set:math:3": "free text", "example.set:math:4": []string{"x=5"}})
	g, q := e.qgrade(t, ck, "pend-2", body)
	if q.Queue == nil || q.Queue.Enqueue != "ok" || q.enq(q3) != "ok" || q.enq(q4) != "ok" {
		t.Fatalf("grade queue: %+v", q.Queue)
	}
	if g.RecordedCount != 2 { // the unsupported answer was written by the enqueue
		t.Errorf("recorded_count %d", g.RecordedCount)
	}
	v := e.qview(t, ck, cid1)
	if v.item(q3).UI != "waiting" || v.item(q4).UI != "waiting" || !v.Summary.AnyPending || v.Summary.Waiting != 2 {
		t.Fatalf("view: %+v", v)
	}
	if !strings.Contains(v.item(q3).Label, "待解答") {
		t.Errorf("label %q", v.item(q3).Label)
	}
	mq := e.brain.q
	if err := mq.Advance(0, q3, "claimed", nil); err != nil {
		t.Fatal(err)
	}
	if got := e.qview(t, ck, cid1).item(q3); got.UI != "ai_working" {
		t.Errorf("claimed -> %+v", got)
	}
	must(t, mq.Advance(0, q3, "compared", map[string]string{"answer": "6 厘米（AI 拟）", "rationale": "因为 …", "compare": "match"}))
	got := e.qview(t, ck, cid1).item(q3)
	if got.UI != "ai_first_pass" || got.Result == nil || got.Result.ProposedAnswer != "6 厘米（AI 拟）" || got.Result.Rationale != "因为 …" || !strings.Contains(got.Label, "AI 初评") {
		t.Fatalf("first pass: %+v", got)
	}
	if mq.Counter("enqueue_calls") != 1 {
		t.Errorf("a view must never enqueue: %d calls", mq.Counter("enqueue_calls"))
	}
}

var q5 = "example.set:math:5"

func triAnswers(cid string) string {
	return answersJSON(cid, map[string]any{q3: "free text", q4: []string{"x=5"}, q5: "7"})
}

func notices(n *ntfyStub) (done, other int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.posts {
		if strings.Contains(p.Body, "AI初评已出") || strings.Contains(p.Body, "AI处理完毕") {
			done++
		} else {
			other++
		}
	}
	return
}

func TestQueueGradedAnswersAreNotEnqueued(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	_, q := e.qgrade(t, ck, "demo-1", answersJSON(cid1, goodAnswers))
	if q.enq(q1) != "-" || q.enq(q2) != "-" || q.enq(q3) != "ok" {
		t.Fatalf("%+v", q.Queue)
	}
	reqs := e.brain.q.Requests()
	if len(reqs) != 1 || strings.Contains(string(reqs[0]), q1) || strings.Contains(string(reqs[0]), q2) {
		t.Fatalf("enqueue bodies: %s", reqs)
	}
	var body map[string]any
	must(t, json.Unmarshal(reqs[0], &body))
	if body["session_ref"] != cid1 || body["session_total"].(float64) != 1 || body["client_attempt_id"] != cid1 {
		t.Errorf("top-level session fields: %v", body)
	}
	for _, it := range body["items"].([]any) {
		if _, has := it.(map[string]any)["session_ref"]; has {
			t.Errorf("item-level session_ref is forbidden by the contract")
		}
	}
}

func TestQueueStateMappingEveryState(t *testing.T) {
	cases := []struct {
		to, opt, ui, want string
	}{
		{"queued", "", "waiting", "待解答"},
		{"claimed", "", "ai_working", "AI 判卷中"},
		{"proposed", "", "ai_working", "AI 判卷中"},
		{"compared", "match", "ai_first_pass", "AI 初评 · 待家长核"},
		{"compared", "mismatch", "ai_first_pass", "不一致"},
		{"judged", "n/a", "ai_first_pass", "没法和你的答案比对"},
		{"reviewed", "", "reviewed", "家长已核"},
		{"failed", "", "failed", "处理失败"},
		{"expired", "", "expired", "仍没有结果，已转人工"},
		{"purged", "", "purged", "家长已删除这道题的解析"},
	}
	for _, c := range cases {
		t.Run(c.to+"-"+c.opt, func(t *testing.T) {
			e, _, ck := queueEnv(t, opts{})
			e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
			o := map[string]string{"compare": c.opt}
			if c.to == "compared" || c.to == "judged" || c.to == "reviewed" {
				o["answer"], o["rationale"] = "AI-ANSWER", "AI-WHY"
			}
			must(t, e.brain.q.Advance(0, q3, c.to, o))
			it := e.qview(t, ck, cid1).item(q3)
			if it.UI != c.ui || !strings.Contains(it.Label+it.CompareText, c.want) {
				t.Fatalf("%s: %+v", c.to, it)
			}
			if (c.ui == "ai_first_pass") != (it.Result != nil) && c.ui != "reviewed" {
				t.Errorf("result presence: %+v", it)
			}
			if c.ui == "purged" && it.Result != nil {
				t.Errorf("purged item must carry no text")
			}
		})
	}
}

func TestQueueFirstPassShowsFullTextIncludingMathAnswer(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	must(t, e.brain.q.Advance(0, q3, "compared", map[string]string{"answer": "x = 12", "rationale": "逐步推导：…（很长的解析）", "compare": "mismatch"}))
	resp, b := e.do(t, "GET", "/api/practice/attempts/"+cid1+"/queue", "", nil, ck)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "x = 12") || !strings.Contains(string(b), "很长的解析") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if e.brain.q.Counter("detail_with_result")+e.brain.q.Counter("batch_calls") == 0 {
		t.Errorf("the view must read the brain with include=result")
	}
}

func TestQueueTimeoutCopyMathAndOpen(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.brain.q.SetKind(q4, "judged")
	e.brain.q.Set("judged", "1")
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text", q4: []string{"x=5"}}))
	must(t, e.brain.q.Advance(0, q3, "expired", nil))
	must(t, e.brain.q.Advance(0, q4, "expired", nil))
	v := e.qview(t, ck, cid1)
	if !strings.Contains(v.item(q3).Label, "超过 24 小时仍没有结果，已转人工") || !strings.Contains(v.item(q4).Label, "超过 48 小时仍没有结果，已转人工") {
		t.Fatalf("copy: %q / %q", v.item(q3).Label, v.item(q4).Label)
	}
}

func TestQueueOverdueHintBeforeBrainSaysExpired(t *testing.T) {
	now := time.Now()
	clock := &now
	e, _, ck := queueEnv(t, opts{now: func() time.Time { return *clock }})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	later := now.Add(25 * time.Hour)
	*clock = later
	it := e.qview(t, ck, cid1).item(q3)
	if it.UI != "overdue_hint" || !strings.Contains(it.Label, "已超过预期时间") {
		t.Fatalf("%+v", it)
	}
}

func TestQueueNoBlindTryWhenKindNotEnabled(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.brain.q.SetKind(q3, "judged") // judged is OFF at the brain
	_, q := e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text", q4: []string{"x=5"}}))
	if q.enq(q3) != "not_queued" || q.enq(q4) != "ok" {
		t.Fatalf("%+v", q.Queue)
	}
	if got := e.brain.q.Counter("enqueue_calls"); got != 1 {
		t.Errorf("enqueue calls %d (only the queueable item may be sent)", got)
	}
	v := e.qview(t, ck, cid1)
	if v.item(q3).UI != "not_queued" || !strings.Contains(v.item(q3).Label, "不入大脑") {
		t.Errorf("%+v", v.item(q3))
	}
}

func TestQueueWholeBatchRejectedThenDropAndResubmit(t *testing.T) {
	e, _, ck := queueEnv(t, opts{queueNoHint: true}) // no hints: the site has to find out from the 422
	e.brain.q.SetKind(q5, "judged")
	_, q := e.qgrade(t, ck, "tri-3", triAnswers(cid1))
	if q.enq(q3) != "ok" || q.enq(q4) != "ok" || q.enq(q5) != "not_queued" || q.Queue.Enqueue != "partial" {
		t.Fatalf("%+v", q.Queue)
	}
	mq := e.brain.q
	reqs := mq.Requests()
	if len(reqs) != 2 || mq.Counter("enqueue_rejected") != 1 {
		t.Fatalf("calls %d rejected %d", len(reqs), mq.Counter("enqueue_rejected"))
	}
	var first, second map[string]any
	must(t, json.Unmarshal(reqs[0], &first))
	must(t, json.Unmarshal(reqs[1], &second))
	if first["session_total"].(float64) != 3 || second["session_total"].(float64) != 2 || strings.Contains(string(reqs[1]), q5) {
		t.Errorf("resubmit: total %v -> %v, body %s", first["session_total"], second["session_total"], reqs[1])
	}
	if mq.Counter("enqueue_new_item") != 2 {
		t.Errorf("items written: %d", mq.Counter("enqueue_new_item"))
	}
}

func TestQueueEveryItemRejectedWritesNothing(t *testing.T) {
	e, _, ck := queueEnv(t, opts{queueNoHint: true})
	e.brain.q.SetKind(q3, "judged")
	e.brain.q.SetKind(q4, "judged")
	_, q := e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text", q4: []string{"x=5"}}))
	if q.enq(q3) != "not_queued" || q.enq(q4) != "not_queued" || q.Queue.Enqueue != "failed" {
		t.Fatalf("%+v", q.Queue)
	}
	if e.brain.q.Counter("enqueue_new_item") != 0 || e.brain.q.Counter("enqueue_new_attempt") != 0 {
		t.Errorf("a refused batch must write nothing")
	}
}

func TestQueueEnqueueTimeout3sThenAutoRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 3 s")
	}
	e, _, ck := queueEnv(t, opts{})
	e.brain.q.Set("slow_ms", "3600")
	e.brain.q.Set("enqueue_mode", "slow")
	t0 := time.Now()
	g, q := e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	if d := time.Since(t0); d < 2800*time.Millisecond || d > 5*time.Second {
		t.Errorf("grade took %v (enqueue budget is 3 s)", d)
	}
	if g.RecordedCount != 1 || q.enq(q3) != "failed" || !g.Archived {
		t.Fatalf("a slow enqueue must not fail the submission: %+v %+v", g, q.Queue)
	}
	v := e.qview(t, ck, cid1)
	if v.item(q3).UI != "enqueue_retry" || !v.Summary.AnyPending {
		t.Fatalf("%+v", v.item(q3))
	}
	e.brain.q.Set("enqueue_mode", "")
	resp, b := e.do(t, "POST", "/api/practice/attempts/"+cid1+"/enqueue", "{}", map[string]string{"Content-Type": "application/json"}, ck)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if got := e.qview(t, ck, cid1).item(q3).UI; got != "waiting" {
		t.Errorf("after retry: %s", got)
	}
}

func TestQueueLostReplyRetryIsIdempotent(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.brain.q.Set("enqueue_mode", "lost") // brain wrote it, the reply was lost
	_, q := e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text", q4: []string{"x=5"}}))
	if q.enq(q3) != "failed" || q.enq(q4) != "failed" {
		t.Fatalf("%+v", q.Queue)
	}
	mq := e.brain.q
	newItems := mq.Counter("enqueue_new_item")
	attempts := e.brain.count("queue_new_attempt") + e.brain.count("grade_new_item")
	resp, b := e.do(t, "POST", "/api/practice/attempts/"+cid1+"/enqueue", "{}", map[string]string{"Content-Type": "application/json"}, ck)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if mq.Counter("enqueue_new_item") != newItems || mq.Counter("enqueue_replay_item") != 2 {
		t.Errorf("the replay must create nothing (new %d, replay %d)", mq.Counter("enqueue_new_item")-newItems, mq.Counter("enqueue_replay_item"))
	}
	if got := e.brain.count("queue_new_attempt") + e.brain.count("grade_new_item"); got != attempts {
		t.Errorf("attempts written twice: %d -> %d", attempts, got)
	}
	v := e.qview(t, ck, cid1)
	if v.item(q3).UI != "waiting" || v.item(q4).UI != "waiting" {
		t.Errorf("%+v", v.Items)
	}
	// and a third POST has nothing left to do
	calls := mq.Counter("enqueue_calls")
	e.do(t, "POST", "/api/practice/attempts/"+cid1+"/enqueue", "{}", map[string]string{"Content-Type": "application/json"}, ck)
	if mq.Counter("enqueue_calls") != calls {
		t.Errorf("nothing failed any more, no call expected")
	}
}

func TestQueueSweepRetriesDueEnqueueWithBackoff(t *testing.T) {
	now := time.Now()
	clock := &now
	e, _, ck := queueEnv(t, opts{now: func() time.Time { return *clock }})
	e.brain.q.Set("enqueue_mode", "500")
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	e.brain.q.Set("enqueue_mode", "")
	calls := e.brain.q.Counter("enqueue_calls")
	e.app.SweepOnce(context.Background())
	if e.brain.q.Counter("enqueue_calls") != calls {
		t.Fatalf("sweep must wait for next_try")
	}
	*clock = now.Add(2 * time.Minute)
	e.app.SweepOnce(context.Background())
	if e.qview(t, ck, cid1).item(q3).UI != "waiting" {
		t.Fatalf("sweep did not retry")
	}
}

func TestQueueEnqueueRetryPostIsRateLimited(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.brain.q.Set("enqueue_mode", "500")
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	codes := map[int]int{}
	for i := 0; i < 5; i++ {
		resp, _ := e.do(t, "POST", "/api/practice/attempts/"+cid1+"/enqueue", "{}", map[string]string{"Content-Type": "application/json"}, ck)
		codes[resp.StatusCode]++
	}
	if codes[429] == 0 {
		t.Errorf("no rate limit: %v", codes)
	}
}

func TestQueueUnknownCidAndOffAre404(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	resp, _ := e.do(t, "GET", "/api/practice/attempts/"+cid2+"/queue", "", nil, ck)
	if resp.StatusCode != 404 {
		t.Errorf("unknown cid %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/api/practice/attempts/not-a-uuid/queue", "", nil, ck)
	if resp.StatusCode != 404 && resp.StatusCode != 400 {
		t.Errorf("bad cid %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/api/practice/attempts/"+cid1+"/queue", "", nil, "")
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Errorf("no session: %d", resp.StatusCode)
	}
}

func TestQueueCapabilitiesAreUsedAndGetsNeverWrite(t *testing.T) {
	e, _, ck := queueEnv(t, opts{queueNotify: true})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	before := e.brain.q.Counter("enqueue_calls")
	for i := 0; i < 3; i++ {
		e.qview(t, ck, cid1)
		e.do(t, "GET", "/api/practice/sets/pend-2", "", nil, ck)
	}
	if e.brain.q.Counter("enqueue_calls") != before {
		t.Errorf("opening a page / viewing status enqueued something")
	}
	if e.brain.q.Counter("write_token_on_events_endpoint") != 0 || e.brain.q.Counter("notify_token_on_write_endpoint") != 0 || e.brain.q.Counter("stats_calls") != 0 {
		t.Errorf("token separation broken: %v", e.brain.q.Stats)
	}
}

func TestQueueQuotaCountsEnqueueWrittenAttemptsAndUsesBrainRemaining(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text", q4: []string{"x=5"}}))
	code, q := e.quota(t, ck, "pend-2")
	if code != 200 || q.Used[q3] != 1 || q.Used[q4] != 1 || q.Basis != "brain_daily_remaining" {
		t.Fatalf("%d %+v", code, q)
	}
	// a second submission of q4 (new cid): the brain says 1 left after it
	e.qgrade(t, ck, "pend-2", answersJSON(cid2, map[string]any{q4: []string{"x=6"}}))
	_, q = e.quota(t, ck, "pend-2")
	if q.Used[q4] != 2 {
		t.Errorf("used %v", q.Used)
	}
}

func TestQueueGradeReplayKeepsQueueStateAndNeverReEnqueues(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	body := answersJSON(cid1, map[string]any{q3: "free text"})
	e.qgrade(t, ck, "pend-2", body)
	calls := e.brain.q.Counter("enqueue_calls")
	resp, b := e.do(t, "POST", "/api/practice/pend-2/grade", body, map[string]string{"Content-Type": "application/json"}, ck)
	var q qGrade
	must(t, json.Unmarshal(b, &q))
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"replayed":true`) || q.enq(q3) != "ok" {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if e.brain.q.Counter("enqueue_calls") != calls {
		t.Errorf("a replay enqueued again")
	}
}
