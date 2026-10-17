package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Corrections against the brain's interface document and developer answers.

func errReply(w http.ResponseWriter, status int, hdr map[string]string, body string) {
	w.Header().Set("Content-Type", "application/json")
	for k, v := range hdr {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func TestErrorBodyIsParsedAndSanitized(t *testing.T) {
	cases := []struct {
		name   string
		status int
		hdr    map[string]string
		body   string
		check  func(*Error) bool
	}{
		{"grade 404 with details", 404, nil, `{"code":"not_found","message":"question not found","details":{"qrefs":["math:s:9","","x"]}}`,
			func(e *Error) bool {
				return e.HasQrefs() && !e.IsRouteNotFound() && len(e.Qrefs) == 2 && e.Message == "question not found"
			}},
		{"catch-all 404", 404, nil, `{"code":"not_found","message":"not found"}`, func(e *Error) bool { return e.IsRouteNotFound() && !e.HasQrefs() }},
		{"plain-text 404", 404, nil, `404 page not found`, func(e *Error) bool { return e.IsRouteNotFound() }},
		{"404 without body", 404, nil, ``, func(e *Error) bool { return e.IsRouteNotFound() }},
		{"pick source 404", 404, nil, `{"code":"not_found","message":"source not found"}`, func(e *Error) bool { return !e.IsRouteNotFound() && !e.HasQrefs() && e.Kind == KindNotFound }},
		{"404 with another code is not the catch-all", 404, nil, `{"code":"unknown_qref","message":"not found"}`, func(e *Error) bool { return !e.IsRouteNotFound() }},
		{"429 with Retry-After", 429, map[string]string{"Retry-After": "120"}, `{"code":"rate_limited","message":"daily grade limit reached","details":{"qrefs":["math:s:1"]}}`,
			func(e *Error) bool {
				return e.Kind == KindRateLimited && e.RetryAfter == 120*time.Second && e.Code == "rate_limited" && len(e.Qrefs) == 1
			}},
		{"429 absurd Retry-After is capped, junk ignored", 429, map[string]string{"Retry-After": "99999999"}, `{}`, func(e *Error) bool { return e.RetryAfter == 24*time.Hour }},
		{"Retry-After as a date is ignored", 429, map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"}, `{}`, func(e *Error) bool { return e.RetryAfter == 0 }},
		{"409 rev_mismatch details; index never parsed", 409, nil, `{"code":"rev_mismatch","message":"rev mismatch","details":{"qref":"math:s:2","rev":1,"current_rev":3,"index":0}}`,
			func(e *Error) bool {
				return e.Kind == KindConflict && e.Qref == "math:s:2" && e.Rev == 1 && e.CurrentRev == 3
			}},
		{"nested error object", 409, nil, `{"error":{"code":"idempotency_conflict","message":"conflict"}}`, func(e *Error) bool { return e.Code == "idempotency_conflict" && e.Message == "conflict" }},
		{"string error", 409, nil, `{"error":"rev_mismatch"}`, func(e *Error) bool { return e.Code == "rev_mismatch" }},
		{"400 is rejected, not unavailable", 400, nil, `{"code":"invalid_json","message":"invalid json"}`, func(e *Error) bool { return e.Kind == KindRejected && e.Code == "invalid_json" }},
		{"hostile message is dropped", 404, nil, `{"code":"not_found","message":"<script>alert(1)</script>"}`, func(e *Error) bool { return e.Message == "" }},
		{"hostile code is dropped", 409, nil, `{"code":"Bad Code!"}`, func(e *Error) bool { return e.Code == "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			errReply(rec, c.status, c.hdr, c.body)
			e := upstreamErr(rec.Result())
			if !c.check(e) {
				t.Errorf("%+v", e)
			}
			if strings.Contains(e.Error(), "math:s") {
				t.Errorf("Error() must not carry qrefs: %q", e.Error())
			}
		})
	}
}

func TestGradeAcceptsCreatedAndOK(t *testing.T) {
	for _, status := range []int{200, 201} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(okReply))
		}, "g", "")
		if resp, err := c.Grade(context.Background(), gReq); err != nil || resp.RecordedCount() != 3 {
			t.Errorf("%d: %v %+v", status, err, resp)
		}
	}
	for _, status := range []int{202, 204} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, "g", "")
		if _, err := c.Grade(context.Background(), gReq); err == nil {
			t.Errorf("%d accepted", status)
		}
	}
}

