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
