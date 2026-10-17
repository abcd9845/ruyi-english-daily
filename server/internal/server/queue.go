package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/attempts"
	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
)

// ---- S1: pending-answer queue (enqueue + status) ---------------------------------------
//
// Everything here is behind Deps.QueueEnabled (RUYI_QUEUE_ENABLED, default off).
// With the queue off no code of this file runs: no brain request, no file, no
// field in any response, so the site behaves exactly like the daily-limit branch.
//
// Flow: grade (unchanged) -> for the answers the brain could not judge
// (pending_key / needs_review / unsupported, with a qref it knows) enqueue them in
// ONE batch (3 s budget per call, never fails the submission) -> archive.
// A failed enqueue is retried in the background (and by the page, once, through
// POST /api/practice/attempts/{cid}/enqueue; no manual button). The page reads
// the state with GET /api/practice/attempts/{cid}/queue, a pure read.

const (
	queueViewTTL     = 5 * time.Second
	queueReadMax     = 20 // per-item reads per refresh when the batch read is not available
	queueBatchDown   = 5 * time.Minute
	queueCapsTTL     = 60 * time.Second
	queueSweepEvery  = 30 * time.Second
	queueTermCap     = 2000
	queueCacheCap    = 256
	enqueueBudget    = 7 * time.Second // two brain calls at most, 3 s each
	enqueueRateCount = 2               // POST .../enqueue per cid per minute
)

// ProbeQueueDir checks that the queue state directory can be created and written
// (called at startup when the queue is on: a queue without state must not start).
func ProbeQueueDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("RUYI_QUEUE_STATE_DIR: cannot create the directory")
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return errors.New("RUYI_QUEUE_STATE_DIR: directory is not writable")
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return nil
}

type qView struct {
	at    time.Time
	pend  map[string]brain.Pending // item id -> latest read
	stale bool
}

type queueRT struct {
	links *linkStore

	mu       sync.Mutex
	cid      map[string]*sync.Mutex // per-cid serialisation (view refresh / retries)
	views    map[string]*qView
	viewOrd  []string
	term     map[string]brain.Pending // terminal items (never read again)
	termOrd  []string
	batchOff time.Time
	caps     *brain.Capabilities
	capsAt   time.Time
	rl       map[string][]time.Time
}

func (s *Server) queueOn() bool { return s.q != nil }

func (s *Server) initQueue() {
	if !s.QueueEnabled || s.Queue == nil || s.QueueDir == "" {
		return
	}
	ls, err := openLinks(s.QueueDir, s.Log, s.now())
	if err != nil {
		s.Log.Error("queue is OFF: its state directory is not usable")
		return
	}
	s.q = &queueRT{links: ls, cid: map[string]*sync.Mutex{}, views: map[string]*qView{}, term: map[string]brain.Pending{}, rl: map[string][]time.Time{}}
}

// CloseQueue releases the queue's files.
func (s *Server) CloseQueue() {
	s.qcloseOnce.Do(func() {
		if s.q != nil {
			s.q.links.close()
		}
		if s.qlock != nil {
			s.qlock.Close()
		}
	})
}

func (q *queueRT) lockFor(cid string) *sync.Mutex {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := q.cid[cid]
	if m == nil {
		m = &sync.Mutex{}
		q.cid[cid] = m
	}
	return m
}

// ---- grade -> enqueue -----------------------------------------------------------------

type enqRes struct {
	Enq         string
	Reason      string
	Item        brain.EnqueuedItem
	RetryAfterS int
}

