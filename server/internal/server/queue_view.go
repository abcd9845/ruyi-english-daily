package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
)

// ---- background retry of enqueues ------------------------------------------------------

// answerForBrain turns an archived answer back into the shape the brain takes
// (a JSON round trip makes []string into []any).
func answerForBrain(a any) any {
	if arr, ok := a.([]any); ok {
		out := make([]string, 0, len(arr))
		for _, x := range arr {
			s, _ := x.(string)
			out = append(out, s)
		}
		return out
	}
	return a
}

// retryEnqueue re-sends the answers of one submission whose enqueue failed
// transiently. force=true ignores next_try (used by the page's single automatic
// request). The answers come from the archive record (same text as grade got);
// answered_at/rev/kind come from the link, so the shared key ps:<cid>:<qref> converges.
func (s *Server) retryEnqueue(ctx context.Context, cid string, force bool) bool {
	if !s.queueOn() {
		return false
	}
	m := s.q.lockFor(cid)
	m.Lock()
	defer m.Unlock()
	l := s.q.links.get(cid)
	if l == nil {
		return false
	}
	now := s.now()
	var due []int
	for i, li := range l.Items {
		if li.Enq == enqFailed && (force || !li.NextTry.After(now)) {
			due = append(due, i)
		}
	}
	if len(due) == 0 {
		return false
	}
	rec := s.idem.record(cid)
	res := map[string]enqRes{}
	var send []brain.EnqueueItem
	for _, i := range due {
		li := l.Items[i]
		if now.Sub(l.SubmittedAt) > enqWindow(li) {
			res[li.QRef] = enqRes{Enq: enqNotQueued, Reason: "retry_exhausted"}
			continue
		}
		var ans any
		if rec != nil {
			for _, it := range rec.Items {
				if it.QRef == li.QRef {
					ans = answerForBrain(it.Answer)
				}
			}
		}
		if ans == nil {
			res[li.QRef] = enqRes{Enq: enqNotQueued, Reason: "archive_unavailable"}
			continue
		}
		send = append(send, brain.EnqueueItem{QRef: li.QRef, Answer: ans, Rev: li.Rev, AttemptKind: li.AttemptKind, AnsweredAt: l.AnsweredAt, Images: li.Images})
	}
	if len(send) > 0 {
		ectx, cancel := context.WithTimeout(ctx, enqueueBudget)
		// the session_total of this cid never changes between requests: keep the one already
		// sent (an older link without it: answers already queued + the ones sent now)
		queued := 0
		for _, it := range l.Items {
			if it.Enq == enqOK {
				queued++
			}
		}
		r2, used := s.doEnqueue(ectx, cid, send, l.SessionTotal, queued)
		for k, v := range r2 {
			res[k] = v
		}
		if used > 0 {
			l.SessionTotal = used
		}
		cancel()
	}
	status := map[string]string{}
	for _, li := range l.Items {
		status[li.QRef] = li.Status
	}
	s.applyEnq(l, res, force)
	day := shanghaiDay(now)
	for qref, r := range res {
		if r.Enq != enqOK {
			continue
		}
		if status[qref] == brain.StatusUnsupport && r.Item.AttemptID > 0 && r.Item.NewAttemptWritten {
			s.quotaT.addOne(day, qref) // the retry WROTE this answer: one more of today's 3
		}
		if r.Item.DailyRemaining >= 0 {
			s.quotaT.anchor(day, qref, r.Item.DailyRemaining)
		}
	}
	if err := s.q.links.put(l, now); err != nil {
		s.Log.Warn("queue links not saved", "err", err)
	}
	s.q.mu.Lock()
	delete(s.q.views, cid)
	s.q.mu.Unlock()
	return true
}

// queueSweep retries due enqueues until the server drains.
func (s *Server) queueSweep(every time.Duration) {
	defer s.nwg.Done()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.nstop:
			return
		case <-t.C:
			s.SweepOnce(context.Background())
		}
	}
}

