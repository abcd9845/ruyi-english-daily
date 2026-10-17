package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// ---- local state of the queue (S1) ------------------------------------------------
//
// All files live in RUYI_QUEUE_STATE_DIR (default /var/lib/ruyi-site/queue, 0700,
// files 0600). They hold ids, states and times ONLY: never an answer, never an
// AI text, never a token or the ntfy topic.
//
//	links.jsonl   append-only, one full snapshot per change of a submission's
//	              queue links (the LAST line of a cid wins at load): which brain
//	              items belong to which client_attempt_id (= session_ref).
//	notify.json   atomic replace (tmp + fsync + rename + fsync dir): the event
//	              cursor and, per session, which items finished and whether the
//	              parent was told.
//	sent.jsonl    append-only + fsync: one line per notified session. A second,
//	              independent record of "already told", so that a damaged
//	              notify.json cannot make the site tell the parent twice.
//	.lock         flock: one process per state dir (a second one does not poll).

const (
	linksFile  = "links.jsonl"
	notifyFile = "notify.json"
	sentFile   = "sent.jsonl"
	lockFile   = ".lock"

	// linksKeep: submissions older than this are not loaded at startup (the longest
	// brain deadline is 48 h; this is far beyond it).
	linksKeep      = 30 * 24 * time.Hour
	sessionsKeep   = 14 * 24 * time.Hour
	maxSessions    = 2000
	maxQueueLine   = 1 << 20
	enqMaxTries    = 5
	enqRetryWindow = 24 * time.Hour
)

// Enqueue states of one answer.
const (
	enqOK        = "ok"
	enqFailed    = "failed"     // transient: retried in the background
	enqNotQueued = "not_queued" // refused for good (reason says why)
)

type linkItem struct {
	QRef        string    `json:"qref"`
	Status      string    `json:"status"` // the grade status that made it eligible
	Rev         int       `json:"rev,omitempty"`
	AttemptKind string    `json:"attempt_kind,omitempty"`
	Enq         string    `json:"enq"`
	Reason      string    `json:"reason,omitempty"`
	ItemID      string    `json:"item_id,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	DeadlineAt  time.Time `json:"deadline_at,omitzero"`
	Created     bool      `json:"created,omitempty"`
	Tries       int       `json:"tries,omitempty"`
	NextTry     time.Time `json:"next_try,omitzero"`
}

type link struct {
	V           int        `json:"v"`
	CID         string     `json:"cid"`
	SetID       string     `json:"set_id"`
	SetTitle    string     `json:"set_title"`
	SubmittedAt time.Time  `json:"submitted_at"`
	AnsweredAt  time.Time  `json:"answered_at,omitzero"` // the value sent with grade (zero = not sent)
	Items       []linkItem `json:"items"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (l *link) clone() *link {
	c := *l
	c.Items = append([]linkItem(nil), l.Items...)
	return &c
}

// okItems are the answers that are queued at the brain.
func (l *link) okItems() []linkItem {
	var out []linkItem
	for _, it := range l.Items {
		if it.Enq == enqOK && it.ItemID != "" {
			out = append(out, it)
		}
	}
	return out
}

func (l *link) lastDeadline() time.Time {
	var d time.Time
	for _, it := range l.Items {
		if it.Enq == enqOK && it.DeadlineAt.After(d) {
			d = it.DeadlineAt
		}
	}
	return d
}

type linkStore struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	f        *os.File
	m        map[string]*link
	errShown bool
}

// openLinks creates the state directory (0700) and loads links.jsonl. An
// unusable directory is an error (the operator must fix it before turning the
// queue on): the queue must never silently run without its state.
func openLinks(dir string, log *slog.Logger, now time.Time) (*linkStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("queue state dir is not usable")
	}
	ls := &linkStore{dir: dir, log: log, m: map[string]*link{}}
	f, err := os.OpenFile(filepath.Join(dir, linksFile), os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("queue links file is not usable")
	}
	ls.f = f
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxQueueLine)
	for sc.Scan() {
		var l link
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.CID == "" {
			continue
		}
		if now.Sub(l.SubmittedAt) > linksKeep {
			delete(ls.m, l.CID)
			continue
		}
		ls.m[l.CID] = &l
	}
	return ls, nil
}

