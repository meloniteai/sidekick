package gitstats

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNumstatLineTextFile(t *testing.T) {
	got, ok := parseNumstatLine("12\t3\tinternal/foo.go")
	if !ok {
		t.Fatal("expected ok parse")
	}
	if got.Path != "internal/foo.go" || got.Added != 12 || got.Removed != 3 || got.Binary {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestParseNumstatLineBinary(t *testing.T) {
	got, ok := parseNumstatLine("-\t-\tassets/logo.png")
	if !ok {
		t.Fatal("expected ok parse for binary file")
	}
	if !got.Binary || got.Path != "assets/logo.png" {
		t.Fatalf("expected binary, got %+v", got)
	}
	if got.Added != 0 || got.Removed != 0 {
		t.Fatalf("binary should have zero counts: %+v", got)
	}
}

func TestParseNumstatLineRejectsGarbage(t *testing.T) {
	for _, line := range []string{
		"",
		"only one field",
		"two\tfields",
		"abc\tdef\tfile.go",
	} {
		if _, ok := parseNumstatLine(line); ok {
			t.Errorf("expected reject for %q", line)
		}
	}
}

func TestFetchUsesSessionWorktreeNotProcessCWD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	trunk := t.TempDir()
	git(t, trunk, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(trunk, "trunk.txt"), "base\n")
	git(t, trunk, "add", "trunk.txt")
	git(t, trunk, "commit", "-q", "-m", "init")
	base := strings.TrimSpace(gitOut(t, trunk, "rev-parse", "HEAD"))

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, trunk, "worktree", "add", "-q", wt)
	writeFile(t, filepath.Join(wt, "trunk.txt"), "base\nsession\n")

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(trunk); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)

	ws := Fetch(context.Background(), wt, base, nil)
	if ws.WorktreeName != filepath.Base(wt) {
		t.Fatalf("worktree name = %q, want %q", ws.WorktreeName, filepath.Base(wt))
	}
	if len(ws.Files) != 1 || ws.Files[0].Path != "trunk.txt" {
		t.Fatalf("files = %+v, want trunk.txt from session worktree", ws.Files)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
