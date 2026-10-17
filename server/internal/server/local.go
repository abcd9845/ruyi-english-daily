package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/abcd9845/ruyi-english-daily/server/internal/practice"
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

// Local test sets (sets.Set.Local) never reach the brain: the question is
// inline in the sets file, "检查" and the verdict on submit are judged here from
// the question's server-side answer field, which is never put in any response.

// noteTestSet is the archive/response brain_note of a local-set submission.
const noteTestSet = "test_set"

// shapeOfLocal is shapeOf for an inline question.
func shapeOfLocal(q *sets.Question) qshape {
	sh := qshape{present: true}
	if len(q.Options) > 0 {
		sh.keys = map[string]bool{}
		for _, o := range q.Options {
			sh.keys[o.Key] = true
		}
		sh.kind = "single"
		return sh
	}
	sh.kind, sh.blanks = "fill", practice.CountBlanks(q.Stem)
	return sh
}

// judgeLocal compares a parsed answer (string for single_choice, []string for
// fill_blank, as parseAnswer returns them) with the question's answer.
// Blanks: surrounding spaces and letter case do not matter.
func judgeLocal(q *sets.Question, ans any) bool {
	switch a := ans.(type) {
	case string:
		return len(q.Answer) == 1 && a == q.Answer[0]
	case []string:
		if len(a) != len(q.Answer) {
			return false
		}
		for i := range a {
			if !strings.EqualFold(strings.TrimSpace(a[i]), strings.TrimSpace(q.Answer[i])) {
				return false
			}
		}
		return true
	}
	return false
}

type checkReq struct {
	Answers []struct {
		QRef   string          `json:"qref"`
		Answer json.RawMessage `json:"answer"`
	} `json:"answers"`
}

type checkResult struct {
	QRef    string `json:"qref"`
	Status  string `json:"status"` // graded | unanswered
	Correct *bool  `json:"correct"`
}

// checkLocal answers POST /api/practice/{id}/check for a local test set: the
// page's 「检查」 button. It judges on the server, archives nothing, sends no
// notification and never contacts the brain. Real sets answer 404.
func (s *Server) checkLocal(w http.ResponseWriter, r *http.Request, id string) {
	if mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])); mt != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return
	}
	set, ok := s.getSet(id)
	if !ok || !set.Local {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	var req checkReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGradeBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "提交内容格式不对")
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "bad_request", "提交内容格式不对")
		return
	}
	if len(req.Answers) == 0 || len(req.Answers) > len(set.Questions) {
		writeErr(w, http.StatusBadRequest, "bad_answers", "提交的题数不对")
		return
	}
	got := map[string]checkResult{}
	for _, a := range req.Answers {
		q, ok := set.Question(a.QRef)
		if !ok {
			writeErr(w, http.StatusBadRequest, "qref_not_in_set", "有题目不属于这套练习")
			return
		}
		if _, dup := got[a.QRef]; dup {
			writeErr(w, http.StatusBadRequest, "duplicate_qref", "同一题提交了两次")
			return
		}
		ans, empty, ok := parseAnswer(shapeOfLocal(q), a.Answer)
		if !ok {
			writeErr(w, http.StatusBadRequest, "bad_answer_shape", "作答格式和题型不匹配")
			return
		}
		if empty {
			got[a.QRef] = checkResult{QRef: a.QRef, Status: statusUnanswer}
			continue
		}
		c := judgeLocal(q, ans)
		got[a.QRef] = checkResult{QRef: a.QRef, Status: "graded", Correct: &c}
	}
	out := make([]checkResult, 0, len(set.Questions))
	for _, q := range set.Questions {
		if g, ok := got[q.QRef]; ok {
			out = append(out, g)
		} else {
			out = append(out, checkResult{QRef: q.QRef, Status: statusUnanswer})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"set_id": set.ID, "test": true, "results": out})
}
