package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSocketPathFor_WorktreeSharesTrunkFingerprint pins the contract that a
// linked git worktree resolves to the same daemon socket as its trunk.
// Regression guard for the case where `sidekick start` runs in the trunk while
// the agent (and its MCP server) operates in a worktree: previously the
// fingerprint was derived from `--show-toplevel`, which differs per
// worktree and stranded worktree-side clients.
func TestSocketPathFor_WorktreeSharesTrunkFingerprint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("SIDEKICK_SOCK", "")

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

func TestSocketPathFor_CloneFallsBackToLiveSocketWithSameOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("SIDEKICK_SOCK", "")

	remote := "git@github-melonite:meloniteai/sidekick-ui.git"
	trunk := t.TempDir()
	mustGit(t, trunk, "init", "-q", "-b", "main")
	mustGit(t, trunk, "commit", "--allow-empty", "-q", "-m", "init")
	mustGit(t, trunk, "remote", "add", "origin", remote)

	clone := t.TempDir()
	mustGit(t, clone, "init", "-q", "-b", "main")
	mustGit(t, clone, "commit", "--allow-empty", "-q", "-m", "init")
	mustGit(t, clone, "remote", "add", "origin", remote)

	home, err := os.MkdirTemp("/tmp", "sidekick-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)

	trunkSock, err := SocketPathFor(trunk)
	if err != nil {
		t.Fatalf("trunk SocketPathFor: %v", err)
	}
	cloneSock, err := SocketPathFor(clone)
	if err != nil {
		t.Fatalf("clone SocketPathFor: %v", err)
	}
	if trunkSock == cloneSock {
		t.Fatalf("independent clones should have distinct primary sockets: %s", trunkSock)
	}

	if err := os.MkdirAll(filepath.Dir(trunkSock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", trunkSock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	errCh := make(chan error, 1)
	go serveStatusOnce(l, trunk, errCh)

	got, err := SocketPathFor(clone)
	if err != nil {
		t.Fatalf("clone fallback SocketPathFor: %v", err)
	}
	if got != trunkSock {
		t.Fatalf("clone socket = %s, want live same-origin socket %s", got, trunkSock)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
}

func TestRepoProjectIdentityPrefersGitHubRepositoryEnv(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "meloniteai/sidekick-ui")

	a := RepoProjectIdentity(filepath.Join(t.TempDir(), "work"))
	b := RepoProjectIdentity(filepath.Join(t.TempDir(), "checkout"))

	if a.Name != "meloniteai/sidekick-ui" {
		t.Fatalf("project name = %q, want repository fqn", a.Name)
	}
	if a.Fingerprint == "" {
		t.Fatal("project fingerprint is empty")
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("same GITHUB_REPOSITORY must be sticky across checkout dirs: %q vs %q", a.Fingerprint, b.Fingerprint)
	}
}

func TestRepoProjectIdentityUsesOriginSlugAcrossClones(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GITHUB_REPOSITORY", "")

	const remote = "git@github.com:meloniteai/sidekick-ui.git"
	a := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, a, "init", "-q", "-b", "main")
	mustGit(t, a, "remote", "add", "origin", remote)

	b := filepath.Join(t.TempDir(), "factory-checkout")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, b, "init", "-q", "-b", "main")
	mustGit(t, b, "remote", "add", "origin", remote)

	ai := RepoProjectIdentity(a)
	bi := RepoProjectIdentity(b)
	if ai.Name != "meloniteai/sidekick-ui" || bi.Name != "meloniteai/sidekick-ui" {
		t.Fatalf("project names = %q/%q, want repository fqn", ai.Name, bi.Name)
	}
	if ai.Fingerprint == "" || ai.Fingerprint != bi.Fingerprint {
		t.Fatalf("same origin must be sticky across clones: %q vs %q", ai.Fingerprint, bi.Fingerprint)
	}
}

func TestRepoSlugFromRemoteURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/meloniteai/sidekick-ui.git":    "meloniteai/sidekick-ui",
		"git@github-melonite:meloniteai/sidekick-ui.git":   "meloniteai/sidekick-ui",
		"ssh://git@github.example.com/meloniteai/Sidekick": "meloniteai/Sidekick",
		"/tmp/local/sidekick-ui.git":                       "",
		"../local/sidekick-ui.git":                         "",
	}
	for raw, want := range cases {
		if got := repoSlugFromRemoteURL(raw); got != want {
			t.Fatalf("repoSlugFromRemoteURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func serveStatusOnce(l net.Listener, worktree string, errCh chan<- error) {
	conn, err := l.Accept()
	if err != nil {
		errCh <- err
		return
	}
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		errCh <- err
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		errCh <- err
		return
	}
	if req.Type != TypeStatus {
		errCh <- nil
		return
	}
	data, err := json.Marshal(StatusReply{
		Worktree:          worktree,
		DisplayedWorktree: worktree,
		Sessions:          []SessionSummary{{Worktree: worktree}},
	})
	if err != nil {
		errCh <- err
		return
	}
	errCh <- json.NewEncoder(conn).Encode(Response{OK: true, Data: data})
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestSendFromStampsRequestCWD(t *testing.T) {
	// Short filename — macOS caps unix socket paths at 104 bytes and the
	// default $TMPDIR + t.TempDir() prefix already burns ~95.
	sock := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("SIDEKICK_SOCK", sock)

	gotCh := make(chan Request, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			errCh <- err
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			errCh <- err
			return
		}
		gotCh <- req
		_ = json.NewEncoder(conn).Encode(Response{OK: true})
	}()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SendFrom(Request{Type: TypeStatus}, cwd); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-gotCh:
		if got.Cwd != cwd {
			t.Fatalf("request cwd = %q, want %q", got.Cwd, cwd)
		}
	case err := <-errCh:
		t.Fatal(err)
	}
}
