package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
	"github.com/abcd9845/ruyi-english-daily/server/internal/practice"
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

type practiceOption struct {
	Key    string `json:"key"`
	TextMD string `json:"text_md"`
}

type practiceItem struct {
	Ordinal      int              `json:"ordinal"`
	QRef         string           `json:"qref"`
	QType        string           `json:"qtype"`
	StemMD       string           `json:"stem_md"`
	Options      []practiceOption `json:"options,omitempty"`
	Blanks       int              `json:"blanks,omitempty"`
	FigureStatus string           `json:"figure_status"`
	Media        []mediaOut       `json:"media"`
}

// practice serves one configured set: the whole source is read through the
// existing read-only brain client, then cut down to the set's qref whitelist,
// in the order the set file lists them. Only fields of brain.Question are ever
// decoded, so an answer/explanation the brain might send cannot be forwarded.
func (s *Server) practice(w http.ResponseWriter, r *http.Request, id string) {
	if !sets.ValidID(id) {
		writeErr(w, http.StatusBadRequest, "bad_set_id", "套编号不合法")
		return
	}
	set, ok := s.getSet(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	byRef, missingRefs, err := s.Bank.LookupByQrefs(r.Context(), set.Source, set.QRefs)
	if err != nil {
		s.brainErr(w, "practice", err)
		return
	}
	items := make([]practiceItem, 0, len(set.QRefs))
	missing := missingRefs
	for _, ref := range set.QRefs {
		q, ok := byRef[ref]
		if !ok {
			continue
		}
		it := practiceItem{
			Ordinal: len(items) + 1, QRef: q.QRef, QType: q.QType, StemMD: q.Stem,
			Blanks: practice.CountBlanks(q.Stem), FigureStatus: q.Figure(), Media: []mediaOut{},
		}
		if len(q.Options) > 0 && string(q.Options) != "null" {
			var opts []optionOut
			if json.Unmarshal(q.Options, &opts) == nil {
				for _, o := range opts {
					if o.Key != "" {
						it.Options = append(it.Options, practiceOption{Key: o.Key, TextMD: o.Text})
					}
				}
			} else {
				s.Log.Warn("options not in expected shape", "set", id, "ordinal", it.Ordinal)
			}
		}
		for _, m := range q.Media {
			if m.Slot == "explanation" {
				continue
			}
			it.Media = append(it.Media, mediaOut{
				URL:  "/api/media/" + set.Source + "/" + m.SHA256,
				Slot: m.Slot, OptionKey: m.OptionKey, Alt: m.Alt, Mime: m.Mime, Ordinal: m.Ordinal,
			})
		}
		items = append(items, it)
	}
	if len(missing) > 0 {
		// counts only: qrefs of a real paper are not written to logs
		s.Log.Warn("practice set has qrefs the brain did not return", "set", id, "missing", len(missing))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"set_id": set.ID, "title": set.Title, "items": items, "missing_qrefs": missing,
	})
}

// getSet finds a configured set, or a live generated one ("auto-…").
func (s *Server) getSet(id string) (*sets.Set, bool) {
	if set, ok := s.Sets.Get(id); ok {
		return set, true
	}
	if strings.HasPrefix(id, sets.AutoPrefix) {
		return s.dyn.get(id)
	}
	return nil, false
}

// autoNew creates a temporary practice set ("auto-xxxxxxxx") from an
// allowed source. Which questions are picked is the brain adapter's job
// (brain.Bank.SelectForPractice); wrong_only / due_first need brain data that
// does not exist yet and answer 501 instead of being faked here.
func (s *Server) autoNew(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	if len(source) > 128 || !s.srcRe.MatchString(source) || !s.auto[source] {
		writeErr(w, http.StatusBadRequest, "source_not_allowed", "这个题库不能自动出题")
		return
	}
	n, err := strconv.Atoi(q.Get("n"))
	if err != nil || n < 1 || n > 20 {
		writeErr(w, http.StatusBadRequest, "bad_n", "题数要在 1 到 20 之间")
		return
	}
	opts := brain.SelectOpts{Order: "random"}
	switch o := q.Get("order"); o {
	case "", "random":
	case "seq":
		opts.Order = "seq"
	default:
		writeErr(w, http.StatusBadRequest, "bad_order", "order 只能是 random 或 seq")
		return
	}
	if v := q.Get("seed"); v != "" {
		sd, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_seed", "seed 要是整数")
			return
		}
		opts.Seed = &sd
	}
	flag := func(k string) bool { v := q.Get(k); return v == "1" || v == "true" }
	opts.WrongOnly, opts.DueFirst = flag("wrong_only"), flag("due_first")

	refs, err := s.Bank.SelectForPractice(r.Context(), source, n, opts)
	if errors.Is(err, brain.ErrUnsupported) {
		code := "wrong_only_unavailable"
		if !opts.WrongOnly {
			code = "due_first_unavailable"
		}
		writeErr(w, http.StatusNotImplemented, code, "大脑还不支持这种选题")
		return
	}
	if err != nil {
		s.brainErr(w, "select", err)
		return
	}
	if len(refs) == 0 {
		writeErr(w, http.StatusNotFound, "no_questions", "这个题库里没有可选的题")
		return
	}
	title := "练习（" + strconv.Itoa(len(refs)) + " 题）"
	set, exp, ok := s.dyn.create(title, source, refs)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "too_many_auto_sets", "临时套太多了，请稍后再试")
		return
	}
	out := map[string]any{
		"set_id": set.ID, "url": "/m/practice/?set=" + set.ID, "title": title,
		"requested": n, "count": len(refs), "truncated": len(refs) < n, "expires_at": exp.UTC().Format(time.RFC3339),
	}
	if len(refs) < n {
		out["note"] = "题库里只有 " + strconv.Itoa(len(refs)) + " 题，已全部选入"
	}
	writeJSON(w, http.StatusOK, out)
}