func TestGradeBodyIsStrictAndLowerCase(t *testing.T) {
	var raw []byte
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		if r.URL.RawQuery != "" {
			t.Errorf("query on grade: %q", r.URL.RawQuery)
		}
		reply(w, "application/json", `{"client_attempt_id":"cid-1","items":[{"qref":"math:src:1","status":"pending_key","attempt_id":1,"recorded":true,"duplicate":false},{"qref":"math:src:2","status":"graded","correct":true,"attempt_id":2,"recorded":true,"duplicate":false}]}`)
	}, "g", "")
	now := time.Now()
	req := GradeRequest{ClientAttemptID: "cid-1", Items: []GradeItem{
		{QRef: "Math:Src:1", Answer: "x", Rev: 0, AnsweredAt: now.Add(-8 * 24 * time.Hour), SessionRef: strings.Repeat("s", 201), Payload: map[string]any{"big": strings.Repeat("p", 5000)}},
		{QRef: "MATH:SRC:2", Answer: []string{"a"}, Rev: 3, AnsweredAt: now.Add(-time.Hour), SessionRef: "ps-1", Payload: map[string]any{"step": 1}, AttemptKind: "variant"},
	}}
	resp, err := c.Grade(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// results carry the caller's own strings
	if resp.Results[0].QRef != "Math:Src:1" || resp.Results[1].QRef != "MATH:SRC:2" || resp.RecordedCount() != 2 {
		t.Errorf("%+v", resp.Results)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var typed struct {
		Brain string `json:"brain"`
		CID   string `json:"client_attempt_id"`
		Items []struct {
			QRef        string          `json:"qref"`
			Answer      any             `json:"answer"`
			Rev         *int            `json:"rev"`
			AttemptKind string          `json:"attempt_kind"`
			AnsweredAt  *string         `json:"answered_at"`
			SessionRef  *string         `json:"session_ref"`
			Payload     json.RawMessage `json:"payload"`
		} `json:"items"`
	}
	if err := dec.Decode(&typed); err != nil {
		t.Fatalf("body has fields outside the contract: %v\n%s", err, raw)
	}
	i1, i2 := typed.Items[0], typed.Items[1]
	if i1.QRef != "math:src:1" || i2.QRef != "math:src:2" {
		t.Errorf("not lower-cased: %s %s", i1.QRef, i2.QRef)
	}
	// optional parts outside the contract are left out, valid ones are sent
	if i1.Rev != nil || i1.AnsweredAt != nil || i1.SessionRef != nil || len(i1.Payload) != 0 {
		t.Errorf("item 1 should carry nothing optional: %+v", i1)
	}
	if i2.Rev == nil || *i2.Rev != 3 || i2.AnsweredAt == nil || i2.SessionRef == nil || *i2.SessionRef != "ps-1" || string(i2.Payload) != `{"step":1}` || i2.AttemptKind != "variant" {
		t.Errorf("item 2: %+v", i2)
	}
	for _, f := range []string{"independence", "reporter_client", "source_event_id"} {
		if strings.Contains(string(raw), f) {
			t.Errorf("%s must never be sent", f)
		}
	}
}

func TestValidateAgainstTheDocumentLimits(t *testing.T) {
	long := strings.Repeat("字", 501)
	ok500 := strings.Repeat("字", 500)
	base := func(items ...GradeItem) GradeRequest { return GradeRequest{ClientAttemptID: "cid-1", Items: items} }
	for name, r := range map[string]GradeRequest{
		"answer 501 chars":          base(GradeItem{QRef: "a", Answer: long}),
		"blank 501 chars":           base(GradeItem{QRef: "a", Answer: []string{"x", long}}),
		"11 blanks":                 base(GradeItem{QRef: "a", Answer: make([]string, 11)}),
		"qrefs equal once lowered":  base(GradeItem{QRef: "Math:1", Answer: "x"}, GradeItem{QRef: "math:1", Answer: "y"}),
		"key over 198 chars":        {ClientAttemptID: strings.Repeat("c", 160), Items: []GradeItem{{QRef: strings.Repeat("q", 36), Answer: "x"}}},
		"attempt_kind first":        base(GradeItem{QRef: "a", Answer: "x", AttemptKind: "first"}),
		"attempt_kind exam":         base(GradeItem{QRef: "a", Answer: "x", AttemptKind: "exam"}),
		"negative rev":              base(GradeItem{QRef: "a", Answer: "x", Rev: -1}),
		"id with a space":           {ClientAttemptID: "a b", Items: []GradeItem{{QRef: "a", Answer: "x"}}},
		"id of 161":                 {ClientAttemptID: strings.Repeat("c", 161), Items: []GradeItem{{QRef: "a", Answer: "x"}}},
		"51 items":                  {ClientAttemptID: "c", Items: make51()},
		"empty":                     {ClientAttemptID: "c"},
		"answer neither str nor []": base(GradeItem{QRef: "a", Answer: 5}),
	} {
		err := ValidateGradeRequest(r)
		var be *Error
		if !errors.As(err, &be) || be.Kind != KindBadRequest {
			t.Errorf("%s: %v", name, err)
		}
	}
	good := []GradeRequest{
		base(GradeItem{QRef: "a", Answer: ok500}),
		base(GradeItem{QRef: "a", Answer: make([]string, 10)}),
		{ClientAttemptID: strings.Repeat("c", 160), Items: []GradeItem{{QRef: strings.Repeat("q", 34), Answer: "x"}}}, // 3+160+1+34 = 198
		base(GradeItem{QRef: "a", Answer: "x", AttemptKind: "correction", Rev: 1}),
	}
	for i, r := range good {
		if err := ValidateGradeRequest(r); err != nil {
			t.Errorf("good %d: %v", i, err)
		}
	}
}

func make51() []GradeItem {
	var it []GradeItem
	for i := 0; i < 51; i++ {
		it = append(it, GradeItem{QRef: "q" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Answer: "x"})
	}
	return it
}

func TestAnsweredAtWindow(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]bool{
		0: true, 20 * time.Second: true, 45 * time.Second: false, time.Hour: false,
		-24 * time.Hour: true, -6 * 24 * time.Hour: true, -7*24*time.Hour + 2*time.Minute: true, -7 * 24 * time.Hour: false, -30 * 24 * time.Hour: false,
	} {
		if got := AnsweredAtSendable(now.Add(d), now); got != want {
			t.Errorf("%v: %v want %v", d, got, want)
		}
	}
	if AnsweredAtSendable(time.Time{}, now) {
		t.Error("zero time is not sendable")
	}
}

