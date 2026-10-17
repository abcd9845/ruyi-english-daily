package staticfiles

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseAllowlist(t *testing.T) {
	got, err := ParseAllowlist("# c\n\nindex.html\n.nojekyll\nunits/a/index.html\nindex.html\n")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"../x", "/abs", "a/../b", "a//b", "a\\b", "dir/", "./x", ".", "a/./b"} {
		if _, err := ParseAllowlist(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBytesUnchangedRangeAndTypes(t *testing.T) {
	dir := t.TempDir()
	html := []byte("<!DOCTYPE html>\r\n<p>中文 \x00\xff raw bytes</p>\n")
	mp3 := bytes.Repeat([]byte{0xAB, 0xCD}, 5000)
	_ = os.MkdirAll(filepath.Join(dir, "u"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "index.html"), html, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "u", "a.mp3"), mp3, 0o644)
	h, missing := New(dir, []string{"index.html", "u/a.mp3", "ghost.html"})
	if len(missing) != 1 || missing[0] != "ghost.html" {
		t.Fatalf("missing: %v", missing)
	}
	get := func(p string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", p, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), html) || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("html: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	w = get("/u/a.mp3", map[string]string{"Range": "bytes=0-9"})
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), mp3[:10]) || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("range: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if get("/ghost.html", nil).Code != 404 {
		t.Fatal("missing allowlisted file must 404")
	}
	hr := httptest.NewRequest("HEAD", "/", nil)
	hw := httptest.NewRecorder()
	h.ServeHTTP(hw, hr)
	if hw.Code != 200 || hw.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %d", hw.Code, hw.Body.Len())
	}
}

func TestSymlinkOutsideNotServedIfNotAllowlisted(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o644)
	_ = os.Symlink("/etc/passwd", filepath.Join(dir, "link.txt"))
	h, _ := New(dir, []string{"index.html"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/link.txt", nil))
	if w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}
