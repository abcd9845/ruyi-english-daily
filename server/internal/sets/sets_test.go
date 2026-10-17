package sets_test

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

var srcRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{1,127}$`)

func parse(s string) (*sets.Registry, error) { return sets.Parse([]byte(s), srcRe.MatchString) }

const good = `{"sets":[{"id":"a-1_x","title":"T","source":"src.one","qrefs":["q:1","q:2"]},{"id":"b","title":"U","source":"src.two","qrefs":["q:9"]}]}`

func TestParseOK(t *testing.T) {
	r, err := parse(good)
	if err != nil {
		t.Fatal(err)
	}
	if r.Len() != 2 || strings.Join(r.IDs(), ",") != "a-1_x,b" {
		t.Errorf("ids %v", r.IDs())
	}
	s, ok := r.Get("a-1_x")
	if !ok || s.Title != "T" || s.Source != "src.one" || strings.Join(s.QRefs, ",") != "q:1,q:2" {
		t.Errorf("set: %+v", s)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("unknown id found")
	}
	if !r.HasSource("src.two") || r.HasSource("src.three") {
		t.Error("HasSource")
	}
	if !r.AllowsQRef("src.one", "q:2") || r.AllowsQRef("src.one", "q:9") || r.AllowsQRef("src.two", "q:1") {
		t.Error("AllowsQRef is per source and per whitelist")
	}
	var nilReg *sets.Registry
	if nilReg.Len() != 0 || nilReg.HasSource("x") || nilReg.AllowsQRef("x", "y") || nilReg.IDs() != nil {
		t.Error("nil registry must behave as empty")
	}
	if _, ok := nilReg.Get("a"); ok {
		t.Error("nil registry Get")
	}
}

func TestParseRejects(t *testing.T) {
	one := func(id, title, src, qrefs string) string {
		return `{"sets":[{"id":"` + id + `","title":"` + title + `","source":"` + src + `","qrefs":` + qrefs + `}]}`
	}
	cases := map[string]string{
		"empty id":           one("", "T", "src.one", `["q"]`),
		"uppercase id":       one("Abc", "T", "src.one", `["q"]`),
		"space in id":        one("a b", "T", "src.one", `["q"]`),
		"slash in id":        one("a/b", "T", "src.one", `["q"]`),
		"dot in id":          one("a.b", "T", "src.one", `["q"]`),
		"65-char id":         one(strings.Repeat("a", 65), "T", "src.one", `["q"]`),
		"unicode id":         one("é", "T", "src.one", `["q"]`),
		"empty title":        one("a", "", "src.one", `["q"]`),
		"control in title":   one("a", `T\u0001`, "src.one", `["q"]`),
		"bad source":         one("a", "T", "Bad Source", `["q"]`),
		"empty source":       one("a", "T", "", `["q"]`),
		"empty qrefs":        one("a", "T", "src.one", `[]`),
		"null qrefs":         one("a", "T", "src.one", `null`),
		"empty qref":         one("a", "T", "src.one", `[""]`),
		"padded qref":        one("a", "T", "src.one", `[" q"]`),
		"control qref":       one("a", "T", "src.one", `["q\n"]`),
		"duplicate qref":     one("a", "T", "src.one", `["q","q"]`),
		"long qref":          one("a", "T", "src.one", `["`+strings.Repeat("q", 201)+`"]`),
		"no sets":            `{"sets":[]}`,
		"missing sets":       `{}`,
		"unknown top field":  `{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["q"]}],"extra":1}`,
		"unknown set field":  `{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["q"],"answers":["x"]}]}`,
		"wrong type":         `{"sets":[{"id":1,"title":"T","source":"src.one","qrefs":["q"]}]}`,
		"duplicate id":       `{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["q"]},{"id":"a","title":"U","source":"src.one","qrefs":["r"]}]}`,
		"trailing data":      good + ` {"sets":[]}`,
		"trailing garbage":   good + `x`,
		"not json":           `sets:`,
		"empty file":         ``,
		"array at top level": `[]`,
	}
	for name, in := range cases {
		if _, err := parse(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	var many strings.Builder
	many.WriteString(`["`)
	for i := 0; i < sets.MaxQRefs+1; i++ {
		if i > 0 {
			many.WriteString(`","`)
		}
		many.WriteString("q" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + strings.Repeat("y", i/26))
	}
	many.WriteString(`"]`)
	if _, err := parse(one("a", "T", "src.one", many.String())); err == nil {
		t.Error("too many qrefs must fail")
	}
	if _, err := sets.Parse([]byte(strings.Repeat(" ", sets.MaxFileBytes+1)), srcRe.MatchString); err == nil {
		t.Error("oversized file must fail")
	}
}

