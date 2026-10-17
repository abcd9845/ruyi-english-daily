package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func gradeClient(t *testing.T, h http.HandlerFunc, gradeToken, path string) *Client {
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	c, err := New(Config{BaseURL: u, Token: "read-tok", GradeToken: gradeToken, GradePath: path})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var gReq = GradeRequest{ClientAttemptID: "cid-1", Items: []GradeItem{
	{QRef: "a", Answer: "B"}, {QRef: "b", Answer: []string{"1", "2"}}, {QRef: "c", Answer: "free"},
}}

func reply(w http.ResponseWriter, ct, body string) {
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write([]byte(body))
}

const okReply = `{"client_attempt_id":"cid-1","items":[
	{"qref":"a","status":"graded","correct":true,"attempt_id":11,"recorded":true,"duplicate":false},
	{"qref":"b","status":"graded","correct":false,"attempt_id":12,"recorded":true,"duplicate":true},
	{"qref":"c","status":"pending_key","attempt_id":13,"recorded":true,"duplicate":false},
	{"qref":"zzz","status":"graded","correct":true,"attempt_id":14,"recorded":true},
	{"qref":"a","status":"graded","correct":false,"attempt_id":15,"recorded":true}]}`

func TestGradeWireContract(t *testing.T) {
	var gotAuth, gotCT, gotPath string
	var body map[string]any
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotPath = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		reply(w, "application/json", okReply)
	}, "grade-tok", "")
	req := gReq
	at := time.Date(2026, 10, 17, 12, 30, 5, 0, time.FixedZone("x", 8*3600))
	req.Items = append([]GradeItem(nil), gReq.Items...)
	req.Items[0] = GradeItem{QRef: "a", Answer: "B", Rev: 3, AttemptKind: AttemptCorrect, AnsweredAt: at, SessionRef: "set-1", Payload: map[string]any{"k": 1}}
	res, err := c.Grade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer grade-tok" || gotCT != "application/json" || gotPath != DefaultGradePath {
		t.Errorf("auth=%q ct=%q path=%q", gotAuth, gotCT, gotPath)
	}
	if len(body) != 3 || body["brain"] != "ruyi" || body["client_attempt_id"] != "cid-1" || len(body["items"].([]any)) != 3 {
		t.Errorf("request body: %v", body)
	}
	i0 := body["items"].([]any)[0].(map[string]any)
	if i0["rev"] != float64(3) || i0["attempt_kind"] != "correction" || i0["answered_at"] != "2026-10-17T04:30:05Z" || i0["session_ref"] != "set-1" || i0["payload"] == nil {
		t.Errorf("item 0: %v", i0)
	}
	i1 := body["items"].([]any)[1].(map[string]any) // optional fields absent when unset; blanks are an array
	if _, has := i1["rev"]; has || i1["answer"].([]any)[1] != "2" || len(i1) != 2 {
		t.Errorf("item 1: %v", i1)
	}
	if len(res.Results) != 3 || res.RecordedCount() != 3 {
		t.Fatalf("%+v", res)
	}
	r0, r1, r2 := res.Results[0], res.Results[1], res.Results[2]
	if r0.Correct == nil || !*r0.Correct || r0.AttemptID != 11 || !r0.Recorded || r0.Duplicate ||
		r1.Correct == nil || *r1.Correct || !r1.Duplicate || r1.AttemptID != 12 ||
		r2.Status != StatusPendingKey || r2.Correct != nil || !r2.Recorded || r2.AttemptID != 13 {
		t.Errorf("results: %+v", res.Results)
	}
}

func TestGradeStatusMapping(t *testing.T) {
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "application/json", `{"client_attempt_id":"cid-1","items":[
			{"qref":"a","status":"needs_review","attempt_id":5,"recorded":true,"duplicate":false},
			{"qref":"b","status":"unsupported","recorded":true,"attempt_id":6},
			{"qref":"c","status":"graded","correct":true,"recorded":false}]}`)
	}, "g", "")
	res, err := c.Grade(context.Background(), gReq)
	if err != nil {
		t.Fatal(err)
	}
	a, b, cc := res.Results[0], res.Results[1], res.Results[2]
	if a.Status != StatusNeedsReview || !a.Recorded || a.Correct != nil {
		t.Errorf("needs_review: %+v", a)
	}
	// unsupported is never "recorded", whatever the reply says
	if b.Status != StatusUnsupport || b.Recorded || b.AttemptID != 0 {
		t.Errorf("unsupported: %+v", b)
	}
	// a verdict that the brain did not record is not shown
	if cc.Status != StatusPendingKey || cc.Correct != nil || cc.Recorded {
		t.Errorf("graded but not recorded: %+v", cc)
	}
	if res.RecordedCount() != 1 {
		t.Errorf("recorded count %d", res.RecordedCount())
	}
}