// SweepOnce runs one retry pass over every submission with a due failed enqueue.
func (s *Server) SweepOnce(ctx context.Context) {
	if !s.queueOn() {
		return
	}
	for _, l := range s.q.links.all() {
		for _, li := range l.Items {
			if li.Enq == enqFailed {
				s.retryEnqueue(ctx, l.CID, false)
				break
			}
		}
	}
}

// ---- status view --------------------------------------------------------------------------

type qResultOut struct {
	ProposedAnswer string `json:"proposed_answer,omitempty"`
	Rationale      string `json:"rationale,omitempty"`
}

type qItemOut struct {
	QRef           string      `json:"qref"`
	UI             string      `json:"ui_state"`
	Label          string      `json:"label"`
	State          string      `json:"state,omitempty"`
	Kind           string      `json:"kind,omitempty"`
	Hours          int         `json:"hours,omitempty"`
	DeadlineAt     string      `json:"deadline_at,omitempty"`
	NeedsManual    bool        `json:"needs_manual,omitempty"`
	CompareOutcome string      `json:"compare_outcome,omitempty"`
	CompareText    string      `json:"compare_text,omitempty"`
	ReviewVerdict  string      `json:"review_verdict,omitempty"`
	Result         *qResultOut `json:"result,omitempty"`
	Reason         string      `json:"reason,omitempty"`
	// ReanalyzeEnabled is always false in S1 (no re-analyze button exists).
	ReanalyzeEnabled bool `json:"reanalyze_enabled"`
}

type qSummary struct {
	Total      int  `json:"total"`
	Waiting    int  `json:"waiting"`
	AIWorking  int  `json:"ai_working"`
	FirstPass  int  `json:"ai_first_pass"`
	Reviewed   int  `json:"reviewed"`
	Manual     int  `json:"manual"`
	Purged     int  `json:"purged"`
	NotQueued  int  `json:"not_queued"`
	Retrying   int  `json:"enqueue_retry"`
	Unknown    int  `json:"unknown"`
	AllDone    bool `json:"all_processed"`
	AnyPending bool `json:"any_pending"`
}

type queueViewOut struct {
	CID       string     `json:"cid"`
	SetID     string     `json:"set_id"`
	Items     []qItemOut `json:"items"`
	Summary   qSummary   `json:"summary"`
	Stale     bool       `json:"stale"`
	UpdatedAt string     `json:"updated_at"`
}

// UI states (page vocabulary).
const (
	uiRetry     = "enqueue_retry"
	uiNotQueued = "not_queued"
	uiWaiting   = "waiting"
	uiWorking   = "ai_working"
	uiFirstPass = "ai_first_pass"
	uiReviewed  = "reviewed"
	uiFailed    = "failed"
	uiExpired   = "expired"
	uiPurged    = "purged"
	uiOverdue   = "overdue_hint"
	uiUnknown   = "unknown"
)

var notQueuedText = map[string]string{
	brain.ReasonDailyCap:             "今天这题的次数已用完",
	brain.ReasonKindNotEnabled:       "这类题暂时还不能排队",
	brain.ReasonImagesNotEnabled:     "带图作答暂时还不能排队",
	brain.ReasonAnswerRequired:       "没有作答内容",
	brain.ReasonNotReady:             "排队功能还没开通",
	brain.ReasonQuestionNotFound:     "题目在大脑里找不到了",
	brain.ReasonRevMismatch:          "题目已更新",
	"retry_exhausted":                "多次排队都没成功",
	brain.ReasonSessionTotalMismatch: "排队信息对不上",
	"archive_unavailable":            "存档里找不到这次作答",
}

func hoursOf(kind string, deadline, submitted time.Time, caps *brain.Capabilities) int {
	if !deadline.IsZero() && deadline.After(submitted) {
		if h := int(math.Round(deadline.Sub(submitted).Hours())); h >= 1 && h <= 24*30 {
			return h
		}
	}
	if h := caps.Hours(kind); h > 0 {
		return h
	}
	if kind == brain.KindJudged {
		return 48
	}
	return 24
}

