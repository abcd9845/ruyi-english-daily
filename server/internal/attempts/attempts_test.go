package attempts

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func rec(id string) *Record {
	return &Record{V: 1, ClientAttemptID: id, SetID: "s", Mode: "archive_only", Items: []Item{{QRef: "q", Status: "pending_key"}}}
}

func TestAppendLoadAndModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	s := Open(dir, nil)
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(rec(strings.Repeat("x", 8) + string(rune('a'+i)))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dir %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Join(dir, FileName)); st.Mode().Perm() != 0o600 {
		t.Errorf("file %v", st.Mode())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if bytes.Count(raw, []byte("\n")) != 20 {
		t.Errorf("lines: %d", bytes.Count(raw, []byte("\n")))
	}
	if got := s.Load(100); len(got) != 20 {
		t.Errorf("load: %d", len(got))
	}
	if got := s.Load(5); len(got) != 5 {
		t.Errorf("load cap: %d", len(got))
	}
	// garbage and torn lines are skipped
	f, _ := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not json\n{\"client_attempt_id\":\n")
	f.Close()
	if got := s.Load(100); len(got) != 20 {
		t.Errorf("load with garbage: %d", len(got))
	}
}

func TestUnwritableLogsOnceWithoutContent(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(blocker, nil, 0o600)
	var buf bytes.Buffer
	s := Open(filepath.Join(blocker, "d"), slog.New(slog.NewTextHandler(&buf, nil)))
	for i := 0; i < 3; i++ {
		if err := s.Append(rec("abcdefgh")); err == nil || strings.Contains(err.Error(), blocker) {
			t.Errorf("want a generic error, got %v", err)
		}
	}
	if strings.Count(buf.String(), "not writable") != 1 || strings.Contains(buf.String(), blocker) {
		t.Errorf("log:\n%s", buf.String())
	}
}

// Test is only archived for local test sets: a real attempt's line has no "test" key.
func TestTestFlagOnlyArchivedWhenSet(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, nil)
	defer s.Close()
	r1, r2 := rec("aaaaaaaa"), rec("bbbbbbbb")
	r2.Test = true
	if err := s.Append(r1); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(r2); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if strings.Contains(lines[0], `"test"`) || !strings.Contains(lines[1], `"test":true`) {
		t.Errorf("lines: %v", lines)
	}
	got := s.Load(10)
	if len(got) != 2 || got[0].Test || !got[1].Test {
		t.Errorf("load: %+v", got)
	}
}
