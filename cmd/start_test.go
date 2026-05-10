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

	h.OnGoal("ship without eager verifier runs")
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
