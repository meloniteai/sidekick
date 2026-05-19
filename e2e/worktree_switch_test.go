//go:build e2e

package e2e

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

// TestWorktreeSwitchNoContention drives the daemon over its raw IPC socket
// from two distinct cwds — a trunk repo and one of its linked worktrees —
// and verifies that goals, verifier triggering, and status responses stay
// strictly isolated per worktree. Uses direct socket messaging so the test
// doesn't depend on shelling out per request, and confirms the registry
// routes by Request.Cwd rather than process cwd.
func TestWorktreeSwitchNoContention(t *testing.T) {
	trunk, wt := ScratchRepoWithWorktree(t)

	// Identical verifier in both worktrees so each session has one to fire.
	// `quiet_period: 50ms` keeps the test snappy; the runner debounces
	// bursts of writes within that window.
	yaml := `goal_source: prompt
quiet_period: 50ms
verifiers:
  - name: Echo
    type: command
    direction: N
    timeout: 5s
    command: ["sh", "-c", "printf '{\"distance\":0.2,\"reason\":\"hit\"}\n'"]
`
	WriteSidekickYAML(t, trunk, yaml)
	WriteSidekickYAML(t, wt, yaml)

	d := StartDaemon(t, trunk)

	// Set goals via direct IPC, one per worktree. The daemon creates a new
	// session lazily on the first request from a cwd it hasn't seen.
	setGoal(t, d, trunk, "goal-A")
	setGoal(t, d, wt, "goal-B")

	// Both sessions should now exist with their respective goals.
	if got := d.StatusFrom(t, trunk).Goal; got != "goal-A" {
		t.Fatalf("trunk goal = %q, want goal-A", got)
	}
	if got := d.StatusFrom(t, wt).Goal; got != "goal-B" {
		t.Fatalf("wt goal = %q, want goal-B", got)
	}

	// Status from trunk lists both sessions (the daemon surfaces a session
	// roster in StatusReply.Sessions so the TUI can render a switcher).
	st := d.StatusFrom(t, trunk)
	if got := len(st.Sessions); got < 2 {
		t.Fatalf("expected at least 2 sessions in roster, got %d: %+v", got, st.Sessions)
	}

	// Fire a write only against trunk. Trunk's verifier must run; wt's
	// verifier must stay at its initial (never-run) state.
	sendWrite(t, d, trunk, filepath.Join(trunk, "x.go"))
	d.waitForEchoIn(t, trunk, 5*time.Second)

	wtV := echoStatus(t, d, wt)
	if !wtV.ComputedAt.IsZero() {
		t.Fatalf("wt verifier ran from a trunk-targeted hook: %+v", wtV)
	}

	// Now fire a write against the worktree. Its verifier must run.
	sendWrite(t, d, wt, filepath.Join(wt, "y.go"))
	d.waitForEchoIn(t, wt, 5*time.Second)

	// Goals must still be intact after the writes — no cross-talk.
	if got := d.StatusFrom(t, trunk).Goal; got != "goal-A" {
		t.Fatalf("trunk goal mutated to %q after writes", got)
	}
	if got := d.StatusFrom(t, wt).Goal; got != "goal-B" {
		t.Fatalf("wt goal mutated to %q after writes", got)
	}
}

func setGoal(t *testing.T, d *Daemon, cwd, goal string) {
	t.Helper()
	data, err := json.Marshal(ipc.GoalData{Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	d.SendIPC(t, ipc.Request{Type: ipc.TypeGoal, Cwd: cwd, Data: data})
}

func sendWrite(t *testing.T, d *Daemon, cwd, file string) {
	t.Helper()
	data, err := json.Marshal(ipc.WriteData{File: file})
	if err != nil {
		t.Fatal(err)
	}
	d.SendIPC(t, ipc.Request{Type: ipc.TypeWrite, Cwd: cwd, Data: data})
}

func echoStatus(t *testing.T, d *Daemon, cwd string) ipc.VerifierStatus {
	t.Helper()
	for _, v := range d.StatusFrom(t, cwd).Verifiers {
		if v.Name == "Echo" {
			return v
		}
	}
	t.Fatalf("Echo verifier missing from status for %s", cwd)
	return ipc.VerifierStatus{}
}

func (d *Daemon) waitForEchoIn(t *testing.T, cwd string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v := echoStatus(t, d, cwd)
		if !v.Running && !v.ComputedAt.IsZero() && v.Reason == "hit" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Echo did not complete in %s for %s", timeout, cwd)
}
