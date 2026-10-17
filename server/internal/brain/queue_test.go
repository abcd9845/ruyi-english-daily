package brain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/mockqueue"
)

type qenv struct {
	mu  sync.Mutex
	ids map[string]int64
	cnt map[string]int
	seq int64
}

func (e *qenv) Lookup(q string) (string, bool) { return "pending_key", strings.HasPrefix(q, "ex:") }
func (e *qenv) Attempt(k string) (int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, ok := e.ids[k]
	return id, ok
}
func (e *qenv) DayCount(q string) int { e.mu.Lock(); defer e.mu.Unlock(); return e.cnt[q] }
func (e *qenv) Write(k, q, _ string, _ time.Time) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	e.ids[k] = e.seq
	e.cnt[q]++
	return e.seq
}
func (e *qenv) Now() time.Time { return time.Now() }

func queueClient(t *testing.T, notify string) (*Client, *mockqueue.Mock, *qenv) {
	env := &qenv{ids: map[string]int64{}, cnt: map[string]int{}}
	m := mockqueue.New(mockqueue.Config{Brain: "ruyi", WriteToken: "w-token", NotifyToken: "n-token", Env: env})
	mux := http.NewServeMux()
	m.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c, err := New(Config{BaseURL: u, Token: "r-token", GradeToken: "w-token", NotifyToken: notify})
	if err != nil {
		t.Fatal(err)
	}
	return c, m, env
}

// one fixed answered_at for the whole test binary: the same key replayed must carry the same value
var fixedAnswered = time.Now().Add(-time.Minute).UTC().Truncate(time.Second)

func req(cid string, total int, qs ...string) EnqueueRequest {
	r := EnqueueRequest{ClientAttemptID: cid, SessionRef: cid, SessionTotal: total}
	for _, q := range qs {
		r.Items = append(r.Items, EnqueueItem{QRef: q, Answer: "a", Rev: 1, AnsweredAt: fixedAnswered})
	}
	return r
}

func TestEnqueueSuccessCarriesEveryContractField(t *testing.T) {
	c, m, _ := queueClient(t, "n-token")
	resp, err := c.Enqueue(context.Background(), req("cid-aaaaaaaa", 2, "ex:1", "ex:2"))
	if err != nil || len(resp.Items) != 2 {
		t.Fatalf("%v %+v", err, resp)
	}
	it := resp.Items[0]
	if it.ItemID == "" || it.State != "queued" || !it.Created || it.AttemptID == 0 || !it.NewAttemptWritten || it.DailyRemaining != 2 || it.Kind != "absolute" || it.DeadlineAt.IsZero() {
		t.Errorf("%+v", it)
	}
	if m.Counter("enqueue_400") != 0 {
		t.Errorf("the strict mock rejected the body")
	}
	// replay: same body, nothing new
	resp, err = c.Enqueue(context.Background(), req("cid-aaaaaaaa", 2, "ex:1", "ex:2"))
	if err != nil || resp.Items[0].Created || resp.Items[0].NewAttemptWritten {
		t.Errorf("replay: %v %+v", err, resp)
	}
}

func TestEnqueueRefusedBatchIsTypedWithPerItemVerdicts(t *testing.T) {
	c, m, _ := queueClient(t, "n-token")
	m.SetKind("ex:2", "judged") // judged is off
	_, err := c.Enqueue(context.Background(), req("cid-bbbbbbbb", 2, "ex:1", "ex:2", "nope:3"))
	var qe *QueueError
	if !errors.As(err, &qe) {
		t.Fatalf("%v", err)
	}
	if len(qe.Items) != 3 || m.Counter("enqueue_new_item") != 0 {
		t.Fatalf("%+v", qe)
	}
	byQ := map[string]Reject{}
	for _, r := range qe.Items {
		byQ[r.QRef] = r
	}
	if !byQ["ex:1"].OK || byQ["ex:1"].Permanent() {
		t.Errorf("the good item is fine: %+v", byQ["ex:1"])
	}
	if !byQ["ex:2"].Permanent() || byQ["ex:2"].Why() != "kind_not_enabled" || !byQ["nope:3"].Permanent() || byQ["nope:3"].Why() != "question_not_found" {
		t.Errorf("%+v", byQ)
	}
}

