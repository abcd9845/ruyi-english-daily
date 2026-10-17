package server

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
)

// ---- completion notification (S1 notify poller) -----------------------------------------
//
// One goroutine, started only with RUYI_QUEUE_NOTIFY_ENABLED=1, polls the brain's event
// outbox with the NOTIFY token (45 s ±10%, never below 36 s), folds the events into the
// local state per session_ref (= client_attempt_id), and tells the parent ONCE per
// submission when everything of it has been processed.
//
// Order of one tick (the PM-approved order):
//   1. read events after the cursor;
//   2. INGEST: fold them into the session state and write notify.json atomically
//      (tmp + fsync + rename + fsync dir) -- pending notifications are now durable;
//   3. ACK the highest ingested id (only ids already durable are ever acked);
//   4. for each session whose items are all processed: write "sending", post to ntfy,
//      append sent.jsonl (fsync), write "sent".
// A restart finds any unsent session still open (or "sending") in notify.json and
// sends it then: nothing is lost after step 2. The only duplicate window is a crash
// between a 2xx from ntfy and the sent.jsonl write (milliseconds); a request that
// reached ntfy but whose answer was lost counts as failed and is retried (same risk
// as the submission heads-up of 单 C).
//
// A damaged/missing notify.json next to known submissions starts "reconcile": each
// recent unsent session is read from the brain once (batch read, else per item) and
// its finished items are folded in, so events that were acked before the loss are not
// lost; sent.jsonl (a separate file) keeps already-told sessions from being told again.

const (
	pollPagesMax   = 5
	pollBackoffMax = 5 * time.Minute
	quietDefault   = 5 * time.Minute
	notifyGiveUp   = 24 * time.Hour
	reconcileAge   = 4 * 24 * time.Hour
	reconcileLate  = time.Hour // a session this long past its last deadline without completion is read once
	authWarnEvery  = 30 * time.Minute
	notifyTitleStr = "Ruyi practice - AI first pass"
)

var errSimCrash = errors.New("simulated crash")

type poller struct {
	s     *Server
	st    *notifyState
	quiet time.Duration
	fails int

	lastAuthWarn time.Time
	capsLogged   bool
}

// EnableNotify prepares the poller (state dir lock, state load, reconcile marks).
// It starts no goroutine; StartQueue does. An error means "no poller" (logged by
// the caller): the site itself keeps working.
func (s *Server) EnableNotify() error {
	if !s.queueOn() {
		return errors.New("queue is off")
	}
	if s.poll != nil {
		return nil
	}
	lk, err := acquireLock(s.QueueDir)
	if err != nil {
		return err
	}
	s.qlock = lk
	p := &poller{s: s, st: loadNotify(s.QueueDir, s.Log, s.now()), quiet: s.QueueQuiet}
	if p.quiet <= 0 {
		p.quiet = quietDefault
	}
	if p.st.recovered {
		p.markRecovered()
	}
	s.poll = p
	return nil
}

// StartQueue starts the background goroutines of the queue: the enqueue retry sweep
// and (if enabled and possible) the notify poller. Safe to call once; Drain stops them.
func (s *Server) StartQueue() {
	if !s.queueOn() {
		return
	}
	sweep := s.QueueSweep
	if sweep <= 0 {
		sweep = queueSweepEvery
	}
	s.nwg.Add(1)
	go s.queueSweep(sweep)
	if !s.QueueNotify {
		return
	}
	if err := s.EnableNotify(); err != nil {
		s.Log.Warn("queue notifications are OFF", "why", err.Error())
		return
	}
	s.nwg.Add(1)
	go s.poll.loop()
}

func (p *poller) crash(stage string) bool {
	return p.s.QueueHook != nil && p.s.QueueHook(stage)
}

func (p *poller) interval() time.Duration {
	base := p.s.QueuePoll
	if base <= 0 {
		base = 45 * time.Second
	}
	j := rand.Float64()*2 - 1
	if p.s.QueueJitter != nil {
		j = max(-1, min(1, p.s.QueueJitter()))
	}
	return time.Duration(float64(base) * (1 + 0.1*j))
}

