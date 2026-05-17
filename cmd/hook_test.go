package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

func TestHookFilesClaudePayload(t *testing.T) {
	files, err := hookFiles([]byte(`{"tool_input":{"file_path":"src/auth.go"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/auth.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesCodexCamelCasePayload(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"hook_event_name": "PostToolUse",
		"toolName": "apply_patch",
		"toolInput": {
			"absolute_file_path": "/repo/internal/hook.go"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/repo/internal/hook.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesApplyPatchPayload(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"toolName": "apply_patch",
		"arguments": {
			"patch": "*** Begin Patch\n*** Update File: cmd/hook.go\n@@\n*** Add File: examples/codex-hooks.json\n+{}\n*** End Patch\n"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/hook.go", "examples/codex-hooks.json"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesStringifiedArguments(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"toolName": "apply_patch",
		"arguments": "{\"patch\":\"*** Begin Patch\\n*** Delete File: old.go\\n*** End Patch\\n\"}"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"old.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestForwardWriteReachesDaemon(t *testing.T) {
	h := &captureHandler{writes: make(chan string, 1)}
	sock := shortSockPath(t)
	state := daemon.NewState()
	if cwd, err := os.Getwd(); err == nil {
		if anchor, ok := daemon.ResolveAnchor(cwd); ok {
			state.SetSessionWorktree(anchor.Worktree)
		}
	}
	registry := daemon.NewRegistry(state, nil)
	srv, err := daemon.Listen(sock, registry, h)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer srv.Close()
	go func() {
		_ = srv.Serve(ctx)
	}()
	t.Setenv("HUD_SOCK", sock)

	if err := forward(ipc.TypeWrite, ipc.WriteData{File: "cmd/hook.go"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-h.writes:
		if got != "cmd/hook.go" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not receive write")
	}
}

func TestForwardWriteRoutesAbsoluteWorktreeFile(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	h := &worktreeCaptureHandler{writes: make(chan routedWrite, 1)}
	sock := shortSockPath(t)
	state := daemon.NewState()
	state.SetSessionWorktree(trunk)
	registry := daemon.NewRegistry(state, func(anchor daemon.SessionAnchor) (*daemon.State, error) {
		s := daemon.NewState()
		s.SetSessionWorktree(anchor.Worktree)
		s.SetSessionBaseRef(anchor.BaseRef)
		return s, nil
	})
	srv, err := daemon.Listen(sock, registry, h)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer srv.Close()
	go func() {
		_ = srv.Serve(ctx)
	}()
	t.Setenv("HUD_SOCK", sock)
	t.Chdir(trunk)

	file := filepath.Join(wt, "cmd", "hook.go")
	if err := forwardWrite(file); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-h.writes:
		if got.worktree != wt {
			t.Fatalf("write routed to %q, want %q", got.worktree, wt)
		}
		if got.file != file {
			t.Fatalf("write file = %q, want %q", got.file, file)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not receive write")
	}
}

func TestHookRouteCWDUsesNearestExistingParent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new", "nested", "file.go")
	if got := hookRouteCWD(file); got != dir {
		t.Fatalf("hookRouteCWD(%q) = %q, want %q", file, got, dir)
	}
}

type captureHandler struct {
	writes chan string
}

func (h *captureHandler) OnWrite(_ *daemon.State, file string) { h.writes <- file }
func (h *captureHandler) OnGoal(_ *daemon.State, _ string)     {}

type routedWrite struct {
	worktree string
	file     string
}

type worktreeCaptureHandler struct {
	writes chan routedWrite
}

func (h *worktreeCaptureHandler) OnWrite(s *daemon.State, file string) {
	h.writes <- routedWrite{worktree: s.SessionWorktree(), file: file}
}
func (h *worktreeCaptureHandler) OnGoal(_ *daemon.State, _ string) {}

// shortSockPath returns a temp socket path that fits within the macOS
// sun_path limit (~104 bytes), which t.TempDir() can exceed when the
// test name is long.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "huds-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "hud.sock")
}

func testRepoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	trunk := t.TempDir()
	testGit(t, trunk, "init", "-q", "-b", "main")
	testGit(t, trunk, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	testGit(t, trunk, "worktree", "add", "-q", wt)
	return canonicalForTest(t, trunk), canonicalForTest(t, wt)
}

func canonicalForTest(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