func TestEnqueue429CarriesRetryAfterFromBodyAndHeader(t *testing.T) {
	c, m, _ := queueClient(t, "n-token")
	m.Set("open_cap", "1")
	_, err := c.Enqueue(context.Background(), req("cid-cccccccc", 2, "ex:1", "ex:2"))
	var qe *QueueError
	if !errors.As(err, &qe) || qe.Reason != "queue_full" || qe.RetryAfterS != 60 {
		t.Fatalf("%v %+v", err, qe)
	}
	for _, r := range qe.Items {
		if r.Permanent() {
			t.Errorf("queue_full is transient: %+v", r)
		}
	}
}

func TestEnqueueSessionTotalMismatchIs400ForTheWholeRequest(t *testing.T) {
	c, _, _ := queueClient(t, "n-token")
	if _, err := c.Enqueue(context.Background(), req("cid-dddddddd", 2, "ex:1", "ex:2")); err != nil {
		t.Fatal(err)
	}
	_, err := c.Enqueue(context.Background(), req("cid-dddddddd", 3, "ex:3"))
	var qe *QueueError
	if !errors.As(err, &qe) || qe.Reason != "session_total_mismatch" || qe.Base.Status != 400 || len(qe.Items) != 0 {
		t.Fatalf("%v %+v", err, qe)
	}
	// the same total again (a retry) is fine
	if _, err := c.Enqueue(context.Background(), req("cid-dddddddd", 2, "ex:1", "ex:2")); err != nil {
		t.Fatalf("same total must pass: %v", err)
	}
}

