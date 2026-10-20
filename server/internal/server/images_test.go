package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/abcd9845/ruyi-english-daily/server/internal/auth"
)

func imgEnv(t *testing.T) (*env, string) {
	e, _, ck := queueEnv(t, opts{images: true})
	e.brain.q.Set("images", "1") // the mock brain accepts items[].images (PR-I2)
	return e, ck
}

func upload(t *testing.T, e *env, ck, set, ct string, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/practice/"+set+"/images", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	if ck != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: ck})
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func pngN(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(fmt.Sprintf("img-%d", n))...)
}

func shaOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func imgAnswers(cid string, imgs map[string][]string, a map[string]any) string {
	type an struct {
		QRef   string   `json:"qref"`
		Answer any      `json:"answer"`
		Images []string `json:"images,omitempty"`
	}
	var list []an
	for _, q := range []string{q3, q4, q5} {
		if v, ok := a[q]; ok {
			list = append(list, an{q, v, imgs[q]})
		}
	}
	b, _ := json.Marshal(map[string]any{"client_attempt_id": cid, "answers": list})
	return string(b)
}

func TestImagesConfigOnlyWhenOn(t *testing.T) {
	e, ck := imgEnv(t)
	_, b := e.do(t, "GET", "/api/config", "", nil, ck)
	if !strings.Contains(string(b), `"images":{`) || !strings.Contains(string(b), `"max_count":6`) {
		t.Errorf("images on: %s", b)
	}
	off, _, ck2 := queueEnv(t, opts{})
	_, b = off.do(t, "GET", "/api/config", "", nil, ck2)
	if strings.Contains(string(b), "images") {
		t.Errorf("images off must not be announced: %s", b)
	}
	resp, _ := upload(t, off, ck2, "tri-3", "image/png", pngN(1))
	if resp.StatusCode != 404 {
		t.Errorf("upload with images off: %d", resp.StatusCode)
	}
}

func TestImageUploadValidation(t *testing.T) {
	e, ck := imgEnv(t)
	resp, m := upload(t, e, ck, "tri-3", "image/png", pngN(1))
	if resp.StatusCode != 200 || m["sha256"] != shaOf(pngN(1)) {
		t.Fatalf("ok upload: %d %v", resp.StatusCode, m)
	}
	// format
	resp, m = upload(t, e, ck, "tri-3", "image/gif", []byte("GIF89a...."))
	if resp.StatusCode != 415 || !strings.Contains(fmt.Sprint(m["message"]), "JPG 或 PNG") {
		t.Errorf("gif: %d %v", resp.StatusCode, m)
	}
	resp, m = upload(t, e, ck, "tri-3", "image/png", []byte("not really a png"))
	if resp.StatusCode != 415 {
		t.Errorf("bad magic: %d %v", resp.StatusCode, m)
	}
	// size
	big := append(pngN(0), make([]byte, 5<<20)...)
	resp, m = upload(t, e, ck, "tri-3", "image/png", big)
	if resp.StatusCode != 413 || !strings.Contains(fmt.Sprint(m["message"]), "5MB") {
		t.Errorf("big: %d %v", resp.StatusCode, m)
	}
	// brain refusals are mapped to Chinese messages
	resp, m = upload(t, e, ck, "tri-3", "image/png", []byte("\x89PNG\r\n\x1a\nQUOTA"))
	if resp.StatusCode != 429 || m["error"] != "media_daily_quota" || !strings.Contains(fmt.Sprint(m["message"]), "上限") {
		t.Errorf("quota: %d %v", resp.StatusCode, m)
	}
	// login required
	resp, _ = upload(t, e, "", "tri-3", "image/png", pngN(1))
	if resp.StatusCode != 401 {
		t.Errorf("no cookie: %d", resp.StatusCode)
	}
	resp, _ = upload(t, e, ck, "nope", "image/png", pngN(1))
	if resp.StatusCode != 404 {
		t.Errorf("unknown set: %d", resp.StatusCode)
	}
}