func TestGradeCustomPathAndBrainID(t *testing.T) {
	var p string
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		reply(w, "application/json", `{"client_attempt_id":"cid-1","items":[]}`)
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "r", GradeToken: "g", GradePath: "/v9/attempts/grade", BrainID: "other"})
	if _, err := c.Grade(context.Background(), gReq); err != nil || p != "/v9/attempts/grade" || body["brain"] != "other" {
		t.Errorf("%v %q %v", err, p, body)
	}
}

func TestGradeDisabledWithoutToken(t *testing.T) {
	called := false
	c := gradeClient(t, func(http.ResponseWriter, *http.Request) { called = true }, "", "")
	if _, err := c.Grade(context.Background(), gReq); !errors.Is(err, ErrGradeDisabled) || called {
		t.Errorf("err=%v called=%v", err, called)
	}
}

func TestValidateGradeRequest(t *testing.T) {
	many := func(n int) []GradeItem {
		var it []GradeItem
		for i := 0; i < n; i++ {
			it = append(it, GradeItem{QRef: "q" + strings.Repeat("x", i), Answer: "A"})
		}
		return it
	}
	blanks := func(n int) []string { return make([]string, n) }
	ok := map[string]GradeRequest{
		"50 items":   {ClientAttemptID: "a", Items: many(50)},
		"10 blanks":  {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: blanks(10)}}},
		"id charset": {ClientAttemptID: "a.B_c:d-1", Items: many(1)},
		"160 chars":  {ClientAttemptID: strings.Repeat("a", 160), Items: many(1)},
		"variant":    {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: "A", AttemptKind: AttemptVariant}}},
	}
	for n, r := range ok {
		if err := ValidateGradeRequest(r); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	bad := map[string]GradeRequest{
		"51 items":     {ClientAttemptID: "a", Items: many(51)},
		"no items":     {ClientAttemptID: "a"},
		"11 blanks":    {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: blanks(11)}}},
		"empty id":     {Items: many(1)},
		"161 chars":    {ClientAttemptID: strings.Repeat("a", 161), Items: many(1)},
		"bad charset":  {ClientAttemptID: "a b", Items: many(1)},
		"slash in id":  {ClientAttemptID: "a/b", Items: many(1)},
		"dup qref":     {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: "A"}, {QRef: "q", Answer: "B"}}},
		"empty qref":   {ClientAttemptID: "a", Items: []GradeItem{{Answer: "A"}}},
		"answer type":  {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: 5}}},
		"nil answer":   {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q"}}},
		"attempt kind": {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: "A", AttemptKind: "cheat"}}},
		"negative rev": {ClientAttemptID: "a", Items: []GradeItem{{QRef: "q", Answer: "A", Rev: -1}}},
	}
	for n, r := range bad {
		err := ValidateGradeRequest(r)
		var be *Error
		if !errors.As(err, &be) || be.Kind != KindBadRequest {
			t.Errorf("%s: %v", n, err)
		}
	}
	// and Grade refuses them without calling the brain
	called := false
	c := gradeClient(t, func(http.ResponseWriter, *http.Request) { called = true }, "g", "")
	if _, err := c.Grade(context.Background(), bad["51 items"]); err == nil || called {
		t.Errorf("err=%v called=%v", err, called)
	}
}

func TestGradeRequiresBrainConfirmation(t *testing.T) {
	bad := map[string]string{
		"other client id": `{"client_attempt_id":"someone-else","items":[]}`,
		"no client id":    `{"items":[]}`,
		"not json":        `<html>`,
	}
	for name, body := range bad {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, "application/json", body) }, "g", "")
		if _, err := c.Grade(context.Background(), gReq); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// wrong content type, redirect, non-200 are all failures too
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "text/html", `{"client_attempt_id":"cid-1","items":[]}`)
	}, "g", "")
	if _, err := c.Grade(context.Background(), gReq); err == nil {
		t.Error("html content-type accepted")
	}
	var hits int
	c = gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/elsewhere" {
			reply(w, "application/json", `{"client_attempt_id":"cid-1","items":[]}`)
			return
		}
		http.Redirect(w, r, "/elsewhere", 302)
	}, "g", "")
	if _, err := c.Grade(context.Background(), gReq); err == nil || hits != 1 {
		t.Errorf("redirect followed: err=%v hits=%d", err, hits)
	}
}

