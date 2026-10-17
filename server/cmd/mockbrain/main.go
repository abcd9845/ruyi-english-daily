// Command mockbrain is a TEST-ONLY stand-in for the learning-center API. It
// implements the phase-2 contract paths and error codes with synthetic data
// (60 numberline questions, question 17 without a figure) and is used by the
// integration/acceptance tests. It is not part of the deployed binary.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const source = "math-intake.zz-yuekao-numberline-4y"

var digits = map[rune][5]string{
	'0': {"111", "101", "101", "101", "111"}, '1': {"010", "110", "010", "010", "111"},
	'2': {"111", "001", "111", "100", "111"}, '3': {"111", "001", "111", "001", "111"},
	'4': {"101", "101", "111", "001", "001"}, '5': {"111", "100", "111", "001", "111"},
	'6': {"111", "100", "111", "101", "111"}, '7': {"111", "001", "001", "001", "001"},
	'8': {"111", "101", "111", "101", "111"}, '9': {"111", "101", "111", "001", "111"},
}

func makePNG(n int, variant int) ([]byte, string) {
	img := image.NewRGBA(image.Rect(0, 0, 360, 120))
	bg := color.RGBA{uint8(235 - n%5*8), uint8(245 - n%7*6), uint8(240 - variant*20), 255}
	for y := 0; y < 120; y++ {
		for x := 0; x < 360; x++ {
			img.Set(x, y, bg)
		}
	}
	ink := color.RGBA{30, 41, 59, 255}
	for x := 20; x < 340; x++ { // number line
		img.Set(x, 80, ink)
		img.Set(x, 81, ink)
	}
	for i := 0; i <= 8; i++ { // ticks
		for y := 72; y < 90; y++ {
			img.Set(20+i*40, y, ink)
		}
	}
	x0 := 20
	for _, ch := range strconv.Itoa(n) {
		for r, row := range digits[ch] {
			for c, b := range row {
				if b == '1' {
					for dy := 0; dy < 6; dy++ {
						for dx := 0; dx < 6; dx++ {
							img.Set(x0+c*6+dx, 10+r*6+dy, ink)
						}
					}
				}
			}
		}
		x0 += 24
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

type media struct {
	SHA256    string `json:"sha256"`
	Mime      string `json:"mime"`
	Bytes     int    `json:"bytes"`
	Ordinal   int    `json:"ordinal"`
	Slot      string `json:"slot"`
	OptionKey string `json:"option_key"`
	Alt       string `json:"alt"`
}

type question struct {
	QRef    string          `json:"qref"`
	Subject string          `json:"subject"`
	Source  string          `json:"source"`
	Set     string          `json:"set"`
	Number  string          `json:"number"`
	QType   string          `json:"qtype"`
	Stem    string          `json:"stem"`
	Options json.RawMessage `json:"options,omitempty"`
	Media   []media         `json:"media"`
}

func selfSigned() tls.Certificate {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "mock brain"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

func main() {
	token := os.Getenv("MOCK_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "MOCK_TOKEN required")
		os.Exit(1)
	}
	listen := os.Getenv("MOCK_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:18090"
	}
	ttl := 15 * time.Minute
	if v := os.Getenv("MOCK_TICKET_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			ttl = d
		}
	}
	pageSize := 500
	if v := os.Getenv("MOCK_PAGE_SIZE"); v != "" {
		pageSize, _ = strconv.Atoi(v)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	var kmu sync.Mutex
	var offset atomic.Int64 // test hook: advances the mock's clock (nanoseconds)
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }

	images := map[string][]byte{}
	var qs []question
	for n := 1; n <= 60; n++ {
		q := question{QRef: fmt.Sprintf("math:%s:q%02d", source, n), Subject: "math", Source: source, Set: "main",
			Number: strconv.Itoa(n), Media: []media{}}
		if n <= 30 {
			q.QType = "single_choice"
			q.Stem = fmt.Sprintf("（模拟题 %d）数轴上点 A 表示的数是？", n)
			q.Options = json.RawMessage(`[{"key":"A","text":"-2"},{"key":"B","text":"-1"},{"key":"C","text":"1"},{"key":"D","text":"2"}]`)
		} else {
			q.QType = "fill_blank"
			q.Stem = fmt.Sprintf("（模拟题 %d）在数轴上，与原点距离为 3 的点表示的数是 ____ 。", n)
		}
		if n != 17 {
			b, sha := makePNG(n, 0)
			images[sha] = b
			q.Media = append(q.Media, media{SHA256: sha, Mime: "image/png", Bytes: len(b), Ordinal: 1, Slot: "stem", Alt: fmt.Sprintf("第%d题数轴", n)})
		}
		if n == 5 { // an option figure too
			b, sha := makePNG(5, 1)
			images[sha] = b
			q.Media = append(q.Media, media{SHA256: sha, Mime: "image/png", Bytes: len(b), Ordinal: 2, Slot: "option", OptionKey: "B", Alt: "选项B图"})
		}
		qs = append(qs, q)
	}
	// Real server orders by updated_at desc: emulate with reverse order.
	sort.Slice(qs, func(i, j int) bool { return qs[i].QRef > qs[j].QRef })

	var smu sync.Mutex
	stats := map[string]int{}
	bump := func(k string) { smu.Lock(); stats[k]++; smu.Unlock() }

	apiErr := func(w http.ResponseWriter, code int, id string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": id}})
	}
	bearer := func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer "+token
	}
	mac := func(sha string, exp int64) string {
		kmu.Lock()
		defer kmu.Unlock()
		m := hmac.New(sha256.New, key)
		fmt.Fprintf(m, "%s|%d", sha, exp)
		return hex.EncodeToString(m.Sum(nil))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/bank/questions", func(w http.ResponseWriter, r *http.Request) {
		bump("list")
		if !bearer(r) {
			apiErr(w, 401, "unauthorized")
			return
		}
		for k := range r.URL.Query() {
			if k != "source" && k != "limit" && k != "cursor" {
				apiErr(w, 400, "invalid_query")
				return
			}
		}
		if r.URL.Query().Get("source") != source {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
			return
		}
		limit := pageSize
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v < limit {
			limit = v
		}
		start := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(c, "c"))
		}
		end := start + limit
		var next any
		if end < len(qs) {
			next = "c" + strconv.Itoa(end)
		} else {
			end = len(qs)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": qs[start:end], "next_cursor": next})
	})
	mux.HandleFunc("POST /v1/media/{sha}/ticket", func(w http.ResponseWriter, r *http.Request) {
		bump("ticket")
		if !bearer(r) {
			apiErr(w, 401, "unauthorized")
			return
		}
		if r.ContentLength > 0 && r.Header.Get("Content-Type") != "application/json" {
			apiErr(w, 415, "unsupported_media_type")
			return
		}
		sha := r.PathValue("sha")
		if _, ok := images[sha]; !ok {
			apiErr(w, 404, "not_found")
			return
		}
		exp := now().Add(ttl)
		t := fmt.Sprintf("v1.%d.%s", exp.Unix(), mac(sha, exp.Unix()))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ticket": t, "expires_at": exp.UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /v1/media/{sha}", func(w http.ResponseWriter, r *http.Request) {
		bump("media")
		if r.Header.Get("Authorization") != "" {
			bump("media_with_authorization") // contract: this route must not need Authorization
		}
		sha := r.PathValue("sha")
		t := r.URL.Query().Get("ticket")
		if t == "" {
			apiErr(w, 401, "ticket_required")
			return
		}
		p := strings.Split(t, ".")
		exp, err := strconv.ParseInt(func() string {
			if len(p) == 3 {
				return p[1]
			}
			return "x"
		}(), 10, 64)
		if len(p) != 3 || p[0] != "v1" || err != nil || !hmac.Equal([]byte(p[2]), []byte(mac(sha, exp))) {
			bump("media_bad_ticket")
			apiErr(w, 401, "ticket_invalid")
			return
		}
		if exp <= now().Unix() {
			bump("media_expired_ticket")
			apiErr(w, 401, "ticket_expired")
			return
		}
		b, ok := images[sha]
		if !ok {
			apiErr(w, 404, "not_found")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	})
	// Test hooks.
	mux.HandleFunc("POST /_mock/rotate-key", func(w http.ResponseWriter, r *http.Request) {
		kmu.Lock()
		_, _ = rand.Read(key)
		kmu.Unlock()
		_, _ = w.Write([]byte("rotated\n"))
	})
	mux.HandleFunc("POST /_mock/advance-clock", func(w http.ResponseWriter, r *http.Request) {
		d, err := time.ParseDuration(r.URL.Query().Get("d"))
		if err != nil {
			http.Error(w, "bad d", 400)
			return
		}
		offset.Add(int64(d))
		_, _ = w.Write([]byte("advanced\n"))
	})
	mux.HandleFunc("GET /_mock/stats", func(w http.ResponseWriter, r *http.Request) {
		smu.Lock()
		defer smu.Unlock()
		_ = json.NewEncoder(w).Encode(stats)
	})
	mux.HandleFunc("GET /_mock/shas", func(w http.ResponseWriter, r *http.Request) {
		// sha of question N's first image: /_mock/shas?n=7
		n := r.URL.Query().Get("n")
		for _, q := range qs {
			if q.Number == n && len(q.Media) > 0 {
				_, _ = w.Write([]byte(q.Media[0].SHA256 + "\n"))
				return
			}
		}
		http.NotFound(w, r)
	})
	fmt.Fprintln(os.Stderr, "mockbrain listening on", listen)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	var err error
	if os.Getenv("MOCK_TLS") == "1" {
		// Self-signed certificate for 127.0.0.1 (like the real brain's local listener).
		// MOCK_CA_OUT, if set, receives the certificate PEM for RUYI_BRAIN_CA_FILE tests.
		cert := selfSigned()
		if out := os.Getenv("MOCK_CA_OUT"); out != "" {
			_ = os.WriteFile(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o644)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mockbrain:", err)
		os.Exit(1)
	}
}
