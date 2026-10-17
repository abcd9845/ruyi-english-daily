package server_test

import (
	"encoding/json"
	"strings"
	"testing"
)

const setsJSON = `{"sets":[
 {"id":"demo-1","title":"演示套","source":"` + practiceSrc + `","qrefs":["example.set:math:1","example.set:math:2","example.set:math:3"]},
 {"id":"reorder_2","title":"乱序套","source":"` + practiceSrc + `","qrefs":["example.set:math:3","example.set:math:99","example.set:math:1"]}
]}`

type pOption struct {
	Key    string `json:"key"`
	TextMD string `json:"text_md"`
}
type pItem struct {
	Ordinal      int       `json:"ordinal"`
	QRef         string    `json:"qref"`
	QType        string    `json:"qtype"`
	StemMD       string    `json:"stem_md"`
	Options      []pOption `json:"options"`
	Blanks       int       `json:"blanks"`
	FigureStatus string    `json:"figure_status"`
	Rev          int       `json:"rev"`
	Origin       string    `json:"origin"`
	RefPending   string    `json:"ref_pending"`
	Media        []struct {
		URL  string `json:"url"`
		Slot string `json:"slot"`
	} `json:"media"`
}
type pResp struct {
	SetID        string   `json:"set_id"`
	Title        string   `json:"title"`
	Items        []pItem  `json:"items"`
	MissingQRefs []string `json:"missing_qrefs"`
}

func getPractice(t *testing.T, e *env, cookie, id string) (int, pResp, []byte) {
	t.Helper()
	resp, b := e.do(t, "GET", "/api/practice/"+id, "", nil, cookie)
	var r pResp
	if resp.StatusCode == 200 {
		must(t, json.Unmarshal(b, &r))
	}
	return resp.StatusCode, r, b
}

func TestPracticeDisabledWithoutSets(t *testing.T) {
	e := newEnv(t, opts{})
	ck := e.session(t)
	for _, p := range []string{"/api/practice/demo-1", "/api/practice/", "/api/practice"} {
		resp, _ := e.do(t, "GET", p, "", nil, ck)
		if resp.StatusCode != 404 {
			t.Errorf("%s without RUYI_SETS_FILE: want 404 got %d", p, resp.StatusCode)
		}
	}
	// nothing else changes
	if resp, _ := e.do(t, "GET", "/api/sets/"+testSource, "", nil, ck); resp.StatusCode != 200 {
		t.Errorf("old /api/sets broken: %d", resp.StatusCode)
	}
	if e.brain.count("list") != 1 {
		t.Errorf("practice 404 must not call the brain; list calls=%d", e.brain.count("list"))
	}
}

func TestPracticeRequiresLogin(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	for _, id := range []string{"demo-1", "nope", "BAD ID", "a%2fb"} {
		resp, b := e.do(t, "GET", "/api/practice/"+id, "", nil, "")
		if resp.StatusCode != 401 {
			t.Errorf("%s anonymous: want 401 got %d", id, resp.StatusCode)
		}
		if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "example.set") {
			t.Errorf("401 body leaks: %s", b)
		}
	}
	if resp, _ := e.do(t, "GET", "/api/practice/demo-1", "", nil, "v1.1.forged"); resp.StatusCode != 401 {
		t.Errorf("forged cookie: %d", resp.StatusCode)
	}
	if e.brain.total() != 0 {
		t.Errorf("anonymous requests reached the brain (%d calls)", e.brain.total())
	}
	resp, _ := e.do(t, "POST", "/api/practice/demo-1", "", nil, e.session(t))
	if resp.StatusCode != 405 {
		t.Errorf("POST: %d", resp.StatusCode)
	}
}

func TestPracticeBadAndUnknownSet(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	ck := e.session(t)
	for _, id := range []string{"UPPER", "a.b", "a%20b", "a%2fb", strings.Repeat("a", 65), "..", "%2e%2e", "demo-1/extra", "demo-1%00"} {
		if code, _, _ := getPractice(t, e, ck, id); code != 400 {
			t.Errorf("id %q: want 400 got %d", id, code)
		}
	}
	for _, id := range []string{"unknown-set", "demo-2", strings.Repeat("a", 64)} {
		if code, _, _ := getPractice(t, e, ck, id); code != 404 {
			t.Errorf("id %q: want 404 got %d", id, code)
		}
	}
	if e.brain.total() != 0 {
		t.Errorf("bad/unknown ids reached the brain (%d)", e.brain.total())
	}
}

