// Package staticfiles serves a fixed allowlist of files from a directory.
//
// Only paths named in the allowlist can ever be served. Anything else
// (server.log, .git/, .env, ../ tricks, directory listings) is a 404 by
// construction, because lookup is an exact match on the decoded URL path.
package staticfiles

import (
	"bufio"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Handler serves allowlisted files from dir.
type Handler struct {
	dir   string
	files map[string]struct{}
}

// ParseAllowlist parses one relative path per line; # comments and blank
// lines are ignored. Entries that are absolute, contain "..", backslashes,
// NULs, or are not in clean form are rejected.
func ParseAllowlist(text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "\\\x00") || strings.HasPrefix(line, "/") ||
			path.Clean(line) != line || line == "." || strings.HasPrefix(line, "../") ||
			strings.HasSuffix(line, "/") {
			return nil, fmt.Errorf("allowlist line %d: invalid path", n)
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// New builds a Handler. Missing files are not an error (they 404) but are
// returned so the caller can warn.
func New(dir string, entries []string) (*Handler, []string) {
	h := &Handler{dir: dir, files: make(map[string]struct{}, len(entries))}
	var missing []string
	for _, e := range entries {
		h.files[e] = struct{}{}
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(e))); err != nil || !st.Mode().IsRegular() {
			missing = append(missing, e)
		}
	}
	return h, missing
}

func (h *Handler) has(rel string) bool { _, ok := h.files[rel]; return ok }

func init() {
	// Make sure types used by the old pages are stable regardless of the OS mime db.
	_ = mime.AddExtensionType(".html", "text/html; charset=utf-8")
	_ = mime.AddExtensionType(".mp3", "audio/mpeg")
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path // already percent-decoded by net/http
	if p == "" || p[0] != '/' || strings.ContainsAny(p, "\x00\\") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(p, "/")
	switch {
	case rel == "":
		rel = "index.html"
	case strings.HasSuffix(rel, "/"):
		rel += "index.html"
	case !h.has(rel) && h.has(rel+"/index.html"):
		// Same behaviour as the old python server: /days -> /days/
		target := p + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	if !h.has(rel) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(h.dir, filepath.FromSlash(rel)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// ServeContent writes the bytes untouched and handles Range / If-Modified-Since.
	http.ServeContent(w, r, "", st.ModTime(), f)
}
