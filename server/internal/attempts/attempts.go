// Package attempts archives submitted practice attempts as JSON Lines under a
// private directory (RUYI_ATTEMPTS_DIR). It is the local fallback record that
// exists whether or not the brain grading call worked.
//
// One line per attempt. A line never holds cookies, tokens or the family code:
// only the attempt id, the set, the answers and verdicts, a timestamp and a short
// client summary (user agent, IP).
package attempts

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the archive file inside the directory.
const FileName = "attempts.jsonl"

const (
	maxLine     = 1 << 20
	maxLoadSize = 64 << 20
)

// Item is one question of an attempt.
type Item struct {
	QRef    string `json:"qref"`
	Answer  any    `json:"answer,omitempty"`
	Status  string `json:"status"`
	Correct *bool  `json:"correct"`
	// Rev is the question revision the student saw (0 = unknown / not sent).
	Rev int `json:"rev,omitempty"`
	// Recorded is true only if the brain itself confirmed it recorded this answer
	// (unsupported items are recorded:false, so is everything archived without a
	// brain reply). AttemptID is the brain's integer id for it.
	Recorded  bool  `json:"recorded"`
	AttemptID int64 `json:"attempt_id,omitempty"`
	Duplicate bool  `json:"duplicate,omitempty"`
	// Capped marks a question the brain named in a 429 daily_cap refusal (the
	// whole batch was refused: nothing was written, Recorded is false).
	Capped bool `json:"capped,omitempty"`
}

// Summary counts the verdicts.
type Summary struct {
	Total      int `json:"total"`
	Graded     int `json:"graded"`
	Correct    int `json:"correct"`
	Pending    int `json:"pending"`     // pending_key, needs_review, or never sent to the brain
	Unsupport  int `json:"unsupported"` // brain says it cannot auto-grade this question
	Unanswered int `json:"unanswered"`
	Recorded   int `json:"recorded"` // items the brain confirmed as recorded
}

// Client is the short client description kept with an attempt.
type Client struct {
	UA string `json:"ua,omitempty"`
	IP string `json:"ip,omitempty"`
}

// Record is one archived attempt.
type Record struct {
	V               int       `json:"v"` // 2: per-item recorded/attempt_id (v1 had one brain_attempt_id)
	ClientAttemptID string    `json:"client_attempt_id"`
	SetID           string    `json:"set_id"`
	SetTitle        string    `json:"set_title"`
	Source          string    `json:"source"`
	SubmittedAt     time.Time `json:"submitted_at"`
	Mode            string    `json:"mode"` // "graded" (the brain replied) or "archive_only"
	// BrainNote says why the brain was not used: not_configured | not_ready |
	// unavailable | not_sent | test_set | daily_cap (the brain refused the batch: 429).
	BrainNote string `json:"brain_note,omitempty"`
	// Sig fingerprints the submitted answers (+revs), so the same
	// client_attempt_id with different answers is refused, not silently replayed.
	Sig string `json:"sig,omitempty"`
	// Test marks a submission of a local test set (never sent to the brain).
	// Absent for real sets, so their archive lines stay byte-identical.
	Test    bool    `json:"test,omitempty"`
	Items   []Item  `json:"items"`
	Summary Summary `json:"summary"`
	Client  Client  `json:"client"`

	// runtime-only (not archived)
	Archived bool   `json:"-"`
	Notify   string `json:"-"`
}

// Store appends records. Safe for concurrent use.
type Store struct {
	dir string
	log *slog.Logger

	mu       sync.Mutex
	f        *os.File
	errShown bool
}

// Open prepares a Store. It never fails: an unusable directory shows up as
// Append errors (and one error log line), not as a startup failure.
func Open(dir string, log *slog.Logger) *Store { return &Store{dir: dir, log: log} }

func (s *Store) open() error {
	if s.f != nil {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, FileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.f = f
	return nil
}

// Append writes one line and syncs it. The error text is generic on purpose.
func (s *Store) Append(r *Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return errors.New("encode failed")
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.open()
	if err == nil {
		if _, err = s.f.Write(line); err == nil {
			err = s.f.Sync()
		}
	}
	if err != nil {
		if s.f != nil {
			s.f.Close()
			s.f = nil
		}
		if !s.errShown && s.log != nil { // once, no content, no path
			s.log.Error("attempts archive is not writable; results are still returned (archived=false)")
			s.errShown = true
		}
		return errors.New("archive not writable")
	}
	s.errShown = false
	return nil
}

// Load returns the archived records (oldest first, at most maxRecords of the
// newest), used to answer repeated client_attempt_ids after a restart.
func (s *Store) Load(maxRecords int) []*Record {
	f, err := os.Open(filepath.Join(s.dir, FileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > maxLoadSize {
		return nil
	}
	var out []*Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ClientAttemptID != "" {
			r.Archived, r.Notify = true, "unknown"
			out = append(out, &r)
		}
	}
	if len(out) > maxRecords {
		out = out[len(out)-maxRecords:]
	}
	return out
}

// Close releases the file.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
}