func TestPracticeWhitelistOrderAndMissing(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	ck := e.session(t)

	code, r, body := getPractice(t, e, ck, "demo-1")
	if code != 200 {
		t.Fatalf("status %d %s", code, body)
	}
	if r.SetID != "demo-1" || r.Title != "演示套" {
		t.Errorf("header: %+v", r)
	}
	var got []string
	for i, it := range r.Items {
		got = append(got, it.QRef)
		if it.Ordinal != i+1 {
			t.Errorf("ordinal %d at %d", it.Ordinal, i)
		}
	}
	if strings.Join(got, ",") != "example.set:math:1,example.set:math:2,example.set:math:3" {
		t.Errorf("whitelist/order: %v", got)
	}
	if !strings.Contains(string(body), `"missing_qrefs":[]`) {
		t.Errorf("missing_qrefs must be an empty array: %s", body)
	}
	// alternates (4,5) exist in the brain but are not whitelisted
	for _, bad := range []string{"math:4", "math:5", "alt"} {
		if strings.Contains(string(body), bad) {
			t.Errorf("non-whitelisted question leaked (%q)", bad)
		}
	}

	// config order wins over brain order; a configured qref the brain lacks goes to missing_qrefs (no 500)
	code, r, _ = getPractice(t, e, ck, "reorder_2")
	if code != 200 {
		t.Fatalf("reorder status %d", code)
	}
	got = nil
	for i, it := range r.Items {
		got = append(got, it.QRef)
		if it.Ordinal != i+1 {
			t.Errorf("reorder ordinal %d at %d", it.Ordinal, i)
		}
	}
	if strings.Join(got, ",") != "example.set:math:3,example.set:math:1" {
		t.Errorf("reorder: %v", got)
	}
	if len(r.MissingQRefs) != 1 || r.MissingQRefs[0] != "example.set:math:99" {
		t.Errorf("missing_qrefs: %v", r.MissingQRefs)
	}
}

func TestPracticeShapeBlanksOptionsMedia(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	_, r, body := getPractice(t, e, e.session(t), "demo-1")
	q1, q2, q3 := r.Items[0], r.Items[1], r.Items[2]
	// q1: ________, ____, (underscores inside $…$ are math, not a blank), ＿＿＿＿ (full-width); ___ is too short
	if q1.Blanks != 3 {
		t.Errorf("q1 blanks=%d want 3", q1.Blanks)
	}
	if q2.Blanks != 0 || q3.Blanks != 0 {
		t.Errorf("q2/q3 blanks: %d %d", q2.Blanks, q3.Blanks)
	}
	if strings.Contains(string(body), `"blanks":0`) {
		t.Error("blanks must be omitted when 0")
	}
	if len(q2.Options) != 2 || q2.Options[0].Key != "A" || q2.Options[0].TextMD != `$\dfrac{1}{2}$` || q2.Options[1].TextMD != "two" {
		t.Errorf("options: %+v", q2.Options)
	}
	if q1.StemMD != "a ________ b ____ c $x_____y$ d\n\n＿＿＿＿ e ___ f" {
		t.Errorf("stem not passed through verbatim: %q", q1.StemMD)
	}
	if len(q1.Media) != 1 || q1.Media[0].URL != "/api/media/"+practiceSrc+"/"+shaP1 || q1.Media[0].Slot != "stem" {
		t.Errorf("q1 media: %+v", q1.Media)
	}
	if q1.FigureStatus != "ok" || q2.FigureStatus != "unknown" {
		t.Errorf("figure_status: %q %q", q1.FigureStatus, q2.FigureStatus)
	}
	if len(q3.Media) != 0 || !strings.Contains(string(body), `"media":[]`) {
		t.Errorf("q3 media must be an empty array")
	}
}

// The fake brain sends answer / explanation / solution (and flags inside options);
// none of it may reach the browser, and the site never asks for include_answer.
func TestPracticeStripsAnswersAndNeverAsksForThem(t *testing.T) {
	for _, lk := range []string{"", "off", "501"} { // lookup endpoint live, and not deployed (list fallback)
		t.Run("lookup="+lk, func(t *testing.T) { stripsAnswers(t, lk) })
	}
}