func (ls *linkStore) get(cid string) *link {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if l := ls.m[cid]; l != nil {
		return l.clone()
	}
	return nil
}

func (ls *linkStore) all() []*link {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := make([]*link, 0, len(ls.m))
	for _, l := range ls.m {
		out = append(out, l.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubmittedAt.Before(out[j].SubmittedAt) })
	return out
}

// put stores the snapshot (memory first, so the page sees it even if the disk
// write fails; the failure is logged once).
func (ls *linkStore) put(l *link, now time.Time) error {
	c := l.clone()
	c.V, c.UpdatedAt = 1, now.UTC()
	line, err := json.Marshal(c)
	if err != nil {
		return errors.New("encode failed")
	}
	line = append(line, '\n')
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.m[c.CID] = c
	if ls.f == nil {
		return errors.New("links closed")
	}
	if _, err = ls.f.Write(line); err == nil {
		err = ls.f.Sync()
	}
	if err != nil {
		if !ls.errShown && ls.log != nil {
			ls.log.Error("queue links file is not writable; queue state may be lost on restart")
			ls.errShown = true
		}
		return errors.New("links not writable")
	}
	ls.errShown = false
	return nil
}

func (ls *linkStore) close() {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.f != nil {
		ls.f.Close()
		ls.f = nil
	}
}

// ---- notify state ------------------------------------------------------------------

// Session states of the completion notification.
const (
	sessOpen    = "open"
	sessSending = "sending" // written before the ntfy post; found at startup = not confirmed sent
	sessSent    = "sent"
	sessSilent  = "silent"  // nothing to tell (everything purged)
	sessGaveUp  = "gave_up" // ntfy kept failing for 24 h
)

type sessState struct {
	State        string            `json:"state"`
	Terminal     map[string]string `json:"terminal,omitempty"` // item id -> done|failed|expired|purged
	FirstSeen    time.Time         `json:"first_seen"`
	LastEvent    time.Time         `json:"last_event,omitzero"`
	SentAt       time.Time         `json:"sent_at,omitzero"`
	Tries        int               `json:"tries,omitempty"`
	NextTry      time.Time         `json:"next_try,omitzero"`
	SessionTotal int               `json:"session_total,omitempty"`
	SessionDone  int               `json:"session_done,omitempty"`
	// Reconcile asks for one read of the brain for this session (state was lost, or
	// its events never came); Reconciled records that it was done.
	Reconcile  bool `json:"reconcile,omitempty"`
	Reconciled bool `json:"reconciled,omitempty"`
	// DoneSeen: an event said session_done=true (the brain's own "whole session processed").
	DoneSeen bool `json:"done_seen,omitempty"`
}

type notifyState struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	Version  int                   `json:"v"`
	Cursor   int64                 `json:"cursor"`
	Acked    int64                 `json:"acked"` // highest id the brain confirmed as acked (cursor > acked = an ack is owed)
	Sessions map[string]*sessState `json:"sessions"`
	Updated  time.Time             `json:"updated"`
	sent     map[string]bool       // sent.jsonl
	// recovered: notify.json was missing or damaged while submissions are known
	// (a fresh start with nothing to recover from is not "recovered").
	recovered bool
}

