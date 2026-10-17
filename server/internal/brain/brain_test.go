package brain

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTLS(t *testing.T, h http.Handler) (*httptest.Server, *url.URL) {
	ts := httptest.NewTLSServer(h)
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return ts, u
}

var okList = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
})

func TestTLSStrictInsecureAndCA(t *testing.T) {
	ts, u := newTLS(t, okList)
	ctx := context.Background()

	strict, _ := New(Config{BaseURL: u, Token: "tok"})
	_, err := strict.Source(ctx, "s.x", true)
	var be *Error
	if !errors.As(err, &be) || be.Kind != KindUnavailable || !strings.Contains(be.What, "tls") {
		t.Fatalf("strict must fail with a TLS error, got %v", err)
	}

	insecure, _ := New(Config{BaseURL: u, Token: "tok", InsecureTLS: true})
	if _, err := insecure.Source(ctx, "s.x", true); err != nil {
		t.Fatalf("insecure: %v", err)
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600)
	withCA, err := New(Config{BaseURL: u, Token: "tok", CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withCA.Source(ctx, "s.x", true); err != nil {
		t.Fatalf("ca file: %v", err)
	}
	_ = x509.NewCertPool
	bad := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(bad, []byte("not pem"), 0o600)
	if _, err := New(Config{BaseURL: u, Token: "tok", CAFile: bad}); err == nil {
		t.Fatal("bad CA file must fail")
	}
}

func TestRedirectsNotFollowedAndTokenNotForwarded(t *testing.T) {
	var hits int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer other.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusFound)
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "tok"})
	if _, err := c.Source(context.Background(), "s.x", true); err == nil {
		t.Fatal("redirect must be an error")
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestOversizeListRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"qref":"a","stem":"`))
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte(`"}],"next_cursor":null}`))
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "tok"})
	if _, err := c.Source(context.Background(), "s.x", true); err == nil {
		t.Fatal("oversize body must be rejected")
	}
}

func TestOversizeAndWrongTypeImage(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ticket"):
			_, _ = w.Write([]byte(`{"ticket":"v1.1.aa","expires_at":"2099-01-01T00:00:00Z"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/media/"+strings.Repeat("a", 64)):
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", "99999999")
			_, _ = w.Write([]byte("x"))
		default:
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg onload=alert(1)/>"))
		}
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "tok"})
	if _, err := c.FetchMedia(context.Background(), strings.Repeat("a", 64)); err == nil {
		t.Fatal("too large must fail")
	}
	if _, err := c.FetchMedia(context.Background(), strings.Repeat("b", 64)); err == nil {
		t.Fatal("svg must be refused")
	}
	if _, err := c.FetchMedia(context.Background(), "../etc/passwd"); err == nil {
		t.Fatal("bad id must fail")
	}
}

func TestPaginationAndFiltering(t *testing.T) {
	pages := map[string]string{
		"":   `{"items":[{"qref":"c","number":"10(1)","stem":"s","media":[]},{"qref":"b","number":"2(1)","stem":"s","media":[]}],"next_cursor":"p2"}`,
		"p2": `{"items":[{"qref":"a","number":"1(1)","stem":"s"},{"qref":"other","source":"someone.else","number":"0(0)","media":[]}],"next_cursor":null}`,
	}
	var keys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.URL.Query() {
			keys = append(keys, k)
		}
		if r.URL.Query().Get("source") != "s.x" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(pages[r.URL.Query().Get("cursor")]))
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "tok"})
	items, err := c.Source(context.Background(), "s.x", true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, q := range items {
		got = append(got, q.QRef)
	}
	if strings.Join(got, ",") != "a,b,c" { // paginated, foreign-source row dropped, sorted 1(1) 2(1) 10(1)
		t.Fatalf("order: %v", got)
	}
	for _, k := range keys {
		if k != "source" && k != "limit" && k != "cursor" {
			t.Errorf("forbidden query parameter sent: %s", k)
		}
	}
}

func q(number *string, ref string) Question { return Question{QRef: ref, Number: number} }
func sp(s string) *string                   { return &s }

func TestSortByMajorMinorSuffix(t *testing.T) {
	in := []Question{
		q(sp("10(1)"), "a"), q(sp("2(10)"), "b"), q(sp("2(2)"), "c"), q(sp("11(2)③"), "d"), q(sp("11(2)①"), "e"),
		q(sp("11(2)②"), "f"), q(sp("11(1)"), "g"), q(sp("1(1)"), "h"), q(sp("18(4)"), "i"), q(sp("9(3)"), "j"),
		q(sp("weird"), "u1"), q(nil, "u2"), q(sp("5"), "u3"), q(sp("2(1)b"), "k"), q(sp("2(1)a"), "l"), q(sp("2(1)"), "m"),
	}
	SortQuestions(in)
	var got []string
	for _, x := range in {
		got = append(got, x.QRef)
	}
	// 1(1) 2(1) 2(1)a 2(1)b 2(2) 2(10) 9(3) 10(1) 11(1) 11(2)① 11(2)② 11(2)③ 18(4) then unparsable in original order
	want := "h,m,l,k,c,b,j,a,g,e,f,d,i,u1,u2,u3"
	if strings.Join(got, ",") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, ","), want)
	}
	// stable for equal keys
	dup := []Question{q(sp("3(1)"), "x"), q(sp("3(1)"), "y"), q(sp("3(1)"), "z")}
	SortQuestions(dup)
	if dup[0].QRef != "x" || dup[1].QRef != "y" || dup[2].QRef != "z" {
		t.Fatal("sort not stable")
	}
}

func TestFigureNormalization(t *testing.T) {
	for in, want := range map[string]string{"ok": "ok", "missing": "missing", "none": "none", "unknown": "unknown", "": "unknown", "OK": "unknown", "weird": "unknown"} {
		s := in
		if got := (Question{FigureStatus: &s}).Figure(); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
	if (Question{}).Figure() != "unknown" {
		t.Error("absent key must be unknown")
	}
}

func TestErrorsCarryNoTicketOrURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ticket") {
			_, _ = w.Write([]byte(`{"ticket":"v1.1.SECRETTICKET","expires_at":"2099-01-01T00:00:00Z"}`))
			return
		}
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close() // abort mid-request: net/http error text would contain the URL with the ticket
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c, _ := New(Config{BaseURL: u, Token: "tok", Now: time.Now})
	_, err := c.FetchMedia(context.Background(), strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRETTICKET") || strings.Contains(err.Error(), "ticket=") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error leaks: %v", err)
	}
}