// itemView maps one answer (link + the latest brain read, nil = none) to what the page shows.
func (s *Server) itemView(li linkItem, p *brain.Pending, submitted time.Time, caps *brain.Capabilities) qItemOut {
	o := qItemOut{QRef: li.QRef}
	switch li.Enq {
	case enqNotQueued:
		o.UI, o.Reason = uiNotQueued, li.Reason
		switch li.Reason {
		case brain.ReasonDailyCap:
			o.Label = "今天这题已经交满 3 次，这次答案先存档，没有 AI 初评；明天再交一次才会排队。"
		case "retry_exhausted":
			o.Label = "暂时没能排队，答案已存档"
		default:
			why := notQueuedText[li.Reason]
			if why == "" {
				why = "暂时不能排队"
			}
			o.Label = "未排队：" + why + "，答案已存档，没有 AI 初评"
		}
		return o
	case enqFailed:
		o.UI, o.Reason = uiRetry, li.Reason
		o.Label = "已提交，正在排队（会自动重试）"
		return o
	}
	kind := li.Kind
	dl := li.DeadlineAt
	state := brain.StateQueued
	if p != nil {
		kind = nz(p.Kind, kind)
		if !p.DeadlineAt.IsZero() {
			dl = p.DeadlineAt
		}
		state = p.State
		o.State = p.State
		o.NeedsManual = p.NeedsManual
		o.CompareOutcome = p.CompareOutcome
		o.ReviewVerdict = p.ReviewVerdict
		// text only from compared on: a bot-proposed (state proposed) answer is not yet an AI first pass
		if p.HasResult && (p.ProposedAnswer != "" || p.Rationale != "") && (state == brain.StateCompared || state == brain.StateJudged || state == brain.StateReviewed) {
			o.Result = &qResultOut{ProposedAnswer: p.ProposedAnswer, Rationale: p.Rationale}
		}
	}
	o.Kind = kind
	if !dl.IsZero() {
		o.DeadlineAt = dl.In(shanghai).Format(time.RFC3339)
	}
	h := hoursOf(kind, dl, submitted, caps)
	o.Hours = h
	overdue := !dl.IsZero() && s.now().After(dl)
	switch {
	case p != nil && (p.Purged || state == brain.StatePurged):
		o.UI, o.Label, o.Result = uiPurged, "家长已删除这道题的解析", nil
	case state == brain.StateQueued:
		o.UI, o.Label = uiWaiting, fmt.Sprintf("待解答（排队中），预计 %d 小时内出结果", h)
	case state == brain.StateClaimed || state == brain.StateProposed:
		o.UI, o.Label = uiWorking, "AI 判卷中"
	case state == brain.StateCompared || state == brain.StateJudged:
		o.UI, o.Label = uiFirstPass, "AI 初评 · 待家长核"
		// compare_outcome compares the AI assistants' proposed answers with EACH OTHER (match = at least two
		// different assistants all agree; mismatch = they disagree, nobody is picked; n/a = fewer than two):
		// it says nothing about the child's answer, so nothing here may judge it.
		switch o.CompareOutcome {
		case "match":
			o.CompareText = "两位 AI 助手的答案一致，待家长核"
		case "mismatch", "n/a":
			o.CompareText = "AI 没有一致答案，需要家长看"
		}
		if o.CompareText == "" && o.NeedsManual {
			o.CompareText = "AI 没有一致答案，需要家长看"
		}
		// a match must come with the text; with no outcome and no text at all there is nothing to draw either:
		// say plainly that the first pass is there and the parent can read it
		if o.Result == nil && (o.CompareOutcome == "match" || o.CompareText == "") {
			o.CompareText = "AI 初评已出，详情请家长查看"
		}
	case state == brain.StateReviewed:
		o.UI, o.Label = uiReviewed, "家长已核"
		switch o.ReviewVerdict {
		case "correct":
			o.Label += "：判对"
		case "partial":
			o.Label += "：部分对"
		case "wrong":
			o.Label += "：判错"
		}
	case state == brain.StateFailed:
		o.UI, o.Label = uiFailed, "处理失败，已通知家长，等人工处理"
	case state == brain.StateExpired:
		o.UI, o.Label = uiExpired, fmt.Sprintf("超过 %d 小时仍没有结果，已转人工", h)
	default:
		o.UI, o.Label = uiUnknown, "状态更新中"
	}
	if overdue && (o.UI == uiWaiting || o.UI == uiWorking) {
		o.UI, o.Label = uiOverdue, "已超过预期时间，正在转人工"
	}
	return o
}

