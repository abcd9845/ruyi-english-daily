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
	// Rev is the question revision the brain served; the page sends it back with
	// the answer so the brain can refuse a stale version (409 rev_mismatch).
	Rev int `json:"rev,omitempty"`
	// Origin (review|wrong) and RefPending are only set on brain-picked sets.
	// RefPending is the brain's deferred:<code> reason: the page shows
	// "参考答案待核，不自动判" for such a question.
	Origin     string `json:"origin,omitempty"`
	RefPending string `json:"ref_pending,omitempty"`
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
	meta := s.dyn.metaOf(set.ID)
	for _, ref := range set.QRefs {
		q, ok := byRef[ref]
		if !ok {
			continue
		}
		it := practiceItem{
			Ordinal: len(items) + 1, QRef: q.QRef, QType: q.QType, StemMD: q.Stem,
			Blanks: practice.CountBlanks(q.Stem), FigureStatus: q.Figure(), Media: []mediaOut{}, Rev: q.RevInt(),
		}
		if m, ok := meta[ref]; ok {
			it.Origin, it.RefPending = m.Origin, m.Deferred
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

// autoNew creates a temporary practice set ("auto-xxxxxxxx") from an allowed
// source. Which questions are picked is the brain adapter's job
// (brain.Bank.SelectForPractice):
//
//	mode omitted      legacy: the whole source, order=random|seq, optional seed (local, no brain data)
//	mode=wrong_only   the brain's pick-practice: unresolved wrong questions
//	mode=due_first    the brain's pick-practice: review-due questions first
//
// The old draft query flags wrong_only=1 / due_first=1 are still accepted as
// aliases of mode (when mode is absent). If the brain has no pick-practice
// endpoint yet the answer is 501 "大脑接口未就绪"; nothing is faked.
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
	opts := brain.SelectOpts{Order: "random", SkipDeferred: s.SkipDeferred}
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
	switch m := q.Get("mode"); m {
	case "", "random":
		opts.Mode = brain.ModeRandom
		wo, df := flag("wrong_only"), flag("due_first")
		switch {
		case wo && df:
			writeErr(w, http.StatusBadRequest, "bad_mode", "wrong_only 和 due_first 不能同时用")
			return
		case wo:
			opts.Mode = brain.ModeWrongOnly
		case df:
			opts.Mode = brain.ModeDueFirst
		}
	case brain.ModeDueFirst, brain.ModeWrongOnly:
		opts.Mode = m
	default:
		writeErr(w, http.StatusBadRequest, "bad_mode", "mode 只能是 due_first 或 wrong_only")
		return
	}

	picks, err := s.Bank.SelectForPractice(r.Context(), source, n, opts)
	if errors.Is(err, brain.ErrUnsupported) {
		code := "wrong_only_unavailable"
		if opts.Mode == brain.ModeDueFirst {
			code = "due_first_unavailable"
		}
		s.Log.Warn("pick-practice not available on the brain", "mode", opts.Mode)
		writeErr(w, http.StatusNotImplemented, code, "大脑接口未就绪（选题接口还没上线），暂时不能这样出题")
		return
	}
	if err != nil {
		s.brainErr(w, "select", err)
		return
	}
	if len(picks) == 0 {
		msg := "这个题库里没有可选的题"
		if opts.Mode != brain.ModeRandom {
			msg = "现在没有符合条件的题（没有未解决的错题或到期复习）"
		}
		writeErr(w, http.StatusNotFound, "no_questions", msg)
		return
	}
	refs := make([]string, 0, len(picks))
	meta := map[string]pickMeta{}
	deferred := 0
	for _, p := range picks {
		refs = append(refs, p.QRef)
		if p.Origin != "" || p.Deferred != "" {
			meta[p.QRef] = pickMeta{Origin: p.Origin, Deferred: p.Deferred}
		}
		if p.Deferred != "" {
			deferred++
		}
	}
	title := "练习（" + strconv.Itoa(len(refs)) + " 题）"
	switch opts.Mode {
	case brain.ModeWrongOnly:
		title = "错题订正（" + strconv.Itoa(len(refs)) + " 题）"
	case brain.ModeDueFirst:
		title = "复习（" + strconv.Itoa(len(refs)) + " 题）"
	}
	set, exp, ok := s.dyn.create(title, source, refs, meta)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "too_many_auto_sets", "临时套太多了，请稍后再试")
		return
	}
	out := map[string]any{
		"set_id": set.ID, "url": "/m/practice/?set=" + set.ID, "title": title,
		"requested": n, "count": len(refs), "truncated": len(refs) < n, "expires_at": exp.UTC().Format(time.RFC3339),
	}
	if opts.Mode != brain.ModeRandom {
		out["mode"] = opts.Mode
		out["deferred"] = deferred
	}
	var notes []string
	if len(refs) < n {
		notes = append(notes, "题库里只有 "+strconv.Itoa(len(refs))+" 题可选，已全部选入")
	}
	if deferred > 0 {
		notes = append(notes, strconv.Itoa(deferred)+" 题参考答案待核，不自动判")
	}
	if len(notes) > 0 {
		out["note"] = strings.Join(notes, "；")
	}
	writeJSON(w, http.StatusOK, out)
}
