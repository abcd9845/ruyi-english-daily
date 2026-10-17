package sets_test

import (
	"os"
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