func summarize(items []qItemOut) qSummary {
	var sm qSummary
	for _, it := range items {
		sm.Total++
		switch it.UI {
		case uiWaiting:
			sm.Waiting++
		case uiWorking:
			sm.AIWorking++
		case uiOverdue:
			sm.AIWorking++
		case uiFirstPass:
			sm.FirstPass++
		case uiReviewed:
			sm.Reviewed++
		case uiFailed, uiExpired:
			sm.Manual++
		case uiPurged:
			sm.Purged++
		case uiNotQueued:
			sm.NotQueued++
		case uiRetry:
			sm.Retrying++
		default:
			sm.Unknown++
		}
	}
	sm.AnyPending = sm.Waiting+sm.AIWorking+sm.Retrying+sm.Unknown > 0
	sm.AllDone = sm.Total > 0 && !sm.AnyPending
	return sm
}

func finalState(p brain.Pending) bool {
	return p.Purged || p.State == brain.StatePurged || p.State == brain.StateFailed || p.State == brain.StateExpired ||
		(p.State == brain.StateReviewed && p.HasResult)
}

func (s *Server) memoTerm(p brain.Pending) {
	q := s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.term[p.ItemID]; !ok {
		q.termOrd = append(q.termOrd, p.ItemID)
		for len(q.termOrd) > queueTermCap {
			delete(q.term, q.termOrd[0])
			q.termOrd = q.termOrd[1:]
		}
	}
	q.term[p.ItemID] = p
}

// refreshPend reads the brain for the not-yet-final items of one submission. Pure
// read (GET only). One batch read per refresh; per-item reads (≤20) only when the
// batch route is not deployed. Failures keep the previous reads and set stale.
func (s *Server) refreshPend(ctx context.Context, l *link, v *qView) {
	q := s.q
	var need []linkItem
	q.mu.Lock()
	for _, li := range l.okItems() {
		if p, ok := q.term[li.ItemID]; ok {
			v.pend[li.ItemID] = p
		} else {
			need = append(need, li)
		}
	}
	useBatch := !s.now().Before(q.batchOff)
	q.mu.Unlock()
	v.at, v.stale = s.now(), false
	if len(need) == 0 {
		return
	}
	got := map[string]bool{}
	if useBatch {
		sr, err := s.Queue.PendingBySession(ctx, l.CID, true)
		switch {
		case err == nil:
			for _, p := range sr.Items {
				v.pend[p.ItemID] = p
				got[p.ItemID] = true
				if finalState(p) {
					s.memoTerm(p)
				}
			}
		case brain.NotReady(err):
			q.mu.Lock()
			q.batchOff = s.now().Add(queueBatchDown)
			q.mu.Unlock()
			useBatch = false
		default:
			s.Log.Warn("queue status: batch read failed", "err", errText(err))
			v.stale = true
			return
		}
	}
	if !useBatch {
		for i, li := range need {
			if i >= queueReadMax {
				v.stale = true
				break
			}
			p, err := s.Queue.PendingDetail(ctx, li.ItemID, true)
			if err != nil {
				s.Log.Warn("queue status: item read failed", "err", errText(err))
				v.stale = true
				continue
			}
			v.pend[li.ItemID] = *p
			got[li.ItemID] = true
			if finalState(*p) {
				s.memoTerm(*p)
			}
		}
		return
	}
	for _, li := range need {
		if !got[li.ItemID] {
			v.stale = true // the brain did not list it (not visible to this token): keep what we had
		}
	}
}