func TestEnqueueRejectsMalformedRequestsClientSide(t *testing.T) {
	c, _, _ := queueClient(t, "n-token")
	for name, r := range map[string]EnqueueRequest{
		"no items":       {ClientAttemptID: "cid-eeeeeeee", SessionRef: "cid-eeeeeeee", SessionTotal: 1},
		"total -1":       req("cid-eeeeeeee", -1, "ex:1"),
		"total 101":      req("cid-eeeeeeee", 101, "ex:1"),
		"duplicate qref": req("cid-eeeeeeee", 2, "ex:1", "ex:1"),
	} {
		if _, err := c.Enqueue(context.Background(), r); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPendingReadsDetailBatchAndCapabilities(t *testing.T) {
	c, m, _ := queueClient(t, "n-token")
	resp, err := c.Enqueue(context.Background(), req("cid-ffffffff", 2, "ex:1", "ex:2"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(m.Advance(0, "ex:1", "compared", map[string]string{"answer": "AI-A", "rationale": "AI-R", "compare": "mismatch"}))
	d, err := c.PendingDetail(context.Background(), resp.Items[0].ItemID, false)
	must(err)
	if d.State != "compared" || d.ProposedAnswer != "" || d.HasResult || d.CompareOutcome != "mismatch" || !d.NeedsManual {
		t.Errorf("detail without include=result carries no text: %+v", d)
	}
	d, err = c.PendingDetail(context.Background(), resp.Items[0].ItemID, true)
	must(err)
	if d.ProposedAnswer != "AI-A" || d.Rationale != "AI-R" || !d.HasResult {
		t.Errorf("%+v", d)
	}
	sr, err := c.PendingBySession(context.Background(), "cid-ffffffff", true)
	must(err)
	if sr.Total != 2 || sr.Processed != 1 || sr.Done || len(sr.Items) != 2 {
		t.Errorf("%+v", sr)
	}
	caps, err := c.Capabilities(context.Background())
	must(err)
	if !caps.AbsoluteEnabled || caps.AbsoluteHours != 24 || caps.JudgedEnabled || caps.JudgedHours != 48 || caps.ImagesEnabled || caps.Hours("judged") != 48 {
		t.Errorf("%+v", caps)
	}
	m.Set("batch_off", "1")
	if _, err = c.PendingBySession(context.Background(), "cid-ffffffff", true); err == nil {
		t.Errorf("a missing batch route must be an error the caller can fall back from")
	}
}

func TestEventsAndAckUseTheNotifyTokenOnly(t *testing.T) {
	c, m, _ := queueClient(t, "n-token")
	if _, err := c.Enqueue(context.Background(), req("cid-gggggggg", 1, "ex:1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Advance(0, "ex:1", "compared", nil); err != nil {
		t.Fatal(err)
	}
	evs, err := c.QueueEvents(context.Background(), 0, 100)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%v %+v", err, evs)
	}
	e := evs[0]
	if e.Type != EventDone || e.SessionRef != "cid-gggggggg" || e.SessionTotal != 1 || e.SessionProcessed != 1 || !e.SessionDone || e.QRef != "ex:1" {
		t.Errorf("%+v", e)
	}
	if err := c.AckEvents(context.Background(), e.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := c.QueueEvents(context.Background(), 0, 100); len(again) != 0 {
		t.Errorf("acked events must not come back: %+v", again)
	}
	if err := c.AckEvents(context.Background(), e.ID+50); err == nil {
		t.Errorf("ack beyond head must be refused")
	}
	if m.Counter("write_token_on_events_endpoint") != 0 || m.Counter("notify_token_on_write_endpoint") != 0 {
		t.Errorf("tokens crossed: %v", m.Stats)
	}
}

func TestNoNotifyTokenMeansNoEventsAndNeverTheWriteToken(t *testing.T) {
	c, m, _ := queueClient(t, "")
	if _, err := c.QueueEvents(context.Background(), 0, 10); !errors.Is(err, ErrNotifyDisabled) {
		t.Errorf("%v", err)
	}
	if err := c.AckEvents(context.Background(), 1); !errors.Is(err, ErrNotifyDisabled) {
		t.Errorf("%v", err)
	}
	if m.Counter("events_calls")+m.Counter("ack_calls") != 0 {
		t.Errorf("the client called the brain without a notify token")
	}
}

func TestWrongNotifyTokenIsAnAuthErrorNotAFallback(t *testing.T) {
	c, m, _ := queueClient(t, "r-token") // the READ token as notify token: valid-looking, not granted
	_, err := c.QueueEvents(context.Background(), 0, 10)
	var be *Error
	if !errors.As(err, &be) || be.Kind != KindAuth {
		t.Fatalf("%v", err)
	}
	if m.Counter("write_token_on_events_endpoint") != 0 {
		t.Errorf("fallback to the write token")
	}
}

// ack wire format: the body is exactly {"up_to_id": <JSON integer>}; the brain refuses anything else with 400.
func TestAckBodyIsAJSONIntegerAndTheBrainRefusesOtherShapes(t *testing.T) {
	var got []byte
	rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"acked_up_to":9007199254740993}`))
	}))
	defer rec.Close()
	u, _ := url.Parse(rec.URL)
	c, err := New(Config{BaseURL: u, Token: "r-token", GradeToken: "w-token", NotifyToken: "n-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{7, 9007199254740993} { // beyond 2^53: still an integer on the wire, no float
		if err := c.AckEvents(context.Background(), n); err != nil {
			t.Fatal(err)
		}
		want := `{"up_to_id":` + strconv.FormatInt(n, 10) + `}`
		if string(got) != want {
			t.Fatalf("body %q, want %q", got, want)
		}
	}

	// the mock (the brain's rule) refuses a string, a float, ids[], null, a missing field: 400 validation_failed
	cl, m, _ := queueClient(t, "n-token")
	_ = cl
	mux := http.NewServeMux()
	m.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(body string) (int, string) {
		rq, _ := http.NewRequest(http.MethodPost, srv.URL+queueAckPath, strings.NewReader(body))
		rq.Header.Set("Authorization", "Bearer n-token")
		rq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 512)
		k, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:k])
	}
	for _, body := range []string{`{"up_to_id":"7"}`, `{"up_to_id":7.0}`, `{"up_to_id":null}`, `{}`, `{"ids":[1,2]}`, `{"up_to_id":[7]}`, `{"up_to_id":7,"ids":[7]}`} {
		if st, b := post(body); st != 400 || !strings.Contains(b, "validation_failed") {
			t.Errorf("%s -> %d %s, want 400 validation_failed", body, st, b)
		}
	}
	if st, b := post(`{"up_to_id":0}`); st != 422 {
		t.Errorf("a non-positive integer is a business answer: %d %s", st, b)
	}
}

// replaying the same key with another answered_at (or answer) is a 409 idempotency_conflict.
func TestEnqueueReplayWithAnotherAnsweredAtIs409(t *testing.T) {
	c, _, _ := queueClient(t, "n-token")
	r := req("cid-aa-1", 1, "ex:1")
	if _, err := c.Enqueue(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enqueue(context.Background(), r); err != nil { // the same replay is fine
		t.Fatalf("identical replay: %v", err)
	}
	r2 := r
	r2.Items = []EnqueueItem{r.Items[0]}
	r2.Items[0].AnsweredAt = r.Items[0].AnsweredAt.Add(-time.Minute)
	_, err := c.Enqueue(context.Background(), r2)
	var be *Error
	if !errors.As(err, &be) || be.Kind != KindConflict {
		t.Fatalf("a changed answered_at must be refused as a conflict: %v", err)
	}
}
