package embedded_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	ruyisite "github.com/abcd9845/ruyi-english-daily/server"
	"github.com/abcd9845/ruyi-english-daily/server/internal/embedded"
)

func TestServedSetIsExactlyTheEmbeddedFiles(t *testing.T) {
	h, err := embedded.New(ruyisite.Pages(), ruyisite.KaTeX())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"/m/practice/": true, "/m/practice/index.html": true}
	err = fs.WalkDir(ruyisite.KaTeX(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			want["/vendor/katex/"+p] = true
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := h.Paths()
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("served %d paths, embedded %d", len(got), len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected served path %s", p)
		}
	}
	// no non-woff2 font, no stray files
	for _, p := range got {
		if strings.HasPrefix(p, "/vendor/katex/fonts/") && !strings.HasSuffix(p, ".woff2") {
			t.Errorf("non-woff2 font embedded: %s", p)
		}
	}
	for _, must := range []string{"/vendor/katex/katex.min.js", "/vendor/katex/katex.min.css", "/vendor/katex/LICENSE", "/vendor/katex/fonts/KaTeX_Main-Regular.woff2"} {
		if !want[must] {
			t.Errorf("missing %s", must)
		}
	}
}

// Every font the css points at must exist; the css must not point anywhere else.
func TestCSSOnlyReferencesEmbeddedWoff2(t *testing.T) {
	css, err := fs.ReadFile(ruyisite.KaTeX(), "katex.min.css")
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\(([^)]*)\)`).FindAllStringSubmatch(string(css), -1)
	if len(urls) != 20 {
		t.Errorf("expected 20 font urls, got %d", len(urls))
	}
	for _, m := range urls {
		u := m[1]
		if !strings.HasPrefix(u, "/vendor/katex/fonts/") || !strings.HasSuffix(u, ".woff2") {
			t.Errorf("css url %q", u)
			continue
		}
		if _, err := fs.Stat(ruyisite.KaTeX(), strings.TrimPrefix(u, "/vendor/katex/")); err != nil {
			t.Errorf("css refers to missing %s", u)
		}
	}
	if strings.Contains(string(css), "http") || strings.Contains(string(css), "@import") {
		t.Error("css must not reference external resources")
	}
}

func TestPageHasNoExternalReferences(t *testing.T) {
	b, err := fs.ReadFile(ruyisite.Pages(), "m/practice/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)(https?:)?//[a-z0-9]`).Match(b) {
		t.Errorf("page references an external or protocol-relative URL: %s", regexp.MustCompile(`(?i)(https?:)?//[a-z0-9][^"' )]*`).Find(b))
	}
	for _, bad := range []string{"localStorage.setItem('ruyi-family", "sessionStorage"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("page contains %q", bad)
		}
	}
}

func TestHandlerBasics(t *testing.T) {
	h, _ := embedded.New(ruyisite.Pages(), ruyisite.KaTeX())
	for _, p := range []string{"/m/practice/", "/m/practice", "/vendor/anything", "/vendor/katex/katex.min.js"} {
		if !h.Handles(p) {
			t.Errorf("Handles(%q)=false", p)
		}
	}
	for _, p := range []string{"/", "/m/numberline-4y/", "/m/practice/x", "/index.html", "/vendors/x"} {
		if h.Handles(p) {
			t.Errorf("Handles(%q)=true", p)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("HEAD", "/m/practice/", nil))
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d body=%d", rec.Code, rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/m/practice/", nil))
	if rec.Code != 405 {
		t.Errorf("PUT: %d", rec.Code)
	}
}