// Lookup: lower-case on the wire, the caller's strings are what the result is keyed by.
func TestLookupAliasesAndCase(t *testing.T) {
	var asked []string
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ QRefs []string }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		asked = req.QRefs
		// "math:s:2-old" is an alias of math:s:2 (returned once, canonical); math:s:gone is missing
		reply(w, "application/json", `{"items":[{"qref":"math:s:1","stem":"s","media":[],"rev":2},{"qref":"math:s:2","stem":"s","media":[],"rev":1}],"missing":["math:s:gone"]}`)
	}, "g", "")
	found, missing, err := c.LookupByQrefs(context.Background(), "s", []string{"MATH:S:1", "math:s:2-old", "math:s:gone", "math:s:2", "MATH:S:1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != "math:s:1,math:s:2-old,math:s:gone,math:s:2" {
		t.Errorf("wire: %v", asked)
	}
	if len(found) != 2 || found["MATH:S:1"].QRef != "math:s:1" || found["math:s:2"].QRef != "math:s:2" {
		t.Fatalf("found: %v", found)
	}
	// the alias had no exact match: the leftover string pairs with the leftover item only
	// when that is unambiguous; here "math:s:2" matched exactly, so the alias is left over with no item
	if _, ok := found["math:s:2-old"]; ok {
		t.Error("an alias must not be guessed onto an item that another string already matched")
	}
	if strings.Join(missing, ",") != "math:s:2-old,math:s:gone" {
		t.Errorf("missing echoes the caller's strings: %v", missing)
	}
}

func TestLookupAliasPairedWhenUnambiguous(t *testing.T) {
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, "application/json", `{"items":[{"qref":"math:s:2","stem":"s","media":[],"rev":1}],"missing":[]}`)
	}, "g", "")
	found, missing, err := c.LookupByQrefs(context.Background(), "s", []string{"math:s:2-old"})
	if err != nil || len(missing) != 0 || found["math:s:2-old"].QRef != "math:s:2" {
		t.Errorf("%v %v %v", found, missing, err)
	}
}

func TestLookupAnyNotFoundMeansNotDeployed(t *testing.T) {
	for _, body := range []string{``, `{"code":"not_found","message":"not found"}`, `{"code":"not_found","message":"source not found","details":{"qrefs":["x"]}}`} {
		var lists int
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				errReply(w, 404, nil, body)
				return
			}
			lists++
			reply(w, "application/json", `{"items":[{"qref":"a","source":"s.x","stem":"s","media":[]}],"next_cursor":null}`)
		}, "g", "")
		found, missing, err := c.LookupByQrefs(context.Background(), "s.x", []string{"a", "b"})
		if err != nil || len(found) != 1 || len(missing) != 1 || lists != 1 {
			t.Errorf("%q: %v %v %v lists=%d", body, found, missing, err, lists)
		}
	}
}