func errText(err error) string {
	if be := asBrainErr(err); be != nil {
		return be.Error()
	}
	return "unknown"
}

// buildView is the status of one submission (cached 5 s per cid; one upstream read at a time).
func (s *Server) buildView(ctx context.Context, l *link) *queueViewOut {
	q := s.q
	m := q.lockFor(l.CID)
	m.Lock()
	defer m.Unlock()
	q.mu.Lock()
	v := q.views[l.CID]
	q.mu.Unlock()
	hasOK := len(l.okItems()) > 0
	if hasOK && (v == nil || s.now().Sub(v.at) >= s.viewTTL()) {
		if v == nil {
			v = &qView{pend: map[string]brain.Pending{}}
		}
		s.refreshPend(ctx, l, v)
		q.mu.Lock()
		if _, ok := q.views[l.CID]; !ok {
			q.viewOrd = append(q.viewOrd, l.CID)
			for len(q.viewOrd) > queueCacheCap {
				delete(q.views, q.viewOrd[0])
				q.viewOrd = q.viewOrd[1:]
			}
		}
		q.views[l.CID] = v
		q.mu.Unlock()
	}
	out := &queueViewOut{CID: l.CID, SetID: l.SetID, Items: []qItemOut{}, UpdatedAt: s.now().In(shanghai).Format(time.RFC3339)}
	var caps *brain.Capabilities
	q.mu.Lock()
	caps = q.caps
	q.mu.Unlock()
	for _, li := range l.Items {
		var p *brain.Pending
		if v != nil && li.ItemID != "" {
			if x, ok := v.pend[li.ItemID]; ok {
				p = &x
			}
		}
		out.Items = append(out.Items, s.itemView(li, p, l.SubmittedAt, caps))
	}
	if v != nil {
		out.Stale = v.stale
	}
	out.Summary = summarize(out.Items)
	return out
}

// ---- endpoints ------------------------------------------------------------------------

// queueRoute serves /api/practice/attempts/<cid>/queue (GET) and .../enqueue (POST).
func (s *Server) queueRoute(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/") // attempts, <cid>, queue|enqueue
	if !s.queueOn() || len(parts) != 3 || (parts[2] != "queue" && parts[2] != "enqueue") || !uuidRe.MatchString(parts[1]) {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	cid := strings.ToLower(parts[1])
	switch parts[2] {
	case "queue":
		s.need(w, r, http.MethodGet, s.authed(func(w http.ResponseWriter, r *http.Request) { s.queueStatus(w, r, cid) }))
	default:
		s.need(w, r, http.MethodPost, s.authed(func(w http.ResponseWriter, r *http.Request) { s.queueEnqueue(w, r, cid) }))
	}
}

func (s *Server) queueStatus(w http.ResponseWriter, r *http.Request, cid string) {
	l := s.q.links.get(cid)
	if l == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	writeJSON(w, http.StatusOK, s.buildView(r.Context(), l))
}

func (s *Server) queueEnqueue(w http.ResponseWriter, r *http.Request, cid string) {
	l := s.q.links.get(cid)
	if l == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	now := s.now()
	s.q.mu.Lock()
	hist := s.q.rl[cid][:0:0]
	for _, t := range s.q.rl[cid] {
		if now.Sub(t) < time.Minute {
			hist = append(hist, t)
		}
	}
	limited := len(hist) >= enqueueRateCount
	if !limited {
		hist = append(hist, now)
	}
	s.q.rl[cid] = hist
	s.q.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "30")
		writeErr(w, http.StatusTooManyRequests, "too_many_requests", "请稍后再试")
		return
	}
	retried := s.retryEnqueue(r.Context(), cid, true)
	l = s.q.links.get(cid)
	writeJSON(w, http.StatusOK, map[string]any{"retried": retried, "queue": queueOutOf(l)})
}

func (s *Server) viewTTL() time.Duration {
	if s.QueueViewTTL != 0 {
		return max(s.QueueViewTTL, 0)
	}
	return queueViewTTL
}