func stripsAnswers(t *testing.T, lk string) {
	e := newEnv(t, opts{sets: setsJSON, lookup: lk})
	for _, id := range []string{"demo-1", "reorder_2"} {
		_, _, body := getPractice(t, e, e.session(t), id)
		s := string(body)
		for _, bad := range []string{"SECRET", `"answer"`, `"explanation"`, `"solution"`, "include_answer", "is_correct", `"correct`} {
			if strings.Contains(s, bad) {
				t.Errorf("%s response contains %q", id, bad)
			}
		}
		var generic map[string]any
		must(t, json.Unmarshal(body, &generic))
		allowedTop := map[string]bool{"set_id": true, "title": true, "items": true, "missing_qrefs": true}
		for k := range generic {
			if !allowedTop[k] {
				t.Errorf("unexpected top-level key %q", k)
			}
		}
		allowedItem := map[string]bool{"ordinal": true, "qref": true, "qtype": true, "stem_md": true, "options": true,
			"blanks": true, "figure_status": true, "media": true, "rev": true}
		for _, it := range generic["items"].([]any) {
			for k, v := range it.(map[string]any) {
				if !allowedItem[k] {
					t.Errorf("unexpected item key %q", k)
				}
				if k == "options" {
					for _, o := range v.([]any) {
						for ok := range o.(map[string]any) {
							if ok != "key" && ok != "text_md" {
								t.Errorf("unexpected option key %q", ok)
							}
						}
					}
				}
			}
		}
	}
	e.brain.mu.Lock()
	defer e.brain.mu.Unlock()
	if lk == "" {
		// contract path: only POST lookup {"qrefs":[…]} (read token), no list, no include_answer
		if len(e.brain.lookupReqs) == 0 || len(e.brain.listQueries) != 0 {
			t.Fatalf("lookup=%d list=%d", len(e.brain.lookupReqs), len(e.brain.listQueries))
		}
		for _, b := range e.brain.lookupReqs {
			var m map[string]any
			must(t, json.Unmarshal(b, &m))
			if len(m) != 1 || m["qrefs"] == nil || strings.Contains(string(b), "include_answer") {
				t.Errorf("lookup body: %s", b)
			}
		}
		return
	}
	if len(e.brain.listQueries) == 0 {
		t.Fatal("brain was not queried")
	}
	for _, q := range e.brain.listQueries {
		for k := range q {
			if k != "source" && k != "limit" && k != "cursor" {
				t.Errorf("brain query carries %q (only source/limit/cursor allowed)", k)
			}
		}
		if q.Get("source") != practiceSrc || q.Get("limit") != "500" {
			t.Errorf("query: %v", q)
		}
	}
}

func TestPracticeBrainDownIs502NotPanic(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON, brainURL: "http://127.0.0.1:1"})
	code, _, body := getPractice(t, e, e.session(t), "demo-1")
	if code != 502 || strings.Contains(string(body), "127.0.0.1") {
		t.Errorf("code %d body %s", code, body)
	}
}