func (p *poller) loop() {
	defer p.s.nwg.Done()
	delay := time.Second // first look soon after start (catch-up), then the regular period
	for {
		t := time.NewTimer(delay)
		select {
		case <-p.s.nstop:
			t.Stop()
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		delay = p.step(ctx)
		cancel()
	}
}

// step runs one tick and returns the wait before the next one. A tick that could not
// read the events (brain down, token refused, ...) doubles the wait each time, up to
// 5 minutes; the first successful read goes back to the normal period.
func (p *poller) step(ctx context.Context) time.Duration {
	if err := p.tick(ctx); err != nil {
		p.fails++
	} else {
		p.fails = 0
	}
	delay := p.interval()
	for i := 0; i < p.fails && delay < pollBackoffMax; i++ {
		delay *= 2
	}
	return min(delay, pollBackoffMax)
}

// PollOnce runs one tick (tests, and the loop's body).
func (s *Server) PollOnce(ctx context.Context) error {
	if s.poll == nil {
		return errors.New("poller not enabled")
	}
	return s.poll.tick(ctx)
}

func (p *poller) save() error {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	return p.st.saveLocked(p.s.now())
}

func (p *poller) sess(cid string) *sessState {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	ss := p.st.Sessions[cid]
	if ss == nil {
		ss = &sessState{State: sessOpen, FirstSeen: p.s.now().UTC(), Terminal: map[string]string{}}
		if p.st.sent[cid] {
			ss.State = sessSent
		}
		p.st.Sessions[cid] = ss
	}
	if ss.Terminal == nil {
		ss.Terminal = map[string]string{}
	}
	return ss
}

// markRecovered asks for one brain read of every recent, not-yet-told submission.
func (p *poller) markRecovered() {
	now := p.s.now()
	for _, l := range p.s.q.links.all() {
		if len(l.okItems()) == 0 || now.Sub(l.SubmittedAt) > reconcileAge || p.st.sent[l.CID] {
			continue
		}
		ss := p.sess(l.CID)
		if ss.State == sessOpen || ss.State == sessSending {
			ss.Reconcile = true
		}
	}
	if p.st.firstOpen {
		p.s.Log.Info("queue notify state not found (first start of the poller): reading recent submissions from the brain once")
		return
	}
	p.s.Log.Warn("queue notify state was missing or damaged: reconciling recent submissions with the brain")
}

// ingest folds events into the sessions. Idempotent: replays change nothing.
func (p *poller) ingest(evs []brain.QueueEvent) {
	now := p.s.now().UTC()
	for _, e := range evs {
		if e.ID > p.st.Cursor {
			p.st.Cursor = e.ID
		}
		if e.SessionRef == "" {
			continue // not part of a set: nothing to aggregate
		}
		var t string
		switch e.Type {
		case brain.EventDone:
			t = "done"
		case brain.EventFailed:
			t = "failed"
		case brain.EventExpired:
			t = "expired"
			if e.ErrorCode == "purged" || e.State == brain.StatePurged {
				t = "purged"
			}
		default:
			continue // reanalyzed / stale_rev (S3) and unknown types never notify
		}
		id := e.ItemID
		if id == "" {
			id = "q:" + e.QRef
		}
		ss := p.sess(e.SessionRef)
		if ss.State != sessOpen && ss.State != sessSending {
			continue // already told (or gave up): an event for a finished session is a no-op
		}
		ss.Terminal[id] = t
		ss.LastEvent = now
		if e.SessionTotal > 0 {
			ss.SessionTotal = e.SessionTotal
		}
		if e.SessionProcessed >= 0 {
			ss.SessionDone = e.SessionProcessed
		}
		if e.SessionDone {
			ss.DoneSeen = true
		}
	}
}

func (p *poller) tick(ctx context.Context) error {
	s := p.s
	now := s.now()
	p.markLate(now)
	p.markExhausted(now)
	if !p.capsLogged {
		p.capsLogged = true
		if c := s.enqueueCaps(ctx); c != nil {
			s.Log.Info("queue capabilities", "absolute", c.AbsoluteEnabled, "judged", c.JudgedEnabled, "images", c.ImagesEnabled)
		}
	}
	// an ack that failed earlier is owed: repeat it first (harmless if the brain already has it)
	if p.st.Cursor > p.st.Acked {
		p.ack(ctx)
	}
	// 1-3: read, ingest (durable), ack
	var readErr error // a failed read makes the tick fail (the loop backs off) but the rest still runs
	for page := 0; page < pollPagesMax; page++ {
		evs, err := s.Queue.QueueEvents(ctx, p.st.Cursor, brain.MaxEventsPage)
		if err != nil {
			p.warnRead(err)
			readErr = err
			break
		}
		if len(evs) == 0 {
			break
		}
		p.ingest(evs)
		if err := p.save(); err != nil {
			s.Log.Error("queue notify state is not writable; stopping this tick (nothing acked)")
			return err
		}
		if p.crash("after_ingest_save") {
			return errSimCrash
		}
		p.ack(ctx)
		if p.crash("after_ack") {
			return errSimCrash
		}
		if len(evs) < brain.MaxEventsPage {
			break
		}
	}
	// reconcile sessions that asked for a read of the brain
	p.reconcile(ctx)
	// 4: tell the parent
	if err := p.notifyDue(ctx); err != nil {
		return err
	}
	return readErr
}

// ack tells the brain everything up to the cursor is stored here. A failure is only
// a warning: the ack is owed and repeated at the next tick; events replayed meanwhile
// change nothing (ingest is idempotent).
func (p *poller) ack(ctx context.Context) {
	cur := p.st.Cursor
	if err := p.s.Queue.AckEvents(ctx, cur); err != nil {
		p.s.Log.Warn("queue event ack failed (will be repeated; harmless)", "err", errText(err))
		return
	}
	p.st.mu.Lock()
	p.st.Acked = cur
	p.st.mu.Unlock()
	if err := p.save(); err != nil {
		p.s.Log.Warn("queue notify state not saved after ack", "err", err.Error())
	}
}

func (p *poller) warnRead(err error) {
	s := p.s
	if be := asBrainErr(err); be != nil && be.Kind == brain.KindAuth {
		if s.now().Sub(p.lastAuthWarn) >= authWarnEvery {
			p.lastAuthWarn = s.now()
			s.Log.Warn("queue notify token rejected by the brain; no fallback to another token", "err", be.Error())
		}
		return
	}
	if errors.Is(err, brain.ErrNotifyDisabled) {
		s.Log.Warn("queue notify token is not configured")
		return
	}
	s.Log.Warn("queue events read failed", "err", errText(err))
}

// markLate schedules one reconcile read for sessions that should have finished long
// ago but never produced their events (lost events, a brain restore, ...).
func (p *poller) markLate(now time.Time) {
	for _, l := range p.s.q.links.all() {
		ok := l.okItems()
		if len(ok) == 0 || now.Sub(l.SubmittedAt) > reconcileAge || p.st.sent[l.CID] {
			continue
		}
		if dl := l.lastDeadline(); dl.IsZero() || now.Before(dl.Add(reconcileLate)) {
			continue
		}
		ss := p.sess(l.CID)
		if (ss.State == sessOpen || ss.State == sessSending) && !ss.Reconciled {
			ss.Reconcile = true
		}
	}
}

// markExhausted opens a session for a recent submission that has answers the site gave up
// queueing (retry_exhausted): the parent hears about them even if no event will ever come.
func (p *poller) markExhausted(now time.Time) {
	for _, l := range p.s.q.links.all() {
		if now.Sub(l.SubmittedAt) > reconcileAge || p.st.sent[l.CID] {
			continue
		}
		for _, li := range l.Items {
			if li.Enq == enqNotQueued && li.Reason == "retry_exhausted" {
				p.sess(l.CID)
				break
			}
		}
	}
}

func terminalOf(pd brain.Pending) string {
	switch {
	case pd.Purged || pd.State == brain.StatePurged:
		return "purged"
	case pd.State == brain.StateCompared || pd.State == brain.StateJudged || pd.State == brain.StateReviewed:
		return "done"
	case pd.State == brain.StateFailed:
		return "failed"
	case pd.State == brain.StateExpired:
		return "expired"
	}
	return ""
}

func (p *poller) reconcile(ctx context.Context) {
	s := p.s
	var todo []string
	p.st.mu.Lock()
	for cid, ss := range p.st.Sessions {
		if ss.Reconcile && (ss.State == sessOpen || ss.State == sessSending) {
			todo = append(todo, cid)
		}
	}
	p.st.mu.Unlock()
	sort.Strings(todo)
	changed := false
	for _, cid := range todo {
		l := s.q.links.get(cid)
		ss := p.sess(cid)
		if l == nil || len(l.okItems()) == 0 {
			ss.Reconcile = false
			changed = true
			continue
		}
		found := map[string]brain.Pending{}
		sr, err := s.Queue.PendingBySession(ctx, cid, false)
		switch {
		case err == nil:
			for _, pd := range sr.Items {
				found[pd.ItemID] = pd
			}
		case brain.NotReady(err):
			for i, li := range l.okItems() {
				if i >= queueReadMax {
					break
				}
				if pd, e2 := s.Queue.PendingDetail(ctx, li.ItemID, false); e2 == nil {
					found[li.ItemID] = *pd
				}
			}
		default:
			s.Log.Warn("queue reconcile: read failed; will try again", "err", errText(err))
			continue
		}
		for id, pd := range found {
			if t := terminalOf(pd); t != "" {
				if _, have := ss.Terminal[id]; !have {
					ss.Terminal[id] = t
				}
			}
		}
		ss.Reconcile, ss.Reconciled = false, true
		changed = true
	}
	if changed {
		_ = p.save()
	}
}

type sessCount struct {
	done, manual, purged int
	exhausted            int // answers the site gave up queueing (retry_exhausted)
	fallback             bool
}

// complete decides whether every item of the session has been processed.
// Priority: the local links (authoritative) > the brain's session_total/session_done
// > a quiet period without new events (counted "at least n").
func (p *poller) complete(cid string, ss *sessState, now time.Time) (sessCount, bool) {
	var c sessCount
	count := func(t string) {
		switch t {
		case "done":
			c.done++
		case "failed", "expired":
			c.manual++
		case "purged":
			c.purged++
		}
	}
	if l := p.s.q.links.get(cid); l != nil {
		for _, li := range l.Items {
			if li.Enq == enqFailed {
				return c, false // still being queued
			}
			if li.Enq == enqNotQueued && li.Reason == "retry_exhausted" {
				c.exhausted++
			}
		}
		oks := l.okItems()
		if len(oks) == 0 {
			return c, c.exhausted > 0
		}
		for _, li := range oks {
			t, ok := ss.Terminal[li.ItemID]
			if !ok {
				return c, false
			}
			count(t)
		}
		return c, true
	}
	for _, t := range ss.Terminal {
		count(t)
	}
	n := len(ss.Terminal)
	switch {
	case n == 0:
		return c, false
	case ss.DoneSeen, ss.SessionTotal > 0 && n >= ss.SessionTotal:
		return c, true
	case !ss.LastEvent.IsZero() && now.Sub(ss.LastEvent) >= p.quiet:
		c.fallback = true
		return c, true
	}
	return c, false
}

func (p *poller) notifyDue(ctx context.Context) error {
	s := p.s
	now := s.now()
	var cids []string
	p.st.mu.Lock()
	for cid, ss := range p.st.Sessions {
		if ss.State == sessOpen || ss.State == sessSending {
			cids = append(cids, cid)
		}
	}
	p.st.mu.Unlock()
	sort.Slice(cids, func(i, j int) bool { return p.st.Sessions[cids[i]].FirstSeen.Before(p.st.Sessions[cids[j]].FirstSeen) })
	var firstErr error
	for _, cid := range cids {
		select {
		case <-s.nstop:
			return nil
		default:
		}
		ss := p.sess(cid)
		if p.st.sent[cid] { // told before (sent.jsonl outlives a damaged notify.json)
			ss.State, ss.SentAt = sessSent, now.UTC()
			_ = p.save()
			continue
		}
		if ss.NextTry.After(now) {
			continue
		}
		c, ok := p.complete(cid, ss, now)
		if !ok {
			continue
		}
		if c.done == 0 && c.manual == 0 && c.exhausted == 0 { // everything was deleted: nothing to tell
			ss.State = sessSilent
			if err := p.save(); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		title, body := notifyText(s.q.links.get(cid), cid, c, now)
		ss.State = sessSending
		if err := p.save(); err != nil {
			return err
		}
		if p.crash("before_ntfy") {
			return errSimCrash
		}
		sent := s.Topic != "" && p.sendNtfy(title, body)
		if sent && p.crash("after_ntfy") { // ntfy said 2xx, nothing recorded yet: the duplicate window
			return errSimCrash
		}
		if sent {
			if err := p.st.markSent(cid, now); err != nil {
				s.Log.Error("queue sent log is not writable (a restart could repeat this notification)")
			}
			if p.crash("after_sent_log") {
				return errSimCrash
			}
			ss.State, ss.SentAt = sessSent, now.UTC()
			ss.NextTry, ss.Tries = time.Time{}, 0
			s.Log.Info("queue completion notification sent", "processed", c.done, "manual", c.manual, "purged", c.purged)
		} else {
			ss.State = sessOpen
			ss.Tries++
			if now.Sub(ss.FirstSeen) > notifyGiveUp {
				ss.State = sessGaveUp
				s.Log.Warn("queue completion notification given up after 24 h")
			} else {
				d := backoffNotify(ss.Tries)
				ss.NextTry = now.Add(d)
				s.Log.Warn("queue completion notification not sent; will retry", "tries", ss.Tries, "in", d.String())
			}
		}
		if err := p.save(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func backoffNotify(tries int) time.Duration {
	d := time.Minute << min(max(tries-1, 0), 5)
	return min(d, 30*time.Minute)
}

// sendNtfy posts with the same attempts/timeouts as the submission heads-up
// (postNtfy: 10 s each, up to ntfyMaxAttempts, doubling delay) in this goroutine.
func (p *poller) sendNtfy(title, body string) bool {
	s := p.s
	delay := s.NtfyRetryDelay
	for attempt := 1; ; attempt++ {
		if s.postNtfy(title, body, attempt) {
			return true
		}
		if attempt >= ntfyMaxAttempts {
			return false
		}
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-s.nstop:
			t.Stop()
			s.Log.Warn("queue notification retry abandoned at shutdown")
			return false
		}
		delay *= 2
	}
}

// notifyText: one short paragraph, no answers. It starts differently from the
// submission heads-up ("如意练习：<set> n 题 … 已提交"): "AI初评已出，待家长核" /
// "AI处理完毕，需要人工".
func notifyText(l *link, cid string, c sessCount, now time.Time) (title, body string) {
	setTitle := "这套题"
	if l != nil && l.SetTitle != "" {
		setTitle = l.SetTitle
	}
	var b strings.Builder
	if c.done > 0 {
		b.WriteString("AI初评已出，待家长核：")
	} else {
		b.WriteString("AI处理完毕，需要人工：")
	}
	fmt.Fprintf(&b, "%s", setTitle)
	var parts []string
	if c.done > 0 {
		parts = append(parts, fmt.Sprintf("%d 题有 AI 初评", c.done))
	}
	if c.manual > 0 {
		parts = append(parts, fmt.Sprintf("%d 题待人工", c.manual))
	}
	if c.purged > 0 {
		parts = append(parts, fmt.Sprintf("%d 题已被删除", c.purged))
	}
	if c.exhausted > 0 {
		parts = append(parts, fmt.Sprintf("%d 题没能排队（答案在存档里）", c.exhausted))
	}
	b.WriteString("，")
	if c.fallback {
		b.WriteString("（至少）")
	}
	b.WriteString(strings.Join(parts, "，"))
	fmt.Fprintf(&b, "。attempt %s。%s（北京时间）。", cid[:8], now.In(shanghai).Format("15:04"))
	return notifyTitleStr, b.String()
}