func TestGradeErrorKinds(t *testing.T) {
	type tc struct {
		status int
		body   string
		kind   Kind
		code   string
	}
	for _, x := range []tc{
		{404, `{"error":{"code":"unknown_qref"}}`, KindNotFound, "unknown_qref"},
		{404, `404 page not found`, KindNotFound, ""},
		{501, ``, KindNotReady, ""},
		{405, ``, KindNotReady, ""},
		{409, `{"error":{"code":"rev_mismatch"}}`, KindConflict, "rev_mismatch"},
		{409, `{"error":{"code":"idempotency_conflict"}}`, KindConflict, "idempotency_conflict"},
		{429, `{"error":{"code":"rate_limited"}}`, KindRateLimited, "rate_limited"},
		{401, ``, KindAuth, ""},
		{403, ``, KindAuth, ""},
		{500, ``, KindUnavailable, ""},
	} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(x.status); _, _ = w.Write([]byte(x.body)) }, "grade-tok", "")
		c.retryDelay = time.Millisecond
		_, err := c.Grade(context.Background(), gReq)
		var be *Error
		if !errors.As(err, &be) || be.Kind != x.kind || be.Code != x.code || be.Status != x.status {
			t.Errorf("%d %q: %+v", x.status, x.body, err)
			continue
		}
		if strings.Contains(be.Error(), "grade-tok") || strings.Contains(be.Error(), "127.0.0.1") {
			t.Errorf("error leaks: %v", be)
		}
	}
}

func TestGradeRetriesOnlyTransientFailuresWithTheSameBody(t *testing.T) {
	var bodies []string
	var n int
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		n++
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		reply(w, "application/json", okReply)
	}, "g", "")
	c.retryDelay = time.Millisecond
	res, err := c.Grade(context.Background(), gReq)
	if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || res.RecordedCount() != 3 {
		t.Fatalf("err=%v bodies=%d", err, len(bodies))
	}
	// 4xx, 501 and malformed replies are not retried
	for _, code := range []int{400, 401, 404, 409, 429, 501} {
		n = 0
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { n++; w.WriteHeader(code) }, "g", "")
		c.retryDelay = time.Millisecond
		_, _ = c.Grade(context.Background(), gReq)
		if n != 1 {
			t.Errorf("%d retried %d times", code, n)
		}
	}
	n = 0
	c = gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		reply(w, "application/json", `{"client_attempt_id":"zz","items":[]}`)
	}, "g", "")
	_, _ = c.Grade(context.Background(), gReq)
	if n != 1 {
		t.Errorf("malformed reply retried: %d", n)
	}
}

func TestGradeUnknownStatusAndMissingItemsStayPending(t *testing.T) {
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "application/json", `{"client_attempt_id":"cid-1","items":[
			{"qref":"a","status":"graded","correct":null,"recorded":true,"attempt_id":3},{"qref":"b","status":"weird","correct":true,"recorded":true,"attempt_id":4}]}`)
	}, "g", "")
	res, err := c.Grade(context.Background(), gReq)
	if err != nil || len(res.Results) != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	for _, r := range res.Results {
		if r.Status != StatusPendingKey || r.Correct != nil {
			t.Errorf("must stay pending without a verdict: %+v", r)
		}
	}
	if res.Results[1].Recorded || res.Results[2].Recorded {
		t.Errorf("unknown status / skipped items must not be claimed as recorded: %+v", res.Results)
	}
}

func TestSelectForPractice(t *testing.T) {
	list := `{"items":[
	 {"qref":"q-10","source":"s.x","number":"10","stem":"s","media":[]},
	 {"qref":"q-2b","source":"s.x","number":"2(1)","stem":"s","media":[]},
	 {"qref":"q-1","source":"s.x","number":"1","stem":"s","media":[]},
	 {"qref":"q-x","source":"s.x","stem":"s","media":[]},
	 {"qref":"q-2","source":"s.x","number":"2","stem":"s","media":[]}],"next_cursor":null}`
	var calls int
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; reply(w, "application/json", list) }, "g", "")
	ctx := context.Background()
	got, err := c.SelectForPractice(ctx, "s.x", 3, SelectOpts{Order: "seq"})
	if err != nil || picks(got) != "q-1,q-2,q-2b" {
		t.Errorf("seq: %v %v", got, err)
	}
	all, _ := c.SelectForPractice(ctx, "s.x", 99, SelectOpts{Order: "seq"})
	if picks(all) != "q-1,q-2,q-2b,q-10,q-x" {
		t.Errorf("seq all (unnumbered last): %v", all)
	}
	// the cached list is not reordered by selection
	if l, _ := c.ListBySource(ctx, "s.x"); l[0].QRef != "q-2b" || l[1].QRef != "q-10" || l[2].QRef != "q-1" {
		t.Errorf("selection reordered the cache: %s %s %s", l[0].QRef, l[1].QRef, l[2].QRef)
	}
	seed := int64(7)
	a, _ := c.SelectForPractice(ctx, "s.x", 4, SelectOpts{Seed: &seed})
	b, _ := c.SelectForPractice(ctx, "s.x", 4, SelectOpts{Seed: &seed})
	if picks(a) != picks(b) || len(a) != 4 {
		t.Errorf("seeded: %v %v", a, b)
	}
}

func picks(p []Pick) string {
	var r []string
	for _, x := range p {
		r = append(r, x.QRef)
	}
	return strings.Join(r, ",")
}
