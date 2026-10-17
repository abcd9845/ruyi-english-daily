package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
)

func TestParseAnswerShapes(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	multi := shapeOf(brain.Question{QType: "multi_choice", Options: raw(`[{"key":"A","text_md":"x"},{"key":"B"},{"key":"C"}]`)})
	single := shapeOf(brain.Question{QType: "single_choice", Options: raw(`[{"key":"A"},{"key":"B"}]`)})
	fill := shapeOf(brain.Question{QType: "fill_blank", Stem: "a ____ b $x____y$ c ____"})
	free := shapeOf(brain.Question{QType: "calc", Stem: "no blanks"})
	if multi.kind != "multi" || single.kind != "single" || fill.kind != "fill" || fill.blanks != 2 || free.kind != "free" {
		t.Fatalf("shapes: %+v %+v %+v %+v", multi, single, fill, free)
	}
	type c struct {
		name  string
		sh    qshape
		in    string
		want  any
		empty bool
		ok    bool
	}
	for _, x := range []c{
		{"multi ok sorted", multi, `["C","A"]`, []string{"A", "C"}, false, true},
		{"multi dup", multi, `["A","A"]`, nil, false, false},
		{"multi unknown key", multi, `["A","Z"]`, nil, false, false},
		{"multi string", multi, `"A"`, nil, false, false},
		{"multi empty", multi, `[]`, nil, true, true},
		{"multi null", multi, `null`, nil, true, true},
		{"single ok", single, `"B"`, "B", false, true},
		{"single lowercase refused", single, `"b"`, nil, false, false},
		{"single blank", single, `""`, nil, true, true},
		{"fill ok", fill, `["1","2"]`, []string{"1", "2"}, false, true},
		{"fill partly blank is answered", fill, `["1",""]`, []string{"1", ""}, false, true},
		{"fill all blank", fill, `["  ",""]`, nil, true, true},
		{"fill count", fill, `["1"]`, nil, false, false},
		{"fill string", fill, `"1"`, nil, false, false},
		{"free ok", free, `"hello"`, "hello", false, true},
		{"free array", free, `["x"]`, nil, false, false},
		{"free whitespace", free, `"  "`, nil, true, true},
		{"free newline allowed", free, `"a\nb"`, "a\nb", false, true},
		{"free control", free, `"a\u0007"`, nil, false, false},
		{"free too long", free, `"` + strings.Repeat("x", maxFreeLen+1) + `"`, nil, false, false},
		{"number", free, `5`, nil, false, false},
		{"object", single, `{"a":1}`, nil, false, false},
	} {
		got, empty, ok := parseAnswer(x.sh, raw(x.in))
		if ok != x.ok || empty != x.empty || (ok && !empty && !reflect.DeepEqual(got, x.want)) {
			t.Errorf("%s: got (%v, %v, %v)", x.name, got, empty, ok)
		}
	}
}

// The second guard before the brain: answers of more than brain.MaxAnswerLen
// characters (code points, not bytes) are not sent; exactly that many are.
func TestAnswerFitsBrainBoundary(t *testing.T) {
	if brain.MaxAnswerLen != 2000 {
		t.Fatalf("MaxAnswerLen = %d, the brain's limit is 2000", brain.MaxAnswerLen)
	}
	if maxFreeLen != brain.MaxAnswerLen {
		t.Fatalf("free-text cap %d must be the brain's cap %d", maxFreeLen, brain.MaxAnswerLen)
	}
	han := func(n int) string { return strings.Repeat("字", n) } // 3 bytes each
	for name, c := range map[string]struct {
		ans  any
		want bool
	}{
		"2000 chars":           {han(2000), true},
		"2001 chars":           {han(2001), false},
		"2000 ascii":           {strings.Repeat("a", 2000), true},
		"2001 ascii":           {strings.Repeat("a", 2001), false},
		"2000 emoji (4 bytes)": {strings.Repeat("😀", 2000), true},
		"2001 emoji":           {strings.Repeat("😀", 2001), false},
		"blanks all 2000":      {[]string{han(2000), han(2000)}, true},
		"one blank 2001":       {[]string{"x", han(2001)}, false},
		"short choice":         {"B", true},
		"multi choice":         {[]string{"A", "C"}, true},
	} {
		if got := answerFitsBrain(c.ans); got != c.want {
			t.Errorf("%s: answerFitsBrain = %v, want %v", name, got, c.want)
		}
	}
	// free text goes through the same boundary on the way in
	free := shapeOf(brain.Question{QType: "calc", Stem: "no blanks"})
	for n, want := range map[int]bool{2000: true, 2001: false} {
		raw, _ := json.Marshal(han(n))
		if _, _, ok := parseAnswer(free, raw); ok != want {
			t.Errorf("free text of %d characters: ok=%v, want %v", n, ok, want)
		}
	}
}
