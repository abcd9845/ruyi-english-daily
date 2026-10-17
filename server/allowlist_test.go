package ruyisite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The committed allowlist must equal what `make allowlist` would generate from
// `git ls-files`, so a new tracked file can never be served (or a removed one
// kept) without an explicit, reviewable change to static-allowlist.txt.
func TestAllowlistMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git checkout")
	}
	out, err := exec.Command("sh", "scripts/gen-allowlist.sh", strings.TrimSpace(string(root))).Output()
	if err != nil {
		t.Fatalf("gen-allowlist.sh: %v", err)
	}
	want := string(out)
	if StaticAllowlist != want {
		t.Fatalf("static-allowlist.txt is out of date; run `make allowlist` in server/.\n--- committed ---\n%s\n--- generated ---\n%s", StaticAllowlist, want)
	}
}

func TestAllowlistContentSanity(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git checkout")
	}
	lines := 0
	for _, l := range strings.Split(StaticAllowlist, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		lines++
		for _, bad := range []string{"server.log", "server.pid", ".sh", ".git/", ".env", "server/", "docs/"} {
			if strings.Contains(l, bad) {
				t.Errorf("allowlist contains forbidden entry %q", l)
			}
		}
		if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(root)), l)); err != nil {
			t.Errorf("allowlisted file missing in repo: %s", l)
		}
	}
	if lines < 17 {
		t.Errorf("allowlist unexpectedly small: %d", lines)
	}
}
