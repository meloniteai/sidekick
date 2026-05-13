package cmd

import (
	"context"
	"testing"
	"time"

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
	h := &runnerHandler{state: state, runner: r}

	h.OnGoal("ship without eager verifier runs", "", "")
	time.Sleep(100 * time.Millisecond)

	if got := state.Goal(); got != "ship without eager verifier runs" {
		t.Fatalf("goal: got %q", got)
	}
	s, _ := state.Verifier("Counter")
	if !s.ComputedAt.IsZero() || s.Reason != "awaiting first run" {
		t.Fatalf("goal set should not compute verifier; computed=%s reason=%q", s.ComputedAt, s.Reason)
	}

	h.OnWrite("cmd/start.go")
	if !waitForStartTest(2*time.Second, func() bool {
		s, _ := state.Verifier("Counter")
		return !s.Running && s.Reason == "ok"
	}) {
		s, _ := state.Verifier("Counter")
		t.Fatalf("write should compute verifier; running=%v reason=%q", s.Running, s.Reason)
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

func pickerSampleVerifiers(names ...string) []verifier.Verifier {
	out := make([]verifier.Verifier, len(names))
	for i, n := range names {
		out[i] = verifier.Verifier{Name: n, Direction: "N", Command: []string{"true"}}
	}
	return out
}

func verifierNamesOf(vs []verifier.Verifier) []string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Name
	}
	return names
}

func TestFilterPickerSelectionPreservesAvailableOrder(t *testing.T) {
	available := pickerSampleVerifiers("Architect", "Test", "Security", "Deployment")
	// Caller scrambles the names; the helper must follow available's order.
	selected := []string{"Deployment", "Architect", "Security"}

	got := verifierNamesOf(filterPickerSelection(available, selected))
	want := []string{"Architect", "Security", "Deployment"}
	if !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestFilterPickerSelectionFullSet(t *testing.T) {
	available := pickerSampleVerifiers("A", "B", "C")
	got := filterPickerSelection(available, []string{"A", "B", "C"})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
}

func TestFilterPickerSelectionEmpty(t *testing.T) {
	available := pickerSampleVerifiers("A", "B")
	got := filterPickerSelection(available, nil)
	if len(got) != 0 {
		t.Fatalf("empty selection should yield zero verifiers, got %d", len(got))
	}
}

func TestFilterPickerSelectionDropsUnknownAndDeduplicates(t *testing.T) {
	available := pickerSampleVerifiers("A", "B")
	// Unknown name ("Z") must be dropped; duplicates must not double-emit.
	got := verifierNamesOf(filterPickerSelection(available, []string{"B", "Z", "B"}))
	want := []string{"B"}
	if !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
