package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/sets"
)

// ---- "how many of today's 3 submissions of this question are used" --------------
//
// The brain's rule (brain/grade.go): the same question may be WRITTEN at most
// dailyLimit times per brain per Asia/Shanghai day; pending_key / needs_review
// count, a replay (duplicate) and unsupported do not; a 429 refuses the whole
// batch. The brain's 429 carries NO "remaining" field and this site has no
// read endpoint for the count, so the only source is this site's own archive:
// every answer of an archived attempt that the brain itself confirmed as
// recorded (and not as a duplicate) on that Shanghai day. It is therefore an
// ESTIMATE ("按本站提交记录估算"): it cannot see writes made by anything else,
// nor an answer the brain wrote but whose reply never reached us.

// dailyLimit is the brain's per-question per-day write limit (a constant of
// the brain's contract, not configurable here).
const dailyLimit = 3

type quotaTally struct {
	mu  sync.Mutex
	day map[string]map[string]int // shanghai day -> qref -> counted writes
}

func newQuotaTally(st *attempts.Store) *quotaTally {
	q := &quotaTally{day: map[string]map[string]int{}}
	if st != nil {
		for _, r := range st.Load(20000) {
			q.add(r)
		}
	}
	return q
}

func shanghaiDay(t time.Time) string { return t.In(shanghai).Format("2006-01-02") }

// add counts one archived attempt. Local test sets, v1 archive lines (their
// "recorded" was derived, not confirmed per item), duplicates and unrecorded
// items are never counted.
func (q *quotaTally) add(r *attempts.Record) {
	if r == nil || r.Test || r.V < 2 {
		return
	}
	d := shanghaiDay(r.SubmittedAt)
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range r.Items {
		if !it.Recorded || it.Duplicate {
			continue
		}
		m := q.day[d]
		if m == nil {
			m = map[string]int{}
			q.day[d] = m
		}
		m[it.QRef]++
	}
	for k := range q.day { // keep a few days only
		if k < shanghaiDay(r.SubmittedAt.AddDate(0, 0, -3)) {
			delete(q.day, k)
		}
	}
}

func (q *quotaTally) used(day string, qrefs []string) map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[string]int{}
	for _, ref := range qrefs {
		out[ref] = q.day[day][ref]
	}
	return out
}

// quota answers GET /api/practice/<set>/quota: for each question of the set,
// how many of today's submissions this site recorded in the brain.
func (s *Server) quota(w http.ResponseWriter, r *http.Request, id string) {
	if !sets.ValidID(id) {
		writeErr(w, http.StatusBadRequest, "bad_set_id", "套编号不合法")
		return
	}
	set, ok := s.getSet(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	day := shanghaiDay(s.now())
	body := map[string]any{"set_id": set.ID, "day": day, "limit": dailyLimit, "basis": "site_archive", "local": set.Local}
	if set.Local { // a local test set never goes to the brain: no limit applies
		body["used"] = map[string]int{}
	} else {
		body["used"] = s.quotaT.used(day, set.QRefs)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}
