package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/verifier"
)

func TestRunnerHandlerGoalDoesNotTriggerVerifiers(t *testing.T) {
	state := daemon.NewState()
	v := verifier.Verifier{
		Name:      "Counter",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := verifier.NewRunner(context.Background(), state, []verifier.Verifier{v})
	defer r.Stop()
	runtimes := newSessionRuntimeManager(context.Background(), "test", "")
	runtimes.Register(state, r, "")
	h := &runnerHandler{runtimes: runtimes}

	h.OnGoal(state, "ship without eager verifier runs")
	time.Sleep(100 * time.Millisecond)

	if got := state.Goal(); got != "ship without eager verifier runs" {
		t.Fatalf("goal: got %q", got)
	}
	s, _ := state.Verifier("Counter")
	if !s.ComputedAt.IsZero() || s.Reason != "awaiting first run" {
		t.Fatalf("goal set should not compute verifier; computed=%s reason=%q", s.ComputedAt, s.Reason)
	}

	h.OnWrite(state, "cmd/start.go")
	if !waitForStartTest(2*time.Second, func() bool {
		s, _ := state.Verifier("Counter")
		return !s.Running && s.Reason == "ok"
	}) {
		s, _ := state.Verifier("Counter")
		t.Fatalf("write should compute verifier; running=%v reason=%q", s.Running, s.Reason)
	}
}

func TestRunnerHandlerLockedGoalSurvivesOnGoal(t *testing.T) {
	state := daemon.NewState()
	state.LockGoal("operator-pinned goal")
	runtimes := newSessionRuntimeManager(context.Background(), "test", "")
	h := &runnerHandler{runtimes: runtimes}

	h.OnGoal(state, "agent override")

	if got := state.Goal(); got != "operator-pinned goal" {
		t.Fatalf("goal: got %q, want locked value", got)
	}
	if !state.GoalLocked() {
		t.Fatal("GoalLocked: got false after agent override attempt")
	}
}

// TestMirrorDisabledToConfig writes each landing-chosen Disabled flag back
// to hud.yaml so the persisted file reflects the active session. A row the
// user re-enabled at landing should flip from disabled:true to disabled:false
// on disk; a row they disabled should flip the other way.
func TestMirrorDisabledToConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hud.yaml")
	if err := os.WriteFile(path, []byte(`goal_source: prompt
verifiers:
  - name: Architect
    type: command
    direction: N
    command: ["sh", "-c", "echo {}"]
  - name: Security
    type: command
    direction: S
    disabled: true
    command: ["sh", "-c", "echo {}"]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Landing flipped both rows: Architect now off, Security now on.
	verifiers := []verifier.Verifier{
		{Name: "Architect", Disabled: true},
		{Name: "Security", Disabled: false},
	}
	if err := mirrorDisabledToConfig(path, verifiers); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	f, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := map[string]bool{}
	for _, v := range f.Verifiers {
		got[v.Name] = v.Disabled
	}
	if !got["Architect"] || got["Security"] {
		t.Fatalf("yaml not mirrored: Architect=%v Security=%v, want true false", got["Architect"], got["Security"])
	}
}

// TestEnabledVerifiers covers the small filter helper used for the boot-time
// "[hud] enabled: ..." log. Disabled rows must be omitted so the operator
// sees what will actually run.
func TestEnabledVerifiers(t *testing.T) {
	vs := []verifier.Verifier{
		{Name: "A"},
		{Name: "B", Disabled: true},
		{Name: "C"},
	}
	got := enabledVerifiers(vs)
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "C" {
		names := make([]string, len(got))
		for i, v := range got {
			names[i] = v.Name
		}
		t.Fatalf("enabled = %v, want [A C]", names)
	}
}

func waitForStartTest(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
