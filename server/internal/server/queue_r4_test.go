package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /api/config carries "queue":true only when the queue is on: with it off the answer is the
// #22 one byte for byte (the page reads this to word the daily limit).
func TestConfigSaysQueueOnlyWhenItIsOn(t *testing.T) {
	on, _, ck := queueEnv(t, opts{})
	_, b := on.do(t, "GET", "/api/config", "", nil, ck)
	if !strings.Contains(string(b), `"queue":true`) {
		t.Errorf("queue on: %s", b)
	}
	off := newEnv(t, opts{sets: queueSets, grade: true})
	_, b = off.do(t, "GET", "/api/config", "", nil, off.session(t))
	if strings.Contains(string(b), "queue") {
		t.Errorf("queue off must not mention it: %s", b)
	}
}

// P3-B: the page text of retry_exhausted promises nothing about a parent notice (it may be
// delayed until the whole set is processed, or never sent when the poller is off).
func TestRetryExhaustedWordingPromisesNoNotice(t *testing.T) {
	e, _, _ := queueEnv(t, opts{})
	got := e.app.TNotQueuedLabel("retry_exhausted")
	if got != "暂时没能排队，答案已存档" {
		t.Errorf("%q", got)
	}
	for _, w := range []string{"家长会看", "家长会核对", "已通知"} {
		if strings.Contains(got, w) {
			t.Errorf("%q must not say %q", got, w)
		}
	}
}

// P3-D (r4 item 5): a poller that ran on an empty queue (nothing to save, so no notify.json and no
// start marker), was switched off and on again after submissions exist, is a first start, not a loss.
func TestPollerOnEmptyQueueThenOffOnIsInfoNotWarn(t *testing.T) {
	e, n, ck := queueEnv(t, opts{queueNotify: true}) // poller on, empty queue
	pollOnce(t, e)
	e.app.CloseQueue()
	if _, err := os.Stat(filepath.Join(e.qDir, ".notify-started")); err == nil {
		t.Fatal("the start marker must not exist before notify.json was ever written")
	}
	// poller off, a submission comes in
	e2 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true})
	ck2 := e2.session(t)
	_ = ck
	submitTri(t, e2, ck2, cid1)
	e2.app.CloseQueue()
	// poller on again
	e3 := newEnv(t, opts{reuse: e, sets: queueSets, grade: true, ntfyURL: n.url(), queue: true, queueNotify: true})
	pollOnce(t, e3)
	l := e3.logs.String()
	if strings.Contains(l, "level=WARN msg=\"queue notify state was missing or damaged") || !strings.Contains(l, "first start of the poller") {
		t.Errorf("a poller that never had anything to save is not a state loss:\n%s", l)
	}
}

// ---- r5: compare_outcome compares the AI assistants with each other, never the child's answer ----

func compareItem(t *testing.T, o map[string]string, to string) qViewItem {
	t.Helper()
	e, _, ck := queueEnv(t, opts{})
	e.qgrade(t, ck, "pend-2", answersJSON(cid1, map[string]any{q3: "free text"}))
	must(t, e.brain.q.Advance(0, q3, to, o))
	return e.qview(t, ck, cid1).item(q3)
}

func TestCompareOutcomeWordingIsAboutTheAIsNotTheChild(t *testing.T) {
	bad := func(s string) bool {
		return strings.Contains(s, "和你的") || strings.Contains(s, "你答") || strings.Contains(s, "你的答案")
	}
	// match + text
	it := compareItem(t, map[string]string{"compare": "match", "answer": "AI-A", "rationale": "AI-R"}, "compared")
	if it.CompareText != "两位 AI 助手的答案一致，待家长核" || it.Result == nil || it.Result.ProposedAnswer != "AI-A" || it.Label != "AI 初评 · 待家长核" {
		t.Errorf("match: %+v", it)
	}
	// mismatch / n/a: the real brain sends no proposed answer -> no result at all, only the plain sentence
	for _, c := range []string{"mismatch", "n/a"} {
		for _, to := range []string{"compared", "judged"} {
			it := compareItem(t, map[string]string{"compare": c}, to)
			if it.UI != "ai_first_pass" || it.CompareText != "AI 没有一致答案，需要家长看" || it.Result != nil || it.Label != "AI 初评 · 待家长核" {
				t.Errorf("%s/%s: %+v", c, to, it)
			}
		}
	}
	// match without text is the abnormal case: a plain fallback line, still no empty result
	it = compareItem(t, map[string]string{"compare": "match", "no_answer": "1"}, "compared")
	if it.UI != "ai_first_pass" || it.Result != nil || it.CompareText != "AI 初评已出，详情请家长查看" {
		t.Errorf("match without fields: %+v", it)
	}
	for _, x := range []qViewItem{it, compareItem(t, map[string]string{"compare": "mismatch"}, "compared"), compareItem(t, map[string]string{"compare": "match", "answer": "a"}, "compared")} {
		if bad(x.CompareText) || bad(x.Label) {
			t.Errorf("a judgement of the child's answer: %+v", x)
		}
	}
	// the text only exists from compared on (never while the bot has only proposed)
	if it := compareItem(t, map[string]string{"compare": "match", "answer": "a"}, "proposed"); it.CompareText != "" || it.UI != "ai_working" {
		t.Errorf("proposed: %+v", it)
	}
}

// r5 P3-G: /api/config says "notify":true only when the completion poller runs (and only with the queue on);
// the queue-off answer stays the #22 one.
func TestConfigSaysNotifyOnlyWhenThePollerRuns(t *testing.T) {
	has := func(e *env, ck, key string) bool {
		_, b := e.do(t, "GET", "/api/config", "", nil, ck)
		return strings.Contains(string(b), `"`+key+`"`)
	}
	qOnly, _, ck := queueEnv(t, opts{})
	if !has(qOnly, ck, "queue") || has(qOnly, ck, "notify") {
		t.Errorf("queue on, poller off: want queue and no notify")
	}
	both, _, ck2 := queueEnv(t, opts{queueNotify: true})
	if !has(both, ck2, "queue") || !has(both, ck2, "notify") {
		t.Errorf("queue on, poller on: want queue and notify")
	}
	off := newEnv(t, opts{sets: queueSets, grade: true})
	if has(off, off.session(t), "queue") || has(off, off.session(t), "notify") {
		t.Errorf("queue off: neither key")
	}
}
