package brain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupWireAndMissing(t *testing.T) {
	var gotAuth, gotMethod, gotPath, gotCT string
	var body map[string]any
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath, gotCT = r.Header.Get("Authorization"), r.Method, r.URL.Path, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		reply(w, "application/json", `{"items":[
			{"qref":"a","source":"s.x","stem":"s","rev":4,"figure_status":"ok","media":[{"sha256":"`+strings.Repeat("a", 64)+`","mime":"image/png","ordinal":1,"slot":"stem"},{"sha256":"bad","mime":"image/png","ordinal":2,"slot":"stem"}]},
			{"qref":"b","source":"s.x","stem":"s","rev":"x","media":[]},
			{"qref":"foreign","source":"other","stem":"s","media":[]},
			{"qref":"not-asked","source":"s.x","stem":"s","media":[]}],"missing":["nope"]}`)
	}, "grade-tok", "")
	found, missing, err := c.LookupByQrefs(context.Background(), "s.x", []string{"b", "nope", "a", "a", "foreign", ""})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != DefaultLookupPath || gotAuth != "Bearer read-tok" || gotCT != "application/json" {
		t.Errorf("%s %s auth=%q ct=%q (lookup uses the READ token)", gotMethod, gotPath, gotAuth, gotCT)
	}
	if qs := body["qrefs"].([]any); len(body) != 1 || len(qs) != 4 || qs[0] != "b" || qs[3] != "foreign" { // deduped, blanks dropped
		t.Errorf("body: %v", body)
	}
	if len(found) != 2 || found["a"].RevInt() != 4 || found["b"].RevInt() != 0 || len(found["a"].Media) != 1 || found["a"].Figure() != "ok" {
		t.Errorf("found: %+v", found)
	}
	if strings.Join(missing, ",") != "nope,foreign" { // brain-missing, plus a foreign-source row
		t.Errorf("missing: %v", missing)
	}
}

func TestLookupChunksOf100(t *testing.T) {
	var sizes []int
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ QRefs []string }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		sizes = append(sizes, len(req.QRefs))
		var items []string
		for _, q := range req.QRefs {
			items = append(items, `{"qref":"`+q+`","stem":"s","media":[]}`)
		}
		reply(w, "application/json", `{"items":[`+strings.Join(items, ",")+`],"missing":[]}`)
	}, "g", "")
	var refs []string
	for i := 0; i < 250; i++ {
		refs = append(refs, "q"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	found, missing, err := c.LookupByQrefs(context.Background(), "s.x", refs)
	if err != nil || len(found) != 250 || len(missing) != 0 || len(sizes) != 3 || sizes[0] != 100 || sizes[1] != 100 || sizes[2] != 50 {
		t.Errorf("%v found=%d missing=%d sizes=%v", err, len(found), len(missing), sizes)
	}
}

func TestLookupFallsBackToTheListWhenNotDeployed(t *testing.T) {
	for _, status := range []int{404, 501, 405} {
		var lookups, lists atomic.Int32
		now := time.Now()
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				lookups.Add(1)
				w.WriteHeader(status)
				return
			}
			lists.Add(1)
			reply(w, "application/json", `{"items":[{"qref":"a","source":"s.x","stem":"s","media":[]}],"next_cursor":null}`)
		}, "g", "")
		c.now = func() time.Time { return now }
		for i := 0; i < 2; i++ {
			found, missing, err := c.LookupByQrefs(context.Background(), "s.x", []string{"a", "nope"})
			if err != nil || len(found) != 1 || len(missing) != 1 {
				t.Fatalf("%d: %v %v %v", status, found, missing, err)
			}
		}
		if lookups.Load() != 1 { // "not deployed" is remembered
			t.Errorf("%d: probed %d times", status, lookups.Load())
		}
		now = now.Add(time.Minute) // ... for a while only
		_, _, _ = c.LookupByQrefs(context.Background(), "s.x", []string{"a"})
		if lookups.Load() != 2 {
			t.Errorf("%d: not re-probed after the quiet period (%d)", status, lookups.Load())
		}
	}
	// other failures are errors, not a silent fallback
	for _, status := range []int{401, 500, 429} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, "g", "")
		if _, _, err := c.LookupByQrefs(context.Background(), "s.x", []string{"a"}); err == nil {
			t.Errorf("%d: no error", status)
		}
	}
}

