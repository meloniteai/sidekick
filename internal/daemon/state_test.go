package daemon

import (
	"testing"

	"github.com/uriahlevy/hud/internal/ipc"
)

func TestSnapshotOverallDistanceExcludesDisabledVerifiers(t *testing.T) {
	state := NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "On", Distance: 0.2})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Off", Distance: 1.0, Disabled: true})

	snap := state.Snapshot()
	if got, want := snap.OverallDistance, 0.2; got != want {
		t.Fatalf("overall distance: got %v, want %v", got, want)
	}
}

func TestSnapshotSurfacesRunningVerifiers(t *testing.T) {
	state := NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Idle", Distance: 0.3})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Busy", Distance: 0.7, Running: true})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "OffButRunning", Distance: 1.0, Running: true, Disabled: true})

	snap := state.Snapshot()
	if !snap.AnyRunning {
		t.Fatal("any_running: got false, want true")
	}
	if got, want := snap.RunningVerifiers, []string{"Busy"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("running_verifiers: got %v, want %v", got, want)
	}
}

func TestSnapshotNoRunningVerifiers(t *testing.T) {
	state := NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Idle", Distance: 0.3})

	snap := state.Snapshot()
	if snap.AnyRunning {
		t.Fatal("any_running: got true, want false")
	}
	if snap.RunningVerifiers != nil {
		t.Fatalf("running_verifiers: got %v, want nil", snap.RunningVerifiers)
	}
}

func TestRecordEditTracksUniqueFilesInOrder(t *testing.T) {
	state := NewState()
	state.RecordEdit("a.go")
	state.RecordEdit("b.go")
	state.RecordEdit("a.go") // duplicate
	state.RecordEdit("")     // dropped
	state.RecordEdit("c.go")
	got := state.SessionEdits()
	want := []string{"a.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("session edits: got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Fatalf("session edits[%d]: got %q, want %q", i, got[i], p)
		}
	}
}

func TestSessionEditsReturnsCopy(t *testing.T) {
	state := NewState()
	state.RecordEdit("a.go")
	got := state.SessionEdits()
	got[0] = "mutated"
	if again := state.SessionEdits(); again[0] != "a.go" {
		t.Fatalf("mutating returned slice leaked into state: %v", again)
	}
}

func TestLockGoalIgnoresSubsequentSetGoal(t *testing.T) {
	state := NewState()
	state.LockGoal("ship oauth")
	if got := state.Goal(); got != "ship oauth" {
		t.Fatalf("locked goal: got %q, want %q", got, "ship oauth")
	}
	if !state.GoalLocked() {
		t.Fatal("GoalLocked: got false after LockGoal")
	}
	state.SetGoal("agent-supplied detour")
	if got := state.Goal(); got != "ship oauth" {
		t.Fatalf("post-SetGoal: got %q, want locked goal %q", got, "ship oauth")
	}
	if snap := state.Snapshot(); !snap.GoalLocked {
		t.Fatal("Snapshot.GoalLocked: got false, want true")
	}
}

func TestSetGoalWorksWhenUnlocked(t *testing.T) {
	state := NewState()
	state.SetGoal("first")
	state.SetGoal("second")
	if got := state.Goal(); got != "second" {
		t.Fatalf("unlocked SetGoal: got %q, want %q", got, "second")
	}
	if state.GoalLocked() {
		t.Fatal("unlocked GoalLocked: got true, want false")
	}
}

func TestToggleVerifierDisabledKeepsVerifierVisible(t *testing.T) {
	state := NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Distance: 0.4, Reason: "ok", Running: true})

	disabled, ok := state.ToggleVerifierDisabled("Architect")
	if !ok || !disabled {
		t.Fatalf("toggle off: disabled=%v ok=%v, want true true", disabled, ok)
	}
	off, ok := state.Verifier("Architect")
	if !ok {
		t.Fatal("verifier should still exist after disabling")
	}
	if !off.Disabled || off.Running || off.Reason != "disabled" {
		t.Fatalf("disabled state not reflected: %+v", off)
	}

	disabled, ok = state.ToggleVerifierDisabled("Architect")
	if !ok || disabled {
		t.Fatalf("toggle on: disabled=%v ok=%v, want false true", disabled, ok)
	}
	on, _ := state.Verifier("Architect")
	if on.Disabled || on.Reason != "awaiting next run" {
		t.Fatalf("enabled state not reflected: %+v", on)
	}
}