func TestImagesTravelWithTheQueuedAnswer(t *testing.T) {
	e, ck := imgEnv(t)
	var shas []string
	for i := 1; i <= 2; i++ {
		_, m := upload(t, e, ck, "tri-3", "image/png", pngN(i))
		shas = append(shas, m["sha256"].(string))
	}
	body := imgAnswers(cid1, map[string][]string{q3: shas}, map[string]any{q3: "见图", q4: []string{"x=5"}, q5: "7"})
	_, q := e.qgrade(t, ck, "tri-3", body)
	if q.enq(q3) != "ok" {
		t.Fatalf("q3 not queued: %+v", q.Queue)
	}
	if got := e.brain.q.Images(); strings.Join(got, ",") != strings.Join(shas, ",") {
		t.Errorf("enqueue images = %v, want %v", got, shas)
	}
	// grade request itself never carries images
	if strings.Contains(fmt.Sprint(e.brain.lastGradeReq()), "images") {
		t.Errorf("grade body must not carry images: %v", e.brain.lastGradeReq())
	}
}

func TestImagesRefusedWhenInvalid(t *testing.T) {
	e, ck := imgEnv(t)
	_, m := upload(t, e, ck, "tri-3", "image/png", pngN(1))
	sha := m["sha256"].(string)
	cases := []struct {
		name string
		imgs []string
		ans  any
		code string
	}{
		{"unknown sha", []string{strings.Repeat("a", 64)}, "x", "image_not_uploaded"},
		{"bad sha", []string{"xyz"}, "x", "bad_images"},
		{"dup", []string{sha, sha}, "x", "bad_images"},
		{"seven", []string{sha, "1", "2", "3", "4", "5", "6"}, "x", "too_many_images"},
		{"no text", []string{sha}, "", "images_need_answer"},
	}
	for i, c := range cases {
		body := imgAnswers(uuid(100+i), map[string][]string{q3: c.imgs}, map[string]any{q3: c.ans, q5: "7"})
		resp, b, _ := e.submit(t, ck, "tri-3", body)
		if resp.StatusCode != 400 || !strings.Contains(string(b), c.code) || !strings.Contains(string(b), `"message"`) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, b)
		}
	}
}

func TestImagesOffRefusesImagesButOldSubmitsUnchanged(t *testing.T) {
	e, _, ck := queueEnv(t, opts{})
	body := imgAnswers(cid1, map[string][]string{q3: {strings.Repeat("a", 64)}}, map[string]any{q3: "x"})
	if resp, b, _ := e.submit(t, ck, "tri-3", body); resp.StatusCode != 400 || !strings.Contains(string(b), "images_not_enabled") {
		t.Errorf("images off: %d %s", resp.StatusCode, b)
	}
	// a submission without images is accepted as before
	if _, q := e.qgrade(t, ck, "tri-3", triAnswers(cid2)); q.Queue == nil {
		t.Errorf("plain submit: %+v", q)
	}
}

// nginx answers an oversized body with its own HTML 413 (no JSON code): still the size message.
func TestImageHTML413MappedByStatus(t *testing.T) {
	e, ck := imgEnv(t)
	resp, m := upload(t, e, ck, "tri-3", "image/png", []byte("\x89PNG\r\n\x1a\nHTML413"))
	if resp.StatusCode != 413 || !strings.Contains(fmt.Sprint(m["message"]), "5MB") {
		t.Errorf("html 413: %d %v", resp.StatusCode, m)
	}
}

// Production today: the brain's RUYI_QUEUE_IMAGES_ENABLED is off -> enqueue with images is
// refused (422 images_not_enabled). The submission still succeeds (grade recorded), the
// answer is marked not queued and the page is told the images did not go.
func TestBrainImagesOffDegradesGracefully(t *testing.T) {
	e, _, ck := queueEnv(t, opts{images: true}) // mock brain images switch stays off
	_, m := upload(t, e, ck, "tri-3", "image/png", pngN(9))
	sha := m["sha256"].(string)
	body := imgAnswers(cid1, map[string][]string{q3: {sha}}, map[string]any{q3: "见图"})
	resp, b, _ := e.submit(t, ck, "tri-3", body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"images_not_sent":["`+q3+`"]`) || !strings.Contains(string(b), "images_not_enabled") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}
