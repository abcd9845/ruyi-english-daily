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
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const source = "math-intake.zz-yuekao-numberline-4y"

// maxAnswerRunes mirrors the brain: answers (and each blank) are at most 2000 Unicode code points.
const maxAnswerRunes = 2000

var mockCIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)

// practiceSource is a second, purely synthetic source with 5 math questions in
// the shape of the real exam list (Markdown + LaTeX stems, ________ blanks,
// options with math). Nothing here is copied from a real paper. Every question
// also carries answer/explanation/solution fields (as a brain with a write
// token would send) so tests can prove the site strips them.
const practiceSource = "math.synthetic.p0-demo"

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
	// FigureStatus is a pointer so that "key absent" can be mocked (the brain
	// has not shipped the field yet).
	FigureStatus *string `json:"figure_status,omitempty"`
	// Only on practiceSource questions: must never reach a browser.
	Answer      string `json:"answer,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	Solution    string `json:"solution,omitempty"`
}

func practiceQuestions() []question {
	mk := func(n int, qtype, stem, opts string) question {
		q := question{QRef: fmt.Sprintf("example.set:math:%d", n), Subject: "math", Source: practiceSource, Set: "demo",
			Number: strconv.Itoa(n), QType: qtype, Stem: stem, Media: []media{},
			Answer: "MOCK-ANSWER-MARKER", Explanation: "MOCK-EXPLANATION-MARKER", Solution: "MOCK-SOLUTION-MARKER"}
		if opts != "" {
			q.Options = json.RawMessage(opts)
		}
		return q
	}
	return []question{
		// 1: inline fractions, absolute value, sub/superscripts, a $$ block, 3 blanks
		mk(1, "short_answer", "设 $m=-4$，$n=\\dfrac{2}{5}$，$k$ 为整数。\n\n(1) 计算 $\\left|m\\right|\\cdot n+k^{2}-m_{1}$ 得 ________ ；\n(2) 若 $t^{2}=16$，则 $t=$ ________ 或 ________ 。\n\n$$P=\\dfrac{m+n}{3}\\cdot w$$", ""),
		// 2: choice, math in the stem and the options, a full-width paren blank
		mk(2, "single_choice", "下列各数中，绝对值最小的是（ ）", `[{"key":"A","text":"$-2$","answer":true},{"key":"B","text":"$\\left|-\\dfrac{1}{3}\\right|$"},{"key":"C","text":"$0.5$"},{"key":"D","text":"$\\dfrac{3}{2}$"}]`),
		// 3: escaping traps: | and < inside math, \\ row separator in a block, HTML-looking text, **bold**, `code`, full-width blanks
		mk(3, "calculation", "**解方程组**（注意 `x<y` 的写法，下面这段 <script>window.__pwned=1</script> 只是文字）：\n\n$$\\begin{cases}x+y=3\\\\x-y=1\\end{cases}$$\n\n已知 $|x|<2$，求 $x=$ ＿＿＿＿＿ ，$y=$ ________ 。", ""),
		// 4, 5: alternates, not in the demo set (4 has no blank and no options: a free-form answer area)
		mk(4, "calculation", "（备选）计算 $\\dfrac{1}{2}+\\dfrac{1}{3}$，写出过程与结果。", ""),
		mk(5, "single_choice", "（备选）$2^{3}$ 等于（ ）", `[{"key":"A","text":"$6$"},{"key":"B","text":"$8$"}]`),
	}
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

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func main() {
	token := os.Getenv("MOCK_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "MOCK_TOKEN required")
		os.Exit(1)
	}
	// MOCK_BRAIN_ID is the one brain this mock serves (default "ruyi"): pick-practice and grade
	// accept only that id and reject every other, like a real brain's token scope.
	brainID := os.Getenv("MOCK_BRAIN_ID")
	if brainID == "" {
		brainID = "ruyi"
	}
	// The grade token (write scope of the brain): accepted by grade AND pick-practice. The
	// read token (bank:read) is refused by both with 403, like the real brain.
	gradeToken := os.Getenv("MOCK_GRADE_TOKEN")
	if gradeToken == "" {
		gradeToken = "grade-" + token
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
	// Question numbers look like the real exam: "1(1)", "11(2)③", "18(3)" ...
	// 18 major questions, 60 distinct numbers (6 majors have 4 parts, 12 have 3;
	// major 11 has 11(1), 11(2)①, 11(2)②, 11(2)③).
	var numbers []string
	for m := 1; m <= 18; m++ {
		switch m {
		case 11:
			numbers = append(numbers, "11(1)", "11(2)①", "11(2)②", "11(2)③")
		case 1, 2, 3, 4, 12:
			for i := 1; i <= 4; i++ {
				numbers = append(numbers, fmt.Sprintf("%d(%d)", m, i))
			}
		default:
			for i := 1; i <= 3; i++ {
				numbers = append(numbers, fmt.Sprintf("%d(%d)", m, i))
			}
		}
	}
	if len(numbers) != 60 {
		fmt.Fprintln(os.Stderr, "mock number table has", len(numbers), "entries, want 60")
		os.Exit(1)
	}
	str := func(v string) *string { return &v }
	noMedia := os.Getenv("MOCK_NO_MEDIA") == "1"
	var qs []question
	for n := 1; n <= 60; n++ { // n = position in the correct (sorted) order
		q := question{QRef: fmt.Sprintf("math:%s:q%02d", source, n), Subject: "math", Source: source, Set: "main",
			Number: numbers[n-1], Media: []media{}}
		if n <= 30 {
			q.QType = "single_choice"
			q.Stem = fmt.Sprintf("（模拟题 %s）数轴上点 A 表示的数是？", q.Number)
			q.Options = json.RawMessage(`[{"key":"A","text":"-2"},{"key":"B","text":"-1"},{"key":"C","text":"1"},{"key":"D","text":"2"}]`)
		} else {
			q.QType = "fill_blank" // options key omitted, as the real list does
			q.Stem = fmt.Sprintf("（模拟题 %s）在数轴上，与原点距离为 3 的点表示的数是 ____ 。", q.Number)
		}
		withImage := true
		switch n {
		case 17: // figure needed but not in the library
			q.FigureStatus, withImage = str("missing"), false
		case 25: // confirmed: no figure needed -> no placeholder at all
			q.FigureStatus, withImage = str("none"), false
		case 26: // figure_status key absent, no media -> unknown -> placeholder
			withImage = false
		case 27: // explicit unknown, no media -> placeholder
			q.FigureStatus, withImage = str("unknown"), false
		case 28: // inconsistent: says ok but media is empty -> placeholder + server warn
			q.FigureStatus, withImage = str("ok"), false
		case 29: // none, yet media exists -> the media is still shown
			q.FigureStatus = str("none")
		case 30: // media present, key absent (old brain)
		default:
			q.FigureStatus = str("ok")
		}
		if noMedia { // MOCK_NO_MEDIA=1: the real situation today - no figure has been ingested yet
			withImage, q.FigureStatus = false, nil
		}
		if withImage {
			b, sha := makePNG(n, 0)
			images[sha] = b
			q.Media = append(q.Media, media{SHA256: sha, Mime: "image/png", Bytes: len(b), Ordinal: 1, Slot: "stem", Alt: fmt.Sprintf("第%s题数轴", q.Number)})
		}
		if n == 5 && !noMedia { // an option figure too
			b, sha := makePNG(5, 1)
			images[sha] = b
			q.Media = append(q.Media, media{SHA256: sha, Mime: "image/png", Bytes: len(b), Ordinal: 2, Slot: "option", OptionKey: "B", Alt: "选项B图"})
		}
		qs = append(qs, q)
	}
	// The real list is updated_at DESC, not number order: return it scrambled
	// (deterministic permutation; 37 is coprime to 60).
	shuffled := make([]question, 60)
	for i := range qs {
		shuffled[(i*37+11)%60] = qs[i]
	}
	qs = shuffled

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
		if r.URL.Query().Get("source") == practiceSource {
			bump("list_practice")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": practiceQuestions(), "next_cursor": nil})
			return
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
	// ---- contract endpoints (docs: brain "practice-site automatic plan") -------
	// Switches: MOCK_GRADE / MOCK_LOOKUP / MOCK_PICK = off (plain 404, like a
	// route that is not deployed) | 501 | fail (500); changeable at run time with
	// POST /_mock/switch?name=grade|lookup|pick&v=…
	switches := map[string]string{"grade": os.Getenv("MOCK_GRADE"), "lookup": os.Getenv("MOCK_LOOKUP"), "pick": os.Getenv("MOCK_PICK")}
	if os.Getenv("MOCK_GRADE_FAIL") == "1" {
		switches["grade"] = "fail"
	}
	var swmu sync.Mutex
	// cerr answers like the real brain's contract endpoints: {"code","message","details"?}.
	cerr := func(w http.ResponseWriter, status int, code, msg string, details map[string]any, hdr map[string]string) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		m := map[string]any{"code": code, "message": msg}
		if details != nil {
			m["details"] = details
		}
		_ = json.NewEncoder(w).Encode(m)
	}
	// blocked answers true (and has written the reply) if the endpoint is switched off/failing.
	blocked := func(name string, w http.ResponseWriter, r *http.Request) bool {
		swmu.Lock()
		v := switches[name]
		swmu.Unlock()
		switch v {
		case "off": // the catch-all 404 of a route the brain does not have: not_found / "not found", no details
			cerr(w, 404, "not_found", "not found", nil, nil)
		case "501":
			cerr(w, 501, "not_implemented", "not implemented", nil, nil)
		case "fail":
			cerr(w, 500, "internal", "internal error", nil, nil)
		default:
			return false
		}
		return true
	}
	mux.HandleFunc("POST /_mock/switch", func(w http.ResponseWriter, r *http.Request) {
		n := r.URL.Query().Get("name")
		if _, ok := switches[n]; !ok {
			http.Error(w, "bad name", 400)
			return
		}
		swmu.Lock()
		switches[n] = r.URL.Query().Get("v")
		swmu.Unlock()
		_, _ = w.Write([]byte("ok\n"))
	})
	// qref index over every question the mock knows.
	known := map[string]question{}
	for _, q := range practiceQuestions() {
		known[q.QRef] = q
	}
	for _, q := range qs {
		known[q.QRef] = q
	}
	// aliases: an old qref that resolves to its canonical one (lookup/pick answer the canonical qref,
	// "missing" echoes the request string, grade echoes the string it was sent).
	aliases := map[string]string{"example.set:math:2-old": "example.set:math:2"}
	canonOf := func(q string) string {
		if c, ok := aliases[q]; ok {
			return c
		}
		return q
	}
	qrefOK := func(q string) bool { return q != "" && len(q) <= 300 && q == strings.ToLower(q) }
	var gmu sync.Mutex // guards revs, keys, writes, caps, attempt ids
	revs := map[string]int{}
	curRev := func(q string) int {
		if v, ok := revs[q]; ok {
			return v
		}
		return 1
	}
	mux.HandleFunc("POST /_mock/bump-rev", func(w http.ResponseWriter, r *http.Request) {
		gmu.Lock()
		defer gmu.Unlock()
		q := r.URL.Query().Get("qref")
		revs[q] = curRev(q) + 1
		_, _ = w.Write([]byte(strconv.Itoa(revs[q]) + "\n"))
	})

	// POST /v1/bank/questions/lookup: no answers, with rev / media / figure_status; unknown qrefs go into
	// missing[] (lookup never answers 404 once deployed). No query string, lower-case qrefs only.
	mux.HandleFunc("POST /v1/bank/questions/lookup", func(w http.ResponseWriter, r *http.Request) {
		bump("lookup")
		if blocked("lookup", w, r) {
			return
		}
		if !bearer(r) {
			cerr(w, 401, "unauthorized", "unauthorized", nil, nil)
			return
		}
		var req struct {
			QRefs []string `json:"qrefs"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if r.URL.RawQuery != "" || dec.Decode(&req) != nil || len(req.QRefs) == 0 || len(req.QRefs) > 100 {
			bump("lookup_400")
			cerr(w, 400, "invalid_json", "invalid request", nil, nil)
			return
		}
		for _, q := range req.QRefs {
			if !qrefOK(q) {
				bump("lookup_400")
				cerr(w, 400, "validation_failed", "invalid qref", nil, nil)
				return
			}
		}
		type lq struct {
			question
			Rev int `json:"rev"`
		}
		items := []lq{}
		missing := []string{}
		returned := map[string]bool{}
		seenStr := map[string]bool{}
		gmu.Lock()
		for _, ref := range req.QRefs {
			if seenStr[ref] {
				continue // duplicate strings count once
			}
			seenStr[ref] = true
			c := canonOf(ref)
			q, ok := known[c]
			if !ok {
				missing = append(missing, ref)
				continue
			}
			if returned[c] {
				continue // two strings of one question: only the first position is returned
			}
			returned[c] = true
			q.Answer, q.Explanation, q.Solution = "", "", "" // lookup never carries answers
			items = append(items, lq{q, curRev(c)})
		}
		gmu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "missing": missing})
	})

	// GET /v1/brain/pick-practice?source=&n=&mode=due_first|wrong_only&brain=<MOCK_BRAIN_ID, default ruyi>
	mux.HandleFunc("GET /v1/brain/pick-practice", func(w http.ResponseWriter, r *http.Request) {
		bump("pick")
		if blocked("pick", w, r) {
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + gradeToken:
		case "Bearer " + token: // bank:read only: no write scope on the target brain
			bump("pick_with_bank_read_token")
			cerr(w, 403, "forbidden", "forbidden", nil, nil)
			return
		default:
			cerr(w, 401, "unauthorized", "unauthorized", nil, nil)
			return
		}
		qv := r.URL.Query()
		for k := range qv { // the only query parameters of this endpoint
			if k != "source" && k != "n" && k != "date" && k != "brain" && k != "mode" {
				bump("pick_400")
				cerr(w, 400, "validation_failed", "unknown query parameter", nil, nil)
				return
			}
		}
		mode, src := qv.Get("mode"), qv.Get("source")
		n, nerr := strconv.Atoi(qv.Get("n"))
		if (mode != "" && mode != "due_first" && mode != "wrong_only") || nerr != nil || n < 1 || n > 10 {
			bump("pick_400")
			cerr(w, 400, "validation_failed", "invalid query", nil, nil)
			return
		}
		if mode == "" {
			mode = "due_first"
		}
		if qv.Get("brain") != brainID { // an unknown brain / source is a real 404 with its own message
			cerr(w, 404, "not_found", "brain not found", nil, nil)
			return
		}
		if src == "ambiguous-source" {
			bump("pick_400")
			cerr(w, 400, "ambiguous_source", "ambiguous source", nil, nil)
			return
		}
		if src != practiceSource && src != source {
			cerr(w, 404, "not_found", "source not found", nil, nil)
			return
		}
		type pick struct {
			QRef    string   `json:"qref"`
			Number  string   `json:"number"`
			Origin  string   `json:"origin"`
			Reasons []string `json:"reasons"`
		}
		mk := func(ref, origin string, reasons ...string) pick {
			return pick{QRef: ref, Number: known[ref].Number, Origin: origin, Reasons: reasons}
		}
		var all []pick
		switch src {
		case practiceSource:
			if mode == "wrong_only" {
				all = []pick{mk("example.set:math:1", "wrong", "wrong_count:3"),
					mk("example.set:math:3", "wrong", "wrong_count:2", "deferred:pending_key"),
					mk("example.set:math:4", "wrong", "wrong_count:1", "deferred:unsupported_qtype")}
			} else {
				all = []pick{mk("example.set:math:2", "review", "due_on:2026-10-17", "wrong_count:2"),
					mk("example.set:math:1", "wrong", "wrong_count:3"),
					mk("example.set:math:5", "review", "due_on:2026-10-16", "wrong_count:1")}
			}
		case source:
			ids := []int{3, 17, 5, 9, 12, 21}
			origin, reason := "wrong", "wrong_count:2"
			if mode == "due_first" {
				ids = []int{2, 3, 6, 17, 8}
				origin, reason = "review", "due_on:2026-10-17"
			}
			for _, i := range ids {
				ref := fmt.Sprintf("math:%s:q%02d", source, i)
				if i == 17 {
					all = append(all, mk(ref, origin, reason, "deferred:figure_missing"))
				} else {
					all = append(all, mk(ref, origin, reason))
				}
			}
		}
		if len(all) > n {
			all = all[:n]
		}
		if all == nil {
			all = []pick{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": all})
	})

	// POST /v1/bank/grade (grade token, NOT the bank:read token).
	var lastGrade []byte
	type stored struct {
		answer     string // canonical JSON of the answer
		status     string
		correct    *bool
		attemptID  int64
		recorded   bool
		answeredAt time.Time
	}
	idem := map[string]stored{}  // ps:<client_attempt_id>:<qref>
	dayCount := map[string]int{} // <day>|<qref> -> writes
	keys := map[string]any{      // synthetic answer keys; they exist ONLY here
		"example.set:math:1": []any{"1", "2", "3"},
		"example.set:math:2": "B",
		"example.set:math:5": "B",
	}
	for i := 1; i <= 10; i++ {
		keys[fmt.Sprintf("math:%s:q%02d", source, i)] = "B"
	}
	mux.HandleFunc("POST /_mock/set-key", func(w http.ResponseWriter, r *http.Request) {
		var v any
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&v)
		gmu.Lock()
		keys[r.URL.Query().Get("qref")] = v
		gmu.Unlock()
		_, _ = w.Write([]byte("ok\n"))
	})
	gradeUnknown := map[string]bool{}
	mux.HandleFunc("POST /_mock/grade-unknown", func(w http.ResponseWriter, r *http.Request) {
		gmu.Lock()
		gradeUnknown[r.URL.Query().Get("qref")] = r.URL.Query().Get("v") != "off"
		gmu.Unlock()
		_, _ = w.Write([]byte("ok\n"))
	})
	var attemptSeq int64 = 1000
	decide := func(ref string, ans any, a string) (status string, correct *bool) {
		if strings.Contains(a, "~review~") {
			return "needs_review", nil
		}
		switch {
		case ref == "example.set:math:4", ref == fmt.Sprintf("math:%s:q17", source):
			return "unsupported", nil
		}
		if want, ok := keys[ref]; ok {
			wnt, _ := json.Marshal(want)
			c := a == string(wnt)
			return "graded", &c
		}
		return "pending_key", nil
	}
	mux.HandleFunc("POST /v1/bank/grade", func(w http.ResponseWriter, r *http.Request) {
		bump("grade")
		if blocked("grade", w, r) {
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+gradeToken {
			if bearer(r) {
				bump("grade_with_bank_read_token")
			}
			cerr(w, 403, "forbidden", "forbidden", nil, nil)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			Brain string `json:"brain"`
			CID   string `json:"client_attempt_id"`
			Items []struct {
				QRef        string          `json:"qref"`
				Answer      any             `json:"answer"`
				Rev         *int            `json:"rev"`
				AttemptKind string          `json:"attempt_kind"`
				AnsweredAt  *string         `json:"answered_at"`
				SessionRef  *string         `json:"session_ref"`
				Payload     json.RawMessage `json:"payload"`
			} `json:"items"`
		}
		// Strict, like the real brain: unknown body fields, any query other than ?brain= and
		// every out-of-contract value is a 400 and nothing is written.
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		for k := range r.URL.Query() {
			if k != "brain" {
				bump("grade_400")
				cerr(w, 400, "validation_failed", "unknown query parameter", nil, nil)
				return
			}
		}
		if err := dec.Decode(&req); err != nil || dec.More() {
			bump("grade_400")
			cerr(w, 400, "invalid_json", "invalid json", nil, nil)
			return
		}
		bad := func(msg string) { cerr(w, 400, "validation_failed", msg, nil, nil); bump("grade_400") }
		if req.Brain != brainID || !mockCIDRe.MatchString(req.CID) || len(req.Items) == 0 || len(req.Items) > 50 {
			bad("invalid request")
			return
		}
		seen := map[string]bool{}
		canon := make([]string, len(req.Items))
		cq := make([]string, len(req.Items))            // canonical qref
		answeredAt := make([]time.Time, len(req.Items)) // zero when not sent
		for i, it := range req.Items {
			if !qrefOK(it.QRef) || it.Answer == nil || (it.AttemptKind != "" && it.AttemptKind != "correction" && it.AttemptKind != "variant") {
				bad("invalid item")
				return
			}
			cq[i] = canonOf(it.QRef)
			if seen[cq[i]] || len("ps:"+req.CID+":"+cq[i]) > 198 {
				bad("duplicate qref or key too long")
				return
			}
			seen[cq[i]] = true
			switch a := it.Answer.(type) {
			case string:
				if len([]rune(a)) > maxAnswerRunes {
					bad("answer too long")
					return
				}
			case []any:
				if len(a) > 10 {
					bad("too many answers")
					return
				}
				for _, e := range a {
					if es, ok := e.(string); !ok || len([]rune(es)) > maxAnswerRunes {
						bad("invalid answer")
						return
					}
				}
			default:
				bad("invalid answer")
				return
			}
			if it.Rev != nil && *it.Rev < 1 {
				bad("invalid rev")
				return
			}
			if it.SessionRef != nil && (len(*it.SessionRef) < 1 || len(*it.SessionRef) > 200) {
				bad("invalid session_ref")
				return
			}
			if len(it.Payload) > 4096 {
				bad("payload too large")
				return
			}
			if it.AnsweredAt != nil {
				t, err := time.Parse(time.RFC3339, *it.AnsweredAt)
				if err != nil || t.Before(now().Add(-7*24*time.Hour)) || t.After(now().Add(time.Minute)) {
					bad("invalid answered_at")
					return
				}
				answeredAt[i] = t
			}
			c, _ := json.Marshal(it.Answer)
			canon[i] = string(c)
		}
		gmu.Lock()
		defer gmu.Unlock()
		lastGrade = body
		// 1. any unknown qref: 404 for the whole batch (details.qrefs lists them), nothing written
		var unknown []string
		for _, it := range req.Items {
			if _, ok := known[canonOf(it.QRef)]; !ok || gradeUnknown[canonOf(it.QRef)] {
				unknown = append(unknown, it.QRef)
			}
		}
		if len(unknown) > 0 {
			cerr(w, 404, "not_found", "question not found", map[string]any{"qrefs": unknown}, nil)
			return
		}
		// 2. rev must be the current one (details never carry an item index)
		for i, it := range req.Items {
			if it.Rev != nil && *it.Rev != curRev(cq[i]) {
				cerr(w, 409, "rev_mismatch", "rev mismatch", map[string]any{"qref": cq[i], "rev": *it.Rev, "current_rev": curRev(cq[i])}, nil)
				return
			}
		}
		// 3. idempotency: same id+canonical qref with another answer/result/answered_at is a conflict.
		// attempt_kind, session_ref and payload do not take part; an absent answered_at is a replay.
		type plan struct {
			status string
			corr   *bool
			prev   *stored
		}
		plans := make([]plan, len(req.Items))
		for i := range req.Items {
			st, c := decide(cq[i], req.Items[i].Answer, canon[i])
			plans[i] = plan{status: st, corr: c}
			if prev, ok := idem["ps:"+req.CID+":"+cq[i]]; ok {
				p := prev
				plans[i].prev = &p
				atDiffers := !answeredAt[i].IsZero() && !prev.answeredAt.IsZero() && !answeredAt[i].Equal(prev.answeredAt)
				if prev.answer != canon[i] || prev.status != st || !sameCorrect(prev.correct, c) || atDiffers {
					cerr(w, 409, "idempotency_conflict", "idempotency conflict", nil, nil)
					return
				}
			}
		}
		// 4. daily cap: 3 writes per brain per question per Shanghai day (new writes only; unsupported
		// writes nothing and does not count; pending_key and needs_review count). The whole batch is refused.
		sh := time.FixedZone("CST", 8*3600)
		nowSh := now().In(sh)
		day := nowSh.Format("2006-01-02")
		var capped []string
		for i, it := range req.Items {
			if plans[i].prev == nil && plans[i].status != "unsupported" && dayCount[day+"|"+cq[i]] >= 3 {
				capped = append(capped, it.QRef)
			}
		}
		if len(capped) > 0 {
			next := time.Date(nowSh.Year(), nowSh.Month(), nowSh.Day()+1, 0, 0, 0, 0, sh)
			cerr(w, 429, "rate_limited", "daily grade limit reached", map[string]any{"qrefs": capped},
				map[string]string{"Retry-After": strconv.Itoa(int(next.Sub(nowSh).Seconds()) + 1)})
			return
		}
		// 5. write
		type outItem struct {
			QRef      string `json:"qref"`
			Status    string `json:"status"`
			Correct   *bool  `json:"correct,omitempty"`
			AttemptID int64  `json:"attempt_id,omitempty"`
			Recorded  bool   `json:"recorded"`
			Duplicate bool   `json:"duplicate"`
		}
		out := make([]outItem, len(req.Items))
		wrote := false
		for i, it := range req.Items {
			p := plans[i]
			o := outItem{QRef: it.QRef, Status: p.status, Correct: p.corr} // echoes the string it was sent
			switch {
			case p.prev != nil:
				o.AttemptID, o.Recorded, o.Duplicate = p.prev.attemptID, p.prev.recorded, true
				bump("grade_dup_item")
			case p.status == "unsupported":
				idem["ps:"+req.CID+":"+cq[i]] = stored{answer: canon[i], status: p.status, answeredAt: answeredAt[i]}
			default:
				attemptSeq++
				o.AttemptID, o.Recorded = attemptSeq, true
				dayCount[day+"|"+cq[i]]++
				idem["ps:"+req.CID+":"+cq[i]] = stored{canon[i], p.status, p.corr, attemptSeq, true, answeredAt[i]}
				bump("grade_new_item")
				wrote = true
			}
			out[i] = o
		}
		bump("grade_new")
		w.Header().Set("Content-Type", "application/json")
		if wrote {
			w.WriteHeader(201)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"client_attempt_id": req.CID, "items": out})
	})
	mux.HandleFunc("GET /_mock/last-grade", func(w http.ResponseWriter, r *http.Request) {
		gmu.Lock()
		defer gmu.Unlock()
		_, _ = w.Write(lastGrade)
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
		// sha of the first image of the question at sorted position N: /_mock/shas?n=7
		n := fmt.Sprintf("math:%s:q%02d", source, atoi(r.URL.Query().Get("n")))
		for _, q := range qs {
			if q.QRef == n && len(q.Media) > 0 {
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

func sameCorrect(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
