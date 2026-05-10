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
