package ipc

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSocketPathFor_WorktreeSharesTrunkFingerprint pins the contract that a
// linked git worktree resolves to the same daemon socket as its trunk.
// Regression guard for the case where `hud start` runs in the trunk while
// the agent (and its MCP server) operates in a worktree: previously the
// fingerprint was derived from `--show-toplevel`, which differs per
// worktree and stranded worktree-side clients.
func TestSocketPathFor_WorktreeSharesTrunkFingerprint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("HUD_SOCK", "")

	trunk := t.TempDir()
	mustGit(t, trunk, "init", "-q", "-b", "main")
	mustGit(t, trunk, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(trunk, "wt")
	mustGit(t, trunk, "worktree", "add", "-q", wt)

	trunkSock, err := SocketPathFor(trunk)
	if err != nil {
		t.Fatalf("trunk SocketPathFor: %v", err)
	}
	wtSock, err := SocketPathFor(wt)
	if err != nil {
		t.Fatalf("worktree SocketPathFor: %v", err)
	}
	if trunkSock != wtSock {
		t.Fatalf("trunk and worktree must share socket\n  trunk: %s\n  wt:    %s", trunkSock, wtSock)
	}

	// Sanity: a sibling repo with a separate .git must NOT collide.
	other := t.TempDir()
	mustGit(t, other, "init", "-q", "-b", "main")
	mustGit(t, other, "commit", "--allow-empty", "-q", "-m", "init")
	otherSock, err := SocketPathFor(other)
	if err != nil {
		t.Fatalf("other SocketPathFor: %v", err)
	}
	if otherSock == trunkSock {
		t.Fatalf("unrelated repos must not share a fingerprint: %s", otherSock)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