const pickReply = `{"items":[
 {"qref":"a","number":"1(2)","origin":"wrong","reasons":["unresolved_wrong"]},
 {"qref":"b","number":3,"origin":"review","reasons":["due","deferred:pending_key","deferred:figure_missing"]},
 {"qref":"a","number":"dup","origin":"wrong","reasons":[]},
 {"qref":"c","origin":"nonsense","reasons":["deferred:Bad Code","has space","<x>","deferred:unsupported_qtype"]},
 {"qref":"","origin":"wrong"},
 {"qref":"d","origin":"wrong","reasons":["deferred:figure_missing"]}]}`

func TestPickPracticeWireAndParsing(t *testing.T) {
	var gotAuth, gotPath string
	var q map[string][]string
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, q = r.Header.Get("Authorization"), r.URL.Path, r.URL.Query()
		reply(w, "application/json", pickReply)
	}, "grade-tok", "")
	got, err := c.SelectForPractice(context.Background(), "s.x", 10, SelectOpts{Mode: ModeWrongOnly})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer read-tok" || gotPath != DefaultPickPath || len(q) != 4 || q["source"][0] != "s.x" || q["n"][0] != "10" || q["mode"][0] != "wrong_only" || q["brain"][0] != "ruyi" {
		t.Errorf("auth=%q path=%q q=%v", gotAuth, gotPath, q)
	}
	if picks(got) != "a,b,c,d" {
		t.Fatalf("dedupe/empty qref: %v", picks(got))
	}
	if got[0].Number != "1(2)" || got[0].Origin != "wrong" || got[0].Deferred != "" ||
		got[1].Number != "3" || got[1].Origin != "review" || got[1].Deferred != "pending_key" ||
		got[2].Origin != "" || got[2].Deferred != "unsupported_qtype" || len(got[2].Reasons) != 1 || // invalid reasons dropped (incl. the malformed deferred code)
		got[3].Deferred != "figure_missing" {
		t.Errorf("%+v", got)
	}
	// n caps the result; SkipDeferred drops deferred picks before the cap counts
	got, _ = c.SelectForPractice(context.Background(), "s.x", 2, SelectOpts{Mode: ModeDueFirst})
	if picks(got) != "a,b" {
		t.Errorf("cap: %v", picks(got))
	}
	got, _ = c.SelectForPractice(context.Background(), "s.x", 10, SelectOpts{Mode: ModeDueFirst, SkipDeferred: true})
	if picks(got) != "a" {
		t.Errorf("skip deferred: %v", picks(got))
	}
}

func TestPickPracticeNotDeployedIsUnsupportedAndOtherErrorsAreErrors(t *testing.T) {
	for _, status := range []int{404, 501, 405} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, "g", "")
		for _, m := range []string{ModeWrongOnly, ModeDueFirst} {
			if _, err := c.SelectForPractice(context.Background(), "s.x", 3, SelectOpts{Mode: m}); !errors.Is(err, ErrUnsupported) {
				t.Errorf("%d/%s: %v", status, m, err)
			}
		}
	}
	for _, status := range []int{401, 429, 500} {
		c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, "g", "")
		_, err := c.SelectForPractice(context.Background(), "s.x", 3, SelectOpts{Mode: ModeWrongOnly})
		var be *Error
		if err == nil || errors.Is(err, ErrUnsupported) || !errors.As(err, &be) {
			t.Errorf("%d: %v", status, err)
		}
	}
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, "application/json", `{`) }, "g", "")
	if _, err := c.SelectForPractice(context.Background(), "s.x", 3, SelectOpts{Mode: ModeWrongOnly}); err == nil {
		t.Error("malformed reply accepted")
	}
	if _, err := c.SelectForPractice(context.Background(), "s.x", 3, SelectOpts{Mode: "chaos"}); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestCustomLookupAndPickPaths(t *testing.T) {
	var paths []string
	c := gradeClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method == "POST" {
			reply(w, "application/json", `{"items":[],"missing":["a"]}`)
			return
		}
		reply(w, "application/json", `{"items":[]}`)
	}, "g", "")
	c.lookupPath, c.pickPath = "/x/lookup", "/x/pick"
	_, _, _ = c.LookupByQrefs(context.Background(), "s.x", []string{"a"})
	_, _ = c.SelectForPractice(context.Background(), "s.x", 1, SelectOpts{Mode: ModeDueFirst})
	if strings.Join(paths, ",") != "/x/lookup,/x/pick" {
		t.Errorf("%v", paths)
	}
}
