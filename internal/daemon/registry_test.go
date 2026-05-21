package daemon

import (
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

func TestRegistryRoutesLinkedWorktreesToDistinctSessions(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.SetSessionBaseRef(testGitOut(t, trunk, "rev-parse", "HEAD"))

	var created int
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		created++
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		s.SetSessionBaseRef(anchor.BaseRef)
		s.UpsertVerifier(ipc.VerifierStatus{Name: "OnlyWT", Distance: 0.2})
		return s, nil
	})

	trunkSession, err := reg.SessionForCWD(trunk)
	if err != nil {
		t.Fatal(err)
	}
	wtSession, err := reg.SessionForCWD(wt)
	if err != nil {
		t.Fatal(err)
	}
	if trunkSession == wtSession {
		t.Fatal("linked worktree should route to a distinct session inside the shared daemon")
	}
	if created != 1 {
		t.Fatalf("factory calls = %d, want 1", created)
	}
	if wtSession.SessionWorktree() != wt {
		t.Fatalf("worktree = %q, want %q", wtSession.SessionWorktree(), wt)
	}
}

func TestRegistryLazySessionsKeepIndependentGoalAndBase(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.SetSessionBaseRef(testGitOut(t, trunk, "rev-parse", "HEAD"))
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		s.SetSessionBaseRef(anchor.BaseRef)
		return s, nil
	})

	trunkSession, err := reg.GoalSessionForCWD(trunk)
	if err != nil {
		t.Fatal(err)
	}
	trunkSession.SetGoal("trunk goal")
	wtSession, err := reg.GoalSessionForCWD(wt)
	if err != nil {
		t.Fatal(err)
	}
	wtSession.SetGoal("wt goal")

	if got := trunkSession.Goal(); got != "trunk goal" {
		t.Fatalf("trunk goal = %q", got)
	}
	if got := wtSession.Goal(); got != "wt goal" {
		t.Fatalf("worktree goal = %q", got)
	}
	if trunkSession.SessionBaseRef() == "" || wtSession.SessionBaseRef() == "" {
		t.Fatalf("base refs must be seeded: trunk=%q wt=%q", trunkSession.SessionBaseRef(), wtSession.SessionBaseRef())
	}
}

func TestRegistryEmptyOrInvalidCWDFallsBackToDefault(t *testing.T) {
	defaultState := NewState()
	defaultState.SetSessionWorktree("/repo/default")
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		t.Fatalf("factory should not run for invalid cwd: %+v", anchor)
		return nil, nil
	})

	for _, cwd := range []string{"", t.TempDir()} {
		got, err := reg.SessionForCWD(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if got != defaultState {
			t.Fatalf("cwd %q routed to %p, want default %p", cwd, got, defaultState)
		}
	}
}

func TestRegistryIdleGCRemovesNonDefaultAndCleansUp(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	var cleaned bool
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		return s, nil
	})
	reg.SetCleanup(func(*State) { cleaned = true })
	reg.SetIdleTimeout(time.Minute)
	if _, err := reg.SessionForCWD(wt); err != nil {
		t.Fatal(err)
	}
	reg.SetLastActivityForTest(wt, time.Now().Add(-2*time.Minute))

	removed := reg.CollectIdle(time.Now())
	if len(removed) != 1 {
		t.Fatalf("removed = %v, want one worktree", removed)
	}
	if !cleaned {
		t.Fatal("cleanup should run for removed session")
	}
	if len(reg.Sessions()) != 1 {
		t.Fatalf("session count after GC = %d, want 1", len(reg.Sessions()))
	}
}

func TestRegistryFirstGoalFixesDisplayedSessionUntilUserSwitch(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		return s, nil
	})

	wtSession, err := reg.GoalSessionForCWD(wt)
	if err != nil {
		t.Fatal(err)
	}
	if reg.DisplayedSession() != wtSession {
		t.Fatal("first goal should select its worktree")
	}
	trunkSession, err := reg.GoalSessionForCWD(trunk)
	if err != nil {
		t.Fatal(err)
	}
	if reg.DisplayedSession() != wtSession {
		t.Fatal("later goal should not auto-switch displayed session")
	}
	if !reg.SwitchDisplayed(trunk) || reg.DisplayedSession() != trunkSession {
		t.Fatal("explicit user switch should change displayed session")
	}
}

func TestRegistryStartupGoalPreventsImplicitWorktreeSwitch(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.SetGoal("existing session")
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		return s, nil
	})

	if _, err := reg.GoalSessionForCWD(wt); err != nil {
		t.Fatal(err)
	}
	if reg.DisplayedSession() != defaultState {
		t.Fatal("worktree goal should not auto-switch after an existing startup goal")
	}
}

func TestRegistryUnsetLiteralStillAllowsInitialWorktreeSwitch(t *testing.T) {
	trunk, wt := testRepoWithWorktree(t)
	defaultState := NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.SetGoal("unset")
	reg := NewRegistry(defaultState, func(anchor SessionAnchor) (*State, error) {
		s := NewState()
		s.SetSessionWorktree(anchor.Worktree)
		return s, nil
	})

	wtSession, err := reg.GoalSessionForCWD(wt)
	if err != nil {
		t.Fatal(err)
	}
	if reg.DisplayedSession() != wtSession {
		t.Fatal("literal unset goal should still allow first worktree selection")
	}
}

func testRepoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	trunk := t.TempDir()
	testGit(t, trunk, "init", "-q", "-b", "main")
	testGit(t, trunk, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	testGit(t, trunk, "worktree", "add", "-q", wt)
	// macOS t.TempDir() returns paths under /var/folders, but git
	// resolves them through the /var -> /private/var symlink. Tests
	// assert that stored session worktrees match these paths, so
	// canonicalise here once and let every assertion see the same
	// form the factory will see.
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

func TestRegistryStartHeartbeatFires(t *testing.T) {
	defaultState := NewState()
	reg := NewRegistry(defaultState, nil)

	var ticks atomic.Int64
	reg.SetHeartbeat(func() {
		reg.EachSession(func(*State) { ticks.Add(1) })
	})

	go reg.StartHeartbeat(t.Context(), 20*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ticks.Load() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("heartbeat callback never fired from StartHeartbeat")
}

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func testGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}
