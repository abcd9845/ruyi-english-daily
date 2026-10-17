package server_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The queue switch-off guarantee: with RUYI_QUEUE_ENABLED=0 the site behaves byte-for-byte
// like the build before S1 (#22). testdata/golden-queue-off.txt was produced by the
// pre-S1 code (GOLDEN_WRITE=<file> go test -run TestGoldenQueueOff on origin/feat/daily-limit-ux)
// and must never be regenerated from S1 code.

var goldenTime = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)|\d\d:\d\d（北京时间）|"day":"[^"]*"`)

var goldenTopic = regexp.MustCompile(`/topic-[0-9a-f]+`)
var goldenDone = regexp.MustCompile(`完成于 [0-9:<>T]+`)

func normGolden(s string) string {
	s = goldenTime.ReplaceAllString(s, "<T>")
	s = goldenTopic.ReplaceAllString(s, "/<topic>")
	return goldenDone.ReplaceAllString(s, "完成于 <T>")
}

func TestGoldenQueueOff(t *testing.T) {
	e, n, ck := gradeEnv(t, opts{})
	var out strings.Builder
	rec := func(name string, code int, body []byte) {
		fmt.Fprintf(&out, "## %s %d\n%s\n", name, code, normGolden(strings.TrimSpace(string(body))))
	}
	hdr := map[string]string{"Content-Type": "application/json"}
	for _, step := range []struct{ name, set, body string }{
		{"graded-mix", "demo-1", answersJSON(cid1, goodAnswers)},
		{"replay", "demo-1", answersJSON(cid1, goodAnswers)},
		{"unsupported", "other-2", answersJSON(cid2, map[string]any{"example.set:math:4": []string{"x"}})},
		{"bad-shape", "demo-1", answersJSON(uuid(3), map[string]any{"example.set:math:3": []string{"a", "b"}})},
	} {
		resp, b := e.do(t, "POST", "/api/practice/"+step.set+"/grade", step.body, hdr, ck)
		rec(step.name, resp.StatusCode, b)
	}
	for _, p := range []string{"/api/practice/demo-1/quota", "/api/practice/other-2/quota", "/api/practice/attempts/" + cid1 + "/queue", "/api/practice/attempts/" + cid1 + "/enqueue"} {
		method := "GET"
		if strings.HasSuffix(p, "/enqueue") {
			method = "POST"
		}
		resp, b := e.do(t, method, p, "{}", hdr, ck)
		rec(method+" "+p, resp.StatusCode, b)
	}
	b, _ := os.ReadFile(filepath.Join(e.attDir, "attempts.jsonl"))
	rec("archive", 0, b)
	ntfyBodies := ""
	n.mu.Lock()
	for _, p := range n.posts {
		ntfyBodies += p.Path + " " + p.Body + "\n"
	}
	n.mu.Unlock()
	rec("ntfy", 0, []byte(ntfyBodies))
	got := out.String()
	if w := os.Getenv("GOLDEN_WRITE"); w != "" {
		must(t, os.WriteFile(w, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile("testdata/golden-queue-off.txt")
	must(t, err)
	if got != string(want) {
		_ = os.WriteFile(filepath.Join(t.TempDir(), "got.txt"), []byte(got), 0o644)
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("queue-off output differs from the pre-S1 golden at line %d:\n got: %s\nwant: %s", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("queue-off output length differs from the golden")
	}
}