// loadNotify reads notify.json and sent.jsonl. A damaged notify.json is renamed
// aside (never deleted) and the state starts empty, with recovered=true.
func loadNotify(dir string, log *slog.Logger, now time.Time) *notifyState {
	st := &notifyState{dir: dir, log: log, Version: 1, Sessions: map[string]*sessState{}, sent: map[string]bool{}}
	if f, err := os.Open(filepath.Join(dir, sentFile)); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 16<<10), 64<<10)
		for sc.Scan() {
			var v struct {
				CID string `json:"cid"`
			}
			if json.Unmarshal(sc.Bytes(), &v) == nil && v.CID != "" {
				st.sent[v.CID] = true
			}
		}
		f.Close()
	}
	b, err := os.ReadFile(filepath.Join(dir, notifyFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		// no state file: a brand-new install (nothing sent, no submissions yet) is NOT a loss;
		// a missing file next to a non-empty sent log or links log is.
		st.recovered = len(st.sent) > 0
		if fi, err := os.Stat(filepath.Join(dir, linksFile)); err == nil && fi.Size() > 0 {
			st.recovered = true // submissions are known (links.jsonl) but the notify state is gone
		}
	case err != nil:
		st.recovered = true
		log.Warn("queue notify state unreadable; starting empty and reconciling")
	default:
		var d notifyState
		if json.Unmarshal(b, &d) != nil || d.Version != 1 {
			aside := fmt.Sprintf("%s.corrupt-%d", notifyFile, now.Unix())
			_ = os.Rename(filepath.Join(dir, notifyFile), filepath.Join(dir, aside))
			log.Warn("queue notify state is damaged; moved aside, starting empty and reconciling")
			st.recovered = true
			break
		}
		st.Cursor = d.Cursor
		st.Acked = d.Acked
		if d.Sessions != nil {
			st.Sessions = d.Sessions
		}
	}
	return st
}

// saveLocked writes notify.json atomically. Caller holds st.mu.
func (st *notifyState) saveLocked(now time.Time) error {
	st.Updated = now.UTC()
	if len(st.Sessions) > maxSessions { // prune the oldest finished sessions
		type kv struct {
			k string
			t time.Time
		}
		var done []kv
		for k, s := range st.Sessions {
			if s.State != sessOpen && s.State != sessSending {
				done = append(done, kv{k, s.FirstSeen})
			}
		}
		sort.Slice(done, func(i, j int) bool { return done[i].t.Before(done[j].t) })
		for _, d := range done {
			if len(st.Sessions) <= maxSessions {
				break
			}
			delete(st.Sessions, d.k)
		}
	}
	for k, s := range st.Sessions {
		if s.State != sessOpen && s.State != sessSending && now.Sub(s.FirstSeen) > sessionsKeep {
			delete(st.Sessions, k)
		}
	}
	b, err := json.Marshal(struct {
		V        int                   `json:"v"`
		Cursor   int64                 `json:"cursor"`
		Sessions map[string]*sessState `json:"sessions"`
		Updated  time.Time             `json:"updated"`
	}{1, st.Cursor, st.Sessions, st.Updated})
	if err != nil {
		return errors.New("encode failed")
	}
	tmp := filepath.Join(st.dir, notifyFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.New("state not writable")
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(st.dir, notifyFile))
	}
	if err == nil {
		if d, derr := os.Open(st.dir); derr == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	if err != nil {
		return errors.New("state not writable")
	}
	return nil
}

// markSent appends the session to sent.jsonl (fsync) BEFORE notify.json says "sent".
func (st *notifyState) markSent(cid string, now time.Time) error {
	line, _ := json.Marshal(map[string]any{"cid": cid, "at": now.UTC().Format(time.RFC3339)})
	f, err := os.OpenFile(filepath.Join(st.dir, sentFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("sent log not writable")
	}
	defer f.Close()
	if _, err = f.Write(append(line, '\n')); err == nil {
		err = f.Sync()
	}
	if err != nil {
		return errors.New("sent log not writable")
	}
	st.mu.Lock()
	st.sent[cid] = true
	st.mu.Unlock()
	return nil
}

// acquireLock takes the exclusive, non-blocking flock of the state dir. The
// returned file must stay open for as long as the lock is wanted.
func acquireLock(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("lock file not usable")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another process holds the queue state lock")
	}
	return f, nil
}