// Error text names positions, never values from the file.
func TestErrorsDoNotEchoValues(t *testing.T) {
	const marker = "REAL-QREF-MARKER"
	for _, in := range []string{
		`{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["` + marker + `","` + marker + `"]}]}`,
		`{"sets":[{"id":"` + marker + `","title":"T","source":"src.one","qrefs":["q"]}]}`,
		`{"sets":[{"id":"a","title":"T","source":"` + marker + `","qrefs":["q"]}]}`,
		`{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["q"],"` + marker + `":1}]}`,
		`{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":["q"]}]} ` + marker,
		`{"sets":[{"id":"a","title":"T","source":"src.one","qrefs":"` + marker + `"}]}`,
	} {
		_, err := parse(in)
		if err == nil {
			t.Errorf("want error for %.40s", in)
			continue
		}
		if strings.Contains(err.Error(), marker) {
			t.Errorf("error echoes a value: %v", err)
		}
	}
}

func TestExampleFileParses(t *testing.T) {
	b, err := os.ReadFile("../../deploy/sets.example.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := r.Get("demo-1")
	if s == nil || !strings.HasPrefix(s.QRefs[0], "example.set:") {
		t.Errorf("example must use placeholder qrefs: %+v", s)
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{"a": true, "a-b_c9": true, "": false, "A": false, "a b": false, "a.b": false, strings.Repeat("z", 64): true, strings.Repeat("z", 65): false} {
		if sets.ValidID(id) != want {
			t.Errorf("ValidID(%q) != %v", id, want)
		}
	}
}

func TestReservedIDs(t *testing.T) {
	for _, id := range []string{"new", "auto-abcdefgh", "auto-x"} {
		_, err := parse(`{"sets":[{"id":"` + id + `","title":"T","source":"src.one","qrefs":["q:1"]}]}`)
		if err == nil || strings.Contains(err.Error(), id+`"`) && false {
			t.Errorf("%s accepted", id)
		}
	}
}

// ---- local (smoke / test) sets ----------------------------------------------

const localOK = `{"id":"smoke-test","local":true,"title":"T","questions":[{"qref":"smoke:q1","qtype":"single_choice","stem":"s","options":[{"key":"A","text":"1"},{"key":"B","text":"2"}],"answer":["B"]}]}`
const realOK = `{"id":"demo-1","title":"R","source":"src.one","qrefs":["q:1"]}`

func wrap(sets ...string) string { return `{"sets":[` + strings.Join(sets, ",") + `]}` }

func TestLocalSetParses(t *testing.T) {
	r, err := parse(wrap(realOK, localOK))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := r.Get("smoke-test")
	if !ok || !s.Local || s.Source != sets.LocalSource || strings.Join(s.QRefs, ",") != "smoke:q1" || len(s.Questions) != 1 {
		t.Fatalf("local set: %+v", s)
	}
	if q, ok := s.Question("smoke:q1"); !ok || q.Answer[0] != "B" {
		t.Error("Question lookup")
	}
	if _, ok := s.Question("smoke:nope"); ok {
		t.Error("unknown inline qref")
	}
	real, _ := r.Get("demo-1")
	if _, ok := real.Question("q:1"); ok {
		t.Error("a real set has no inline questions")
	}
	// local sets are invisible to the brain-source checks that gate media access
	if r.HasSource(sets.LocalSource) || r.AllowsQRef(sets.LocalSource, "smoke:q1") || r.AllowsQRef("", "smoke:q1") {
		t.Error("HasSource/AllowsQRef must exclude local sets")
	}
	if !r.HasSource("src.one") || !r.AllowsQRef("src.one", "q:1") {
		t.Error("real sets unchanged")
	}
	// the source may be written, if it is the reserved value
	if _, err := parse(wrap(strings.Replace(localOK, `"local":true,`, `"local":true,"source":"local.smoke",`, 1))); err != nil {
		t.Errorf("reserved source written explicitly: %v", err)
	}
	// "test-" ids and fill_blank work too
	fb := `{"id":"test-x","local":true,"title":"T","questions":[{"qref":"smoke:f","qtype":"fill_blank","stem":"a ____ b ____","answer":["1","2"]}]}`
	if _, err := parse(wrap(fb)); err != nil {
		t.Errorf("fill_blank: %v", err)
	}
}

func TestLocalSetRejects(t *testing.T) {
	rep := func(s, a, b string) string { return strings.Replace(s, a, b, 1) }
	cases := map[string]string{
		"local id not smoke-/test-":      wrap(rep(localOK, `"smoke-test"`, `"demo-9"`)),
		"local id merely contains smoke": wrap(rep(localOK, `"smoke-test"`, `"my-smoke-test"`)),
		"local with qrefs":               wrap(rep(localOK, `"local":true,`, `"local":true,"qrefs":["smoke:q1"],`)),
		"local with real-looking qrefs":  wrap(rep(localOK, `"local":true,`, `"local":true,"qrefs":["q:1"],`)),
		"local without questions":        wrap(`{"id":"smoke-test","local":true,"title":"T"}`),
		"local empty questions":          wrap(`{"id":"smoke-test","local":true,"title":"T","questions":[]}`),
		"qref without smoke: prefix":     wrap(rep(localOK, `"smoke:q1"`, `"q1"`)),
		"qref with other prefix":         wrap(rep(localOK, `"smoke:q1"`, `"example.set:math:1"`)),
		"qref uppercase prefix":          wrap(rep(localOK, `"smoke:q1"`, `"SMOKE:q1"`)),
		"duplicate local qref":           wrap(rep(localOK, `"questions":[`, `"questions":[{"qref":"smoke:q1","qtype":"fill_blank","stem":"a ____","answer":["x"]},`)),
		"local with a foreign source":    wrap(rep(localOK, `"local":true,`, `"local":true,"source":"src.one",`)),
		"real set with questions":        wrap(rep(realOK, `"qrefs"`, `"questions":[{"qref":"smoke:q1","qtype":"fill_blank","stem":"a ____","answer":["x"]}],"qrefs"`)),
		"real set, smoke- id":            wrap(rep(realOK, `"demo-1"`, `"smoke-1"`)),
		"real set, test- id":             wrap(rep(realOK, `"demo-1"`, `"test-1"`)),
		"real set, smoke: qref":          wrap(rep(realOK, `"q:1"`, `"smoke:q1"`)),
		"real set, SMOKE: qref":          wrap(rep(realOK, `"q:1"`, `"Smoke:q1"`)),
		"real set, reserved source":      wrap(rep(realOK, `"src.one"`, `"local.smoke"`)),
		"local:false with questions":     wrap(rep(localOK, `"local":true`, `"local":false`)),
		"bad qtype":                      wrap(rep(localOK, `"single_choice"`, `"multi_choice"`)),
		"answer not an option":           wrap(rep(localOK, `["B"]`, `["Z"]`)),
		"two answers for single_choice":  wrap(rep(localOK, `["B"]`, `["A","B"]`)),
		"no answer":                      wrap(rep(localOK, `,"answer":["B"]`, ``)),
		"one option":                     wrap(rep(localOK, `,{"key":"B","text":"2"}`, ``)),
		"duplicate option key":           wrap(rep(localOK, `"key":"B"`, `"key":"A"`)),
		"lowercase option key":           wrap(rep(localOK, `"key":"B"`, `"key":"b"`)),
		"empty stem":                     wrap(rep(localOK, `"stem":"s"`, `"stem":" "`)),
		"empty option text":              wrap(rep(localOK, `"text":"2"`, `"text":""`)),
		"fill_blank without blanks":      wrap(`{"id":"smoke-x","local":true,"title":"T","questions":[{"qref":"smoke:f","qtype":"fill_blank","stem":"no blanks","answer":["1"]}]}`),
		"fill_blank answer count":        wrap(`{"id":"smoke-x","local":true,"title":"T","questions":[{"qref":"smoke:f","qtype":"fill_blank","stem":"a ____","answer":["1","2"]}]}`),
		"fill_blank with options":        wrap(`{"id":"smoke-x","local":true,"title":"T","questions":[{"qref":"smoke:f","qtype":"fill_blank","stem":"a ____","options":[{"key":"A","text":"x"},{"key":"B","text":"y"}],"answer":["1"]}]}`),
		"unknown question field":         wrap(rep(localOK, `"qtype"`, `"explanation":"x","qtype"`)),
		"questions with no title":        wrap(rep(localOK, `"title":"T"`, `"title":""`)),
	}
	for name, in := range cases {
		if _, err := parse(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// a valid local set after the bad ones was fine on its own, so the failures above are the rules, not the fixture
	if _, err := parse(wrap(localOK)); err != nil {
		t.Fatal(err)
	}
}

func TestLocalErrorsDoNotEchoValues(t *testing.T) {
	const marker = "REAL-QREF-MARKER"
	for _, in := range []string{
		wrap(strings.Replace(localOK, `"smoke:q1"`, `"`+marker+`"`, 1)),
		wrap(strings.Replace(localOK, `"smoke-test"`, `"`+marker+`"`, 1)),
		wrap(strings.Replace(realOK, `"q:1"`, `"smoke:`+marker+`"`, 1)),
		wrap(strings.Replace(localOK, `["B"]`, `["`+marker+`"]`, 1)),
		wrap(strings.Replace(localOK, `"local":true,`, `"local":true,"source":"`+marker+`",`, 1)),
	} {
		_, err := parse(in)
		if err == nil {
			t.Errorf("want error for %.60s", in)
		} else if strings.Contains(err.Error(), marker) {
			t.Errorf("error echoes a value: %v", err)
		}
	}
}

func TestSmokeExampleFilesParse(t *testing.T) {
	b, err := os.ReadFile("../../deploy/sets.smoke.example.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := r.Get("smoke-test")
	if !ok || !s.Local || len(s.Questions) != 1 || !strings.Contains(s.Questions[0].Stem, "测试题，随便填") {
		t.Fatalf("smoke-test: %+v", s)
	}
	// the standalone entry is the same object as in the full file
	e, err := os.ReadFile("../../deploy/sets.smoke-entry.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parse(`{"sets":[` + string(e) + `]}`); err != nil {
		t.Fatalf("entry file: %v", err)
	}
	var a, c any
	var full struct {
		Sets []json.RawMessage `json:"sets"`
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(json.Unmarshal(b, &full))
	must(json.Unmarshal(full.Sets[1], &a))
	must(json.Unmarshal(e, &c))
	if !reflect.DeepEqual(a, c) {
		t.Error("sets.smoke-entry.json must equal the smoke-test entry of sets.smoke.example.json")
	}
}
