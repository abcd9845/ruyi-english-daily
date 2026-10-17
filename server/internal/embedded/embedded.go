// Package embedded serves the files compiled into the binary: the practice
// page and the KaTeX library. Only exact, known paths are served; everything
// else (including any other /vendor/… path) is not handled here.
package embedded

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// PracticeHTML is the page's file name inside the page filesystem.
const (
	PracticePath = "/m/practice/"
	vendorPrefix = "/vendor/katex/"
	pageCSP      = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
		"font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

type file struct {
	data []byte
	ctyp string
	etag string
	page bool
}

// Handler serves the embedded files.
type Handler struct {
	files map[string]*file // exact URL path -> file
}

// New indexes pages (served under /m/practice/) and vendor (served under
// /vendor/katex/). pages must contain "m/practice/index.html"; vendor holds the
// katex files at its root (katex.min.js, katex.min.css, LICENSE, fonts/…).
func New(pages, vendor fs.FS) (*Handler, error) {
	h := &Handler{files: map[string]*file{}}
	idx, err := fs.ReadFile(pages, "m/practice/index.html")
	if err != nil {
		return nil, err
	}
	pf := mk(idx, "text/html; charset=utf-8", true)
	h.files["/m/practice/"] = pf
	h.files["/m/practice/index.html"] = pf
	err = fs.WalkDir(vendor, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(vendor, p)
		if err != nil {
			return err
		}
		h.files[vendorPrefix+p] = mk(b, ctype(p), false)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func mk(b []byte, ct string, page bool) *file {
	sum := sha256.Sum256(b)
	return &file{data: b, ctyp: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, page: page}
}

func ctype(p string) string {
	switch path.Ext(p) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	}
	return "text/plain; charset=utf-8"
}

// Paths lists every URL path this handler answers (for tests and docs).
func (h *Handler) Paths() []string {
	out := make([]string, 0, len(h.files))
	for p := range h.files {
		out = append(out, p)
	}
	return out
}

// Handles reports whether p is exactly one of the embedded paths, or a path
// the handler answers with a redirect or 404 of its own (/m/practice, /vendor/…).
func (h *Handler) Handles(p string) bool {
	if _, ok := h.files[p]; ok {
		return true
	}
	return p == "/m/practice" || p == "/vendor" || strings.HasPrefix(p, "/vendor/")
}

// ServeHTTP implements http.Handler for paths where Handles is true.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	if p == "/m/practice" {
		target := PracticePath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	f, ok := h.files[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", f.ctyp)
	hd.Set("ETag", f.etag)
	if f.page {
		hd.Set("Cache-Control", "no-cache")
		hd.Set("Content-Security-Policy", pageCSP)
	} else {
		hd.Set("Cache-Control", "public, max-age=86400")
	}
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// zero modtime: validators come from the ETag above.
	http.ServeContent(w, r, "", time.Time{}, bytesReader(f.data))
}
