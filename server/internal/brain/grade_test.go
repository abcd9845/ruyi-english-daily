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

var gReq = GradeRequest{ClientAttemptID: "cid-1", SetID: "s1", Source: "s.x", Items: []GradeItem{
	{QRef: "a", Answer: "B"}, {QRef: "b", Answer: []string{"1", "2"}}, {QRef: "c", Answer: "free"},
}}

func reply(w http.ResponseWriter, ct, body string) {
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write([]byte(body))
}

func TestGradeWireContract(t *testing.T) {
	var gotAuth, gotCT, gotPath string
	var body map[string]any
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCT, gotPath = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		reply(w, "application/json", `{"client_attempt_id":"cid-1","attempt_id":"br-9","recorded":true,"items":[
			{"qref":"a","status":"graded","correct":true},
			{"qref":"b","status":"graded","correct":false},
			{"qref":"c","status":"pending_key","correct":null},
			{"qref":"zzz","status":"graded","correct":true},
			{"qref":"a","status":"graded","correct":false}]}`)
	}, "grade-tok", "")
	res, err := c.Grade(context.Background(), gReq)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer grade-tok" || gotCT != "application/json" || gotPath != DefaultGradePath {
		t.Errorf("auth=%q ct=%q path=%q", gotAuth, gotCT, gotPath)
	}
	if body["client_attempt_id"] != "cid-1" || body["set_id"] != "s1" || body["source"] != "s.x" || len(body["items"].([]any)) != 3 {
		t.Errorf("request body: %v", body)
	}
	if len(body) != 4 {
		t.Errorf("unexpected request fields: %v", body)
	}
	if res.BrainAttemptID != "br-9" || len(res.Results) != 3 {
		t.Fatalf("%+v", res)
	}
	if res.Results[0].Correct == nil || !*res.Results[0].Correct || res.Results[1].Correct == nil || *res.Results[1].Correct || res.Results[2].Status != StatusPendingKey || res.Results[2].Correct != nil {
		t.Errorf("results: %+v", res.Results)
	}
}

func TestGradeCustomPath(t *testing.T) {
	var p string
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		p = r.URL.Path
		reply(w, "application/json", `{"client_attempt_id":"cid-1","attempt_id":"x","recorded":true,"items":[]}`)
	}, "g", "/v9/attempts/grade")
	if _, err := c.Grade(context.Background(), gReq); err != nil || p != "/v9/attempts/grade" {
		t.Errorf("%v %q", err, p)
	}
}

func TestGradeDisabledWithoutToken(t *testing.T) {
	called := false
	c := gradeClient(t, func(http.ResponseWriter, *http.Request) { called = true }, "", "")
	if _, err := c.Grade(context.Background(), gReq); !errors.Is(err, ErrGradeDisabled) || called {
		t.Errorf("err=%v called=%v", err, called)
	}
}

func TestGradeRequiresBrainConfirmation(t *testing.T) {
	bad := map[string]string{
		"not recorded":    `{"client_attempt_id":"cid-1","attempt_id":"x","recorded":false,"items":[]}`,
		"no recorded":     `{"client_attempt_id":"cid-1","attempt_id":"x","items":[]}`,
		"no attempt id":   `{"client_attempt_id":"cid-1","recorded":true,"items":[]}`,
		"other client id": `{"client_attempt_id":"someone-else","attempt_id":"x","recorded":true,"items":[]}`,
		"no client id":    `{"attempt_id":"x","recorded":true,"items":[]}`,
		"not json":        `<html>`,
		"huge attempt id": `{"client_attempt_id":"cid-1","attempt_id":"` + strings.Repeat("x", 200) + `","recorded":true,"items":[]}`,
	}
	for name, body := range bad {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, "application/json", body) }, "g", "")
		if _, err := c.Grade(context.Background(), gReq); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// wrong content type, redirect, non-200 are all failures too
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "text/html", `{"client_attempt_id":"cid-1","attempt_id":"x","recorded":true,"items":[]}`)
	}, "g", "")
	if _, err := c.Grade(context.Background(), gReq); err == nil {
		t.Error("html content-type accepted")
	}
	var hits int
	c = gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/elsewhere" {
			reply(w, "application/json", `{"client_attempt_id":"cid-1","attempt_id":"x","recorded":true}`)
			return
		}
		http.Redirect(w, r, "/elsewhere", 302)
	}, "g", "")
	if _, err := c.Grade(context.Background(), gReq); err == nil || hits != 1 {
		t.Errorf("redirect followed: err=%v hits=%d", err, hits)
	}
	for _, code := range []int{401, 403, 404, 429, 500} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }, "g", "")
		_, err := c.Grade(context.Background(), gReq)
		var be *Error
		if !errors.As(err, &be) {
			t.Errorf("%d: %v", code, err)
		} else if strings.Contains(be.Error(), "grade-tok") || strings.Contains(be.Error(), "127.0.0.1") {
			t.Errorf("error leaks: %v", be)
		}
	}
}

func TestGradeUnknownStatusAndMissingItemsStayPending(t *testing.T) {
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "application/json", `{"client_attempt_id":"cid-1","attempt_id":"x","recorded":true,"items":[
			{"qref":"a","status":"graded","correct":null},{"qref":"b","status":"weird","correct":true}]}`)
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
	if err != nil || strings.Join(got, ",") != "q-1,q-2,q-2b" {
		t.Errorf("seq: %v %v", got, err)
	}
	all, _ := c.SelectForPractice(ctx, "s.x", 99, SelectOpts{Order: "seq"})
	if strings.Join(all, ",") != "q-1,q-2,q-2b,q-10,q-x" {
		t.Errorf("seq all (unnumbered last): %v", all)
	}
	// the cached list is not reordered by selection
	if l, _ := c.ListBySource(ctx, "s.x"); l[0].QRef != "q-2b" || l[1].QRef != "q-10" || l[2].QRef != "q-1" {
		t.Errorf("selection reordered the cache: %s %s %s", l[0].QRef, l[1].QRef, l[2].QRef)
	}
	seed := int64(7)
	a, _ := c.SelectForPractice(ctx, "s.x", 4, SelectOpts{Seed: &seed})
	b, _ := c.SelectForPractice(ctx, "s.x", 4, SelectOpts{Seed: &seed})
	if strings.Join(a, ",") != strings.Join(b, ",") || len(a) != 4 {
		t.Errorf("seeded: %v %v", a, b)
	}
	if _, err := c.SelectForPractice(ctx, "s.x", 3, SelectOpts{WrongOnly: true}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("wrong_only: %v", err)
	}
	if _, err := c.SelectForPractice(ctx, "s.x", 3, SelectOpts{DueFirst: true}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("due_first: %v", err)
	}
}

func TestLookupByQrefs(t *testing.T) {
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "application/json", `{"items":[{"qref":"a","source":"s.x","stem":"s","media":[]},{"qref":"b","source":"s.x","stem":"s","media":[]}],"next_cursor":null}`)
	}, "g", "")
	found, missing, err := c.LookupByQrefs(context.Background(), "s.x", []string{"b", "nope", "a"})
	if err != nil || len(found) != 2 || len(missing) != 1 || missing[0] != "nope" {
		t.Errorf("%v %v %v", found, missing, err)
	}
}