func (s *Server) enqueueCaps(ctx context.Context) *brain.Capabilities {
	q := s.q
	q.mu.Lock()
	c, at := q.caps, q.capsAt
	q.mu.Unlock()
	if c != nil && s.now().Sub(at) < queueCapsTTL {
		return c
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	nc, err := s.Queue.Capabilities(cctx)
	if err != nil {
		return c // the last known value (may be nil): only used for wording
	}
	q.mu.Lock()
	q.caps, q.capsAt = nc, s.now()
	q.mu.Unlock()
	return nc
}

func (s *Server) dropCaps() {
	s.q.mu.Lock()
	s.q.capsAt = time.Time{}
	s.q.mu.Unlock()
}

// doEnqueue sends the items in at most two batches: the first with everything; if the
// brain refuses it (nothing written) and names items that can never be queued, those
// are dropped and the rest is resubmitted once with session_total reduced to match.
// It never panics on odd replies: every item of the input gets a result.
//
// base is how many answers of this cid are already queued (they count in session_total).
// sticky is the session_total already sent for this cid (0 = none yet): the first request of
// a retry keeps it, because the brain refuses a different total for the same session_ref.
// The second return value is the total of the last request actually sent (0 = none).
func (s *Server) doEnqueue(ctx context.Context, cid string, items []brain.EnqueueItem, sticky, base int) (map[string]enqRes, int) {
	res := map[string]enqRes{}
	cur := items
	used := 0
	for round := 0; round < 2 && len(cur) > 0; round++ {
		total := base + len(cur)
		if round == 0 && sticky > 0 {
			total = sticky
		}
		used = total
		resp, err := s.Queue.Enqueue(ctx, brain.EnqueueRequest{ClientAttemptID: cid, SessionRef: cid, SessionTotal: total, Items: cur})
		if err == nil {
			got := map[string]brain.EnqueuedItem{}
			for _, it := range resp.Items {
				got[it.QRef] = it
			}
			for _, it := range cur {
				if g, ok := got[it.QRef]; ok {
					res[it.QRef] = enqRes{Enq: enqOK, Item: g}
				} else {
					res[it.QRef] = enqRes{Enq: enqFailed, Reason: "no_reply"}
				}
			}
			return res, used
		}
		drops, whole := s.classifyEnqErr(cur, err)
		if len(drops) == 0 {
			for _, it := range cur {
				res[it.QRef] = whole(it.QRef)
			}
			return res, used
		}
		var rest []brain.EnqueueItem
		for _, it := range cur {
			if d, ok := drops[it.QRef]; ok {
				res[it.QRef] = d
			} else {
				rest = append(rest, it)
			}
		}
		cur = rest
		if round == 1 { // a second refusal: whatever is left waits for the background retry
			for _, it := range cur {
				res[it.QRef] = enqRes{Enq: enqFailed, Reason: "batch_refused"}
			}
		}
	}
	return res, used
}

// classifyEnqErr turns a failed batch into (per-item permanent drops, a function
// giving the result for every item otherwise). drops non-empty = resubmit the rest.
func (s *Server) classifyEnqErr(cur []brain.EnqueueItem, err error) (map[string]enqRes, func(string) enqRes) {
	var qe *brain.QueueError
	var be *brain.Error
	errors.As(err, &qe)
	errors.As(err, &be)
	desc := "unknown"
	if be != nil {
		desc = be.Error() // fixed text + status + upstream code only
	}
	reason := ""
	retry := 0
	if qe != nil {
		reason, retry = qe.Reason, qe.RetryAfterS
		if len(qe.Items) > 0 {
			drops := map[string]enqRes{}
			byLower := map[string]string{}
			for _, it := range cur {
				byLower[strings.ToLower(it.QRef)] = it.QRef
			}
			for _, r := range qe.Items {
				if o, ok := byLower[r.QRef]; ok && r.Permanent() {
					drops[o] = enqRes{Enq: enqNotQueued, Reason: r.Why()}
				}
			}
			if len(drops) > 0 {
				for _, d := range drops {
					if d.Reason == brain.ReasonKindNotEnabled {
						s.dropCaps() // the brain's switches changed: re-read capabilities next time
						break
					}
				}
				s.Log.Info("queue enqueue: batch refused, resubmitting without the refused items", "refused", len(drops), "of", len(cur), "code", reason)
				return drops, nil
			}
		}
	}
	whole := func(enq, why string) func(string) enqRes {
		return func(string) enqRes { return enqRes{Enq: enq, Reason: why, RetryAfterS: retry} }
	}
	switch {
	case errors.Is(err, brain.ErrGradeDisabled):
		return nil, whole(enqNotQueued, "not_configured")
	case brain.NotReady(err):
		s.Log.Warn("queue enqueue: the brain has no pending-answers endpoint (yet)")
		return nil, whole(enqNotQueued, brain.ReasonNotReady)
	case be != nil && be.Kind == brain.KindBadRequest:
		if reason == brain.ReasonSessionTotalMismatch {
			s.Log.Warn("queue enqueue: the brain refused a changed session_total (site bug path)")
		}
		return nil, whole(enqNotQueued, "invalid")
	case be != nil && be.Kind == brain.KindAuth:
		s.Log.Warn("queue enqueue: the brain rejected the write token", "err", desc)
		return nil, whole(enqFailed, "auth")
	case be != nil && be.Kind == brain.KindConflict:
		s.Log.Warn("queue enqueue: conflict (site bug path)", "err", desc)
		return nil, whole(enqNotQueued, "conflict")
	case be != nil && be.Kind == brain.KindRateLimited && reason == brain.ReasonDailyCap:
		return nil, whole(enqNotQueued, brain.ReasonDailyCap)
	case be != nil && be.Kind == brain.KindRateLimited:
		return nil, whole(enqFailed, nz(reason, "rate_limited"))
	case be != nil && be.Kind == brain.KindRejected:
		s.Log.Warn("queue enqueue: request rejected as malformed (site bug path)", "err", desc)
		return nil, whole(enqNotQueued, "invalid")
	case qe != nil && qe.Base.Status == http.StatusUnprocessableEntity && len(cur) == 1 && (reason == brain.ReasonKindNotEnabled ||
		reason == brain.ReasonAnswerRequired || reason == brain.ReasonImagesNotEnabled || reason == brain.ReasonQuestionNotFound || reason == brain.ReasonRevMismatch):
		if reason == brain.ReasonKindNotEnabled {
			s.dropCaps()
		}
		return nil, whole(enqNotQueued, reason)
	}
	s.Log.Warn("queue enqueue failed; will retry", "err", desc)
	return nil, whole(enqFailed, nz(reason, "unavailable"))
}

func nz(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// enqBackoff is the wait before the n-th automatic retry: 1, 2, 4 minutes, then 5 minutes.
func enqBackoff(n int) time.Duration {
	return min(time.Minute<<min(max(n-1, 0), 10), enqBackoffCap)
}

// enqWindow is how long an answer is retried: until the brain's processing deadline.
func enqWindow(li linkItem) time.Duration {
	if li.Kind == brain.KindAbsolute {
		return enqWindowAbsolute
	}
	return enqWindowJudged
}

// applyEnq folds results into the link items. A failed automatic attempt counts for the
// backoff; a failed page-forced attempt (forced) costs nothing: the schedule stays as it
// was. Whatever the way, an answer still failing at its deadline becomes retry_exhausted.
func (s *Server) applyEnq(l *link, res map[string]enqRes, forced bool) {
	now := s.now()
	for i := range l.Items {
		li := &l.Items[i]
		r, ok := res[li.QRef]
		if !ok {
			continue
		}
		switch r.Enq {
		case enqOK:
			li.Enq, li.Reason, li.NextTry = enqOK, "", time.Time{}
			li.ItemID, li.Kind, li.DeadlineAt, li.Created = r.Item.ItemID, r.Item.Kind, r.Item.DeadlineAt, r.Item.Created
		case enqNotQueued:
			li.Enq, li.Reason, li.NextTry = enqNotQueued, r.Reason, time.Time{}
		default:
			li.Enq, li.Reason = enqFailed, r.Reason
			if !forced || li.NextTry.IsZero() {
				li.Tries++
				d := enqBackoff(li.Tries)
				if ra := time.Duration(r.RetryAfterS) * time.Second; ra > d {
					d = ra
				}
				li.NextTry = now.Add(d)
			}
			if now.Sub(l.SubmittedAt) > enqWindow(*li) {
				li.Enq, li.Reason, li.NextTry = enqNotQueued, "retry_exhausted", time.Time{}
			}
		}
	}
}

type queueOutItem struct {
	QRef       string `json:"qref"`
	Enq        string `json:"enq"`
	Reason     string `json:"reason,omitempty"`
	DeadlineAt string `json:"deadline_at,omitempty"`
}

// queueOut is the additive "queue" object of the grade response (absent when
// nothing was eligible, and always absent with the queue off).
type queueOut struct {
	Enqueue string         `json:"enqueue"` // ok | partial | failed
	Items   []queueOutItem `json:"items"`
}

func queueOutOf(l *link) *queueOut {
	if l == nil || len(l.Items) == 0 {
		return nil
	}
	qo := &queueOut{Items: []queueOutItem{}}
	ok, bad := 0, 0
	for _, li := range l.Items {
		o := queueOutItem{QRef: li.QRef, Enq: li.Enq, Reason: li.Reason}
		if !li.DeadlineAt.IsZero() {
			o.DeadlineAt = li.DeadlineAt.In(shanghai).Format(time.RFC3339)
		}
		qo.Items = append(qo.Items, o)
		if li.Enq == enqOK {
			ok++
		} else {
			bad++
		}
	}
	switch {
	case bad == 0:
		qo.Enqueue = "ok"
	case ok == 0:
		qo.Enqueue = "failed"
	default:
		qo.Enqueue = "partial"
	}
	return qo
}

// gradeHint is the brain's additive static hint for one answer (契约 v1.0).
type gradeHint struct {
	known     bool // the brain sent queueable
	queueable bool
	reason    string
}

// queueAfterGrade enqueues what grade could not judge. items are exactly what was
// sent to grade. It rewrites rec.Items / out.Results for an unsupported answer
// that the enqueue WROTE (it is now in the brain), stores the link and returns the
// grade response's queue object plus the daily_remaining anchors for the quota estimate.
func (s *Server) queueAfterGrade(ctx context.Context, cid, setID, setTitle string, rec *attempts.Record, out *gradeOut,
	items []brain.GradeItem, hints map[string]gradeHint, answeredAt time.Time) (*queueOut, map[string]int) {
	sent := map[string]brain.GradeItem{}
	for _, it := range items {
		sent[it.QRef] = it
	}
	l := &link{CID: cid, SetID: setID, SetTitle: setTitle, SubmittedAt: rec.SubmittedAt, AnsweredAt: answeredAt}
	var send []brain.EnqueueItem
	for _, res := range out.Results {
		gi, ok := sent[res.QRef]
		if !ok {
			continue
		}
		eligible := res.Status == brain.StatusUnsupport || ((res.Status == brain.StatusPendingKey || res.Status == brain.StatusNeedsReview) && res.Recorded)
		if !eligible {
			continue
		}
		li := linkItem{QRef: res.QRef, Status: res.Status, Rev: gi.Rev, AttemptKind: gi.AttemptKind}
		if h := hints[res.QRef]; h.known && !h.queueable { // the brain says it can never be queued
			if h.reason == brain.ReasonNotNeeded {
				continue
			}
			li.Enq, li.Reason = enqNotQueued, nz(h.reason, "not_queueable")
			l.Items = append(l.Items, li)
			continue
		}
		l.Items = append(l.Items, li)
		send = append(send, brain.EnqueueItem{QRef: res.QRef, Answer: gi.Answer, Rev: gi.Rev, AttemptKind: gi.AttemptKind, AnsweredAt: gi.AnsweredAt})
	}
	if len(l.Items) == 0 {
		return nil, nil
	}
	anchors := map[string]int{}
	if len(send) > 0 {
		ectx, cancel := context.WithTimeout(ctx, enqueueBudget)
		res, used := s.doEnqueue(ectx, cid, send, 0, 0)
		cancel()
		if used > 0 {
			l.SessionTotal = used
		}
		s.applyEnq(l, res, false)
		for qref, r := range res {
			if r.Enq != enqOK {
				continue
			}
			if r.Item.DailyRemaining >= 0 {
				anchors[qref] = r.Item.DailyRemaining
			}
			// an unsupported answer written by the enqueue is now in the brain: say so (and it counts)
			if r.Item.AttemptID > 0 && sent[qref].QRef != "" && statusOf(out, qref) == brain.StatusUnsupport {
				markWritten(rec, out, qref, r.Item.AttemptID)
			}
		}
	}
	for _, li := range l.Items {
		for i := range rec.Items {
			if rec.Items[i].QRef != li.QRef {
				continue
			}
			rec.Items[i].Queue = &attempts.ItemQueue{Enq: li.Enq, ItemID: li.ItemID, Created: li.Created, DeadlineAt: li.DeadlineAt, Reason: li.Reason}
			if li.Enq == enqOK {
				rec.Items[i].Queue.State = brain.StateQueued
			}
		}
	}
	if err := s.q.links.put(l, s.now()); err != nil {
		s.Log.Warn("queue links not saved", "err", err)
	}
	return queueOutOf(l), anchors
}

func statusOf(out *gradeOut, qref string) string {
	for _, r := range out.Results {
		if r.QRef == qref {
			return r.Status
		}
	}
	return ""
}

// markWritten flips an unsupported answer that the enqueue wrote into "recorded".
func markWritten(rec *attempts.Record, out *gradeOut, qref string, attemptID int64) {
	for i := range rec.Items {
		if rec.Items[i].QRef == qref && !rec.Items[i].Recorded {
			rec.Items[i].Recorded, rec.Items[i].AttemptID = true, attemptID
		}
	}
	for i := range out.Results {
		if out.Results[i].QRef == qref && !out.Results[i].Recorded {
			out.Results[i].Recorded, out.Results[i].AttemptID = true, attemptID
			out.Summary.Recorded++
		}
	}
}

func asBrainErr(err error) *brain.Error {
	var be *brain.Error
	if errors.As(err, &be) {
		return be
	}
	return nil
}