// Figures of a set's source are served only for whitelisted questions, and the
// old /api/sets listing stays closed for sources that are only used by sets.
func TestPracticeMediaGatingAndSourceIsolation(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	ck := e.session(t)
	resp, b := e.do(t, "GET", "/api/media/"+practiceSrc+"/"+shaP1, "", nil, ck)
	if resp.StatusCode != 200 || !strings.HasPrefix(string(b), "\x89PNG") {
		t.Errorf("whitelisted figure: %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, "GET", "/api/media/"+practiceSrc+"/"+shaP4, "", nil, ck); resp.StatusCode != 404 {
		t.Errorf("figure of a non-whitelisted question: want 404 got %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, "GET", "/api/media/"+practiceSrc+"/"+shaP1, "", nil, ""); resp.StatusCode != 401 {
		t.Errorf("anonymous media: %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, "GET", "/api/sets/"+practiceSrc, "", nil, ck); resp.StatusCode != 404 {
		t.Errorf("/api/sets must not list a set-only source: %d", resp.StatusCode)
	}
	if resp, _ = e.do(t, "GET", "/api/media/other.source/"+shaP1, "", nil, ck); resp.StatusCode != 404 {
		t.Errorf("unknown source media: %d", resp.StatusCode)
	}
}

func TestEmbeddedRoutes(t *testing.T) {
	e := newEnv(t, opts{}) // works with and without sets, and without login
	resp, b := e.do(t, "GET", "/m/practice/", "", nil, "")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(b), "/vendor/katex/katex.min.js") {
		t.Fatalf("page: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("csp: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
	if resp, _ = e.do(t, "GET", "/m/practice/index.html?set=demo-1", "", nil, ""); resp.StatusCode != 200 {
		t.Errorf("index.html: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/m/practice?set=demo-1", "", nil, "")
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/m/practice/?set=demo-1" {
		t.Errorf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ = e.do(t, "POST", "/m/practice/", "", nil, ""); resp.StatusCode != 405 {
		t.Errorf("POST: %d", resp.StatusCode)
	}
	for p, ct := range map[string]string{
		"/vendor/katex/katex.min.js":                   "javascript",
		"/vendor/katex/katex.min.css":                  "text/css",
		"/vendor/katex/fonts/KaTeX_Main-Regular.woff2": "font/woff2",
		"/vendor/katex/LICENSE":                        "text/plain",
	} {
		resp, b := e.do(t, "GET", p, "", nil, "")
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), ct) || len(b) < 100 {
			t.Errorf("%s: %d %q %d bytes", p, resp.StatusCode, resp.Header.Get("Content-Type"), len(b))
		}
		if et := resp.Header.Get("ETag"); et == "" {
			t.Errorf("%s: no etag", p)
		} else if r2, _ := e.do(t, "GET", p, "", map[string]string{"If-None-Match": et}, ""); r2.StatusCode != 304 {
			t.Errorf("%s: conditional get %d", p, r2.StatusCode)
		}
	}
	for _, p := range []string{
		"/vendor/", "/vendor", "/vendor/katex/", "/vendor/katex", "/vendor/katex/fonts/", "/vendor/katex/nope.js",
		"/vendor/katex/fonts/KaTeX_Main-Regular.ttf", "/vendor/katex/fonts/KaTeX_Main-Regular.woff", "/vendor/other/x.js",
		"/vendor/katex/../katex/katex.min.js", "/vendor/katex/%2e%2e/katex.min.js", "/vendor/katex/fonts/../katex.min.js",
		"/vendor/katex//katex.min.js", "/vendor/katex/KATEX.MIN.JS", "/vendor/go.mod", "/vendor/modules.txt", "/vendor/README.md",
		"/vendor/katex/README.md", "/vendor/katex/katex.min.js/", "/m/practice/other.html", "/m/practice/../practice/index.html",
		"/m/practice/index.html/",
	} {
		resp, _ := e.do(t, "GET", p, "", nil, "")
		if resp.StatusCode != 404 && resp.StatusCode != 301 {
			t.Errorf("%s: want 404 got %d", p, resp.StatusCode)
		}
		if resp.StatusCode == 301 && !strings.HasPrefix(resp.Header.Get("Location"), "/") {
			t.Errorf("%s: odd redirect %q", p, resp.Header.Get("Location"))
		}
	}
}

// Log hygiene regression for the new endpoint.
func TestPracticeLogsCarryNoSecretsOrQuestionText(t *testing.T) {
	e := newEnv(t, opts{sets: setsJSON})
	ck := e.session(t)
	e.do(t, "GET", "/api/practice/reorder_2?x=ticket%3Dabc", "", map[string]string{"Authorization": "Bearer leak-me"}, ck)
	e.do(t, "GET", "/api/practice/demo-1", "", nil, "")
	e.do(t, "GET", "/api/media/"+practiceSrc+"/"+shaP1+"?ticket=v1.1.zzz", "", nil, ck)
	logs := e.logs.String()
	for _, bad := range []string{e.brain.token, e.code, e.secret, ck, "leak-me", "ticket=", "Bearer", "Authorization", "Cookie",
		"example.set:math", "SECRET-", "dfrac", "ruyi_sess"} {
		if strings.Contains(logs, bad) {
			t.Errorf("log contains %q:\n%s", bad, logs)
		}
	}
	if !strings.Contains(logs, "missing=1") {
		t.Errorf("expected a count-only warning for the missing qref:\n%s", logs)
	}
}
