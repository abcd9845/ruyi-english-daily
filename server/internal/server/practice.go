package server

import (
	"encoding/json"
	"net/http"

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
	set, ok := s.Sets.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	all, err := s.Brain.Source(r.Context(), set.Source, false)
	if err != nil {
		s.brainErr(w, "practice", err)
		return
	}
	byRef := make(map[string]brain.Question, len(all))
	for _, q := range all {
		if _, dup := byRef[q.QRef]; !dup {
			byRef[q.QRef] = q
		}
	}
	items := make([]practiceItem, 0, len(set.QRefs))
	missing := []string{}
	for _, ref := range set.QRefs {
		q, ok := byRef[ref]
		if !ok {
			missing = append(missing, ref)
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