func TestProbeLookup(t *testing.T) {
	probe := func(status int, body string) (*Client, *int) {
		n := new(int)
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
			*n++
			b, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || r.URL.RawQuery != "" || !strings.Contains(string(b), probeQRef) || strings.Count(string(b), "math:") != 1 {
				t.Errorf("probe must be one POST with only the non-existent qref: %s %s", r.Method, b)
			}
			errReply(w, status, nil, body)
		}, "g", "")
		return c, n
	}
	c, n := probe(200, `{"items":[],"missing":["`+probeQRef+`"]}`)
	if ok, err := c.ProbeLookup(context.Background()); !ok || err != nil || *n != 1 {
		t.Errorf("deployed: %v %v", ok, err)
	}
	for _, st := range []int{404, 501, 405} {
		c, _ := probe(st, `{"code":"not_found","message":"not found"}`)
		if ok, err := c.ProbeLookup(context.Background()); ok || err != nil {
			t.Errorf("%d: %v %v", st, ok, err)
		}
		if !c.now().Before(c.lookupDownUntil) {
			t.Errorf("%d: the answer is not cached", st)
		}
	}
	for name, p := range map[string][2]any{
		"401": {401, `{}`}, "500": {500, `{}`}, "400": {400, `{"code":"validation_failed"}`},
		"returned an item": {200, `{"items":[{"qref":"` + probeQRef + `"}],"missing":[]}`}, "probe not in missing": {200, `{"items":[],"missing":[]}`},
	} {
		c, _ := probe(p[0].(int), p[1].(string))
		if ok, err := c.ProbeLookup(context.Background()); ok || err == nil {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
	// a positive probe clears an earlier "not deployed" memory
	c, _ = probe(200, `{"items":[],"missing":["`+probeQRef+`"]}`)
	c.lookupDownUntil = time.Now().Add(time.Hour)
	if ok, _ := c.ProbeLookup(context.Background()); !ok || c.now().Before(c.lookupDownUntil) {
		t.Error("deployed probe must clear the down cache")
	}
}

func TestPickSourceNotFoundIsNotNotDeployed(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		unsupported bool
		kind        Kind
		code        string
	}{
		{404, `{"code":"not_found","message":"not found"}`, true, 0, ""},
		{404, ``, true, 0, ""},
		{501, `{}`, true, 0, ""},
		{404, `{"code":"not_found","message":"source not found"}`, false, KindNotFound, "not_found"},
		{404, `{"code":"not_found","message":"brain not found"}`, false, KindNotFound, "not_found"},
		{400, `{"code":"ambiguous_source","message":"ambiguous source"}`, false, KindRejected, "ambiguous_source"},
		{403, `{"code":"forbidden"}`, false, KindAuth, "forbidden"},
	}
	for _, c := range cases {
		cl := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { errReply(w, c.status, nil, c.body) }, "g", "")
		_, err := cl.SelectForPractice(context.Background(), "s.x", 3, SelectOpts{Mode: ModeWrongOnly})
		var be *Error
		if c.unsupported != errors.Is(err, ErrUnsupported) {
			t.Errorf("%d %s: %v", c.status, c.body, err)
			continue
		}
		if !c.unsupported && (!errors.As(err, &be) || be.Kind != c.kind || be.Code != c.code) {
			t.Errorf("%d %s: %v", c.status, c.body, err)
		}
	}
}

func TestPickNeverAsksForMoreThanTenAndSendsOnlyKnownQuery(t *testing.T) {
	var q map[string][]string
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		reply(w, "application/json", `{"items":[
		 {"qref":"math:s:1","number":"1","origin":"review","reasons":["due_on:2026-10-17","wrong_count:2"]},
		 {"qref":"math:s:2","number":"2","origin":"wrong","reasons":["wrong_count:1","deferred:figure_missing"]},
		 {"qref":"math:s:3","number":"3","origin":"both","reasons":["wrong_count:1"]}]}`)
	}, "g", "")
	got, err := c.SelectForPractice(context.Background(), "s.x", 25, SelectOpts{Mode: ModeDueFirst})
	if err != nil {
		t.Fatal(err)
	}
	if q["n"][0] != "10" {
		t.Errorf("n=%v", q["n"])
	}
	for k := range q {
		if k != "source" && k != "n" && k != "date" && k != "brain" && k != "mode" {
			t.Errorf("query parameter %q is not in the contract", k)
		}
	}
	if len(got) != 3 || got[0].Reasons[0] != "due_on:2026-10-17" || got[0].Reasons[1] != "wrong_count:2" || got[1].Deferred != "figure_missing" || got[2].Origin != "" {
		t.Errorf("vocabulary / origin (review|wrong only): %+v", got)
	}
	got, _ = c.SelectForPractice(context.Background(), "s.x", 0, SelectOpts{Mode: ModeWrongOnly})
	if q["n"][0] != "1" || len(got) != 1 {
		t.Errorf("n below 1 is lifted to 1: %v %v", q["n"], got)
	}
}
