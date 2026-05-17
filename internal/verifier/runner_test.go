package verifier

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/daemon"
)

// waitFor polls until cond() returns true or the deadline fires. Returns
// false on timeout. Used to keep tests stable on slow CI without blanket
// sleeps that would mask real regressions.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestQuietPeriodCoalescesBurst pins the headline behavior: a burst of
// triggers inside the quiet window must collapse to one batch run, but not
// drop pending changes — once the window elapses, exactly one run fires.
func TestQuietPeriodCoalescesBurst(t *testing.T) {
	state := daemon.NewState()
	v := Verifier{
		Name:      "Counter",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.5,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{v})
	defer r.Stop()
	r.SetQuietPeriod(150 * time.Millisecond)

	// Count completed batches by watching ComputedAt advance.
	var calls atomic.Int32
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		var last time.Time
		for {
			select {
			case <-time.After(5 * time.Millisecond):
			}
			s, ok := state.Verifier("Counter")
			if !ok {
				continue
			}
			if !s.ComputedAt.Equal(last) && !s.ComputedAt.IsZero() && !s.Running {
				calls.Add(1)
				last = s.ComputedAt
			}
			if calls.Load() >= 2 {
				return
			}
		}
	}()

	// Fire a burst of triggers well inside the quiet window. The first
	// trigger schedules an immediate run; the rest must coalesce.
	for i := 0; i < 10; i++ {
		r.Trigger(fmt.Sprintf("file%d.go", i))
	}

	if !waitFor(500*time.Millisecond, func() bool { return calls.Load() >= 1 }) {
		t.Fatalf("expected first batch within 500ms; got %d calls", calls.Load())
	}
	first := calls.Load()
	// Keep triggering through the quiet window. The runner must NOT fire a
	// second time until quietPeriod elapses, but must NOT drop the edits.
	for i := 0; i < 10; i++ {
		r.Trigger(fmt.Sprintf("burst%d.go", i))
		time.Sleep(5 * time.Millisecond)
	}
	// Within the window: at most one extra run (quiet_period might just
	// have elapsed by the end of the burst).
	if got := calls.Load(); got > first+1 {
		t.Fatalf("expected at most one extra run inside quiet window, got %d (first=%d)", got, first)
	}
	// After the window elapses, the queued batch must fire.
	if !waitFor(800*time.Millisecond, func() bool { return calls.Load() >= first+1 }) {
		t.Fatalf("queued batch did not fire after quiet period; calls=%d first=%d", calls.Load(), first)
	}
	<-doneCh
}

// TestQuietPeriodChangesDuringBatchReschedule guarantees we don't lose
// edits that arrived while the previous batch was still running — the
// runner must reschedule once the in-flight batch completes.
func TestQuietPeriodChangesDuringBatchReschedule(t *testing.T) {
	state := daemon.NewState()
	// A slow verifier so we can land a Trigger while it's running.
	v := Verifier{
		Name:      "Slow",
		Direction: "N",
		Command:   []string{"sh", "-c", `sleep 0.2; printf '{"distance":0.5,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{v})
	defer r.Stop()
	// Quiet period > batch duration so there's a clean gap between batches
	// for the test to observe Running=false.
	r.SetQuietPeriod(500 * time.Millisecond)

	r.Trigger("a.go")
	if !waitFor(500*time.Millisecond, func() bool {
		s, _ := state.Verifier("Slow")
		return s.Running
	}) {
		t.Fatal("first batch never started")
	}
	// Mid-run trigger — must enqueue another batch for after the current one.
	r.Trigger("b.go")
	// Wait for the first batch to finish so we can capture its ComputedAt.
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return !s.Running && !s.ComputedAt.IsZero()
	}) {
		t.Fatal("first batch never finished")
	}
	first, _ := state.Verifier("Slow")
	// Now wait for the second batch to complete. With quietPeriod=500ms and
	// first batch starting at t≈0 and ending at t≈200ms, the second batch
	// starts at t≈500ms and finishes around t≈700ms.
	if !waitFor(3*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return !s.Running && s.ComputedAt.After(first.ComputedAt)
	}) {
		t.Fatal("second batch never ran; mid-run trigger was dropped")
	}
}

// TestKillBatchStopsRunningSubprocess verifies that KillBatch terminates an
// in-flight verifier subprocess promptly, leaves the runner usable for
// subsequent triggers, and labels the row "stopped" without overwriting the
// previously known distance.
func TestKillBatchStopsRunningSubprocess(t *testing.T) {
	state := daemon.NewState()
	// A verifier that would sleep for 10s if not killed.
	v := Verifier{
		Name:      "Slow",
		Direction: "N",
		Command:   []string{"sh", "-c", `sleep 10; printf '{"distance":0.1,"reason":"done"}\n'`},
		Timeout:   30 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{v})
	defer r.Stop()
	r.SetQuietPeriod(50 * time.Millisecond)

	// Seed a known distance so we can prove KillBatch preserves it.
	state.UpsertVerifier(state.Snapshot().Verifiers[0])
	cur, _ := state.Verifier("Slow")
	cur.Distance = 0.42
	state.UpsertVerifier(cur)

	r.Trigger("a.go")
	if !waitFor(time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return s.Running
	}) {
		t.Fatal("batch never started")
	}

	start := time.Now()
	r.KillBatch()

	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return !s.Running
	}) {
		t.Fatal("KillBatch did not terminate subprocess within 2s")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("KillBatch took too long: %s", elapsed)
	}

	s, _ := state.Verifier("Slow")
	if s.Reason != "stopped" {
		t.Errorf("expected Reason=%q, got %q", "stopped", s.Reason)
	}
	if s.Distance != 0.42 {
		t.Errorf("KillBatch should preserve last distance: got %v, want 0.42", s.Distance)
	}

	// Runner must remain usable: a fresh trigger schedules and runs a new batch.
	v2 := Verifier{
		Name:      "Slow",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r.verifiers = []Verifier{v2}
	r.Trigger("b.go")
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return !s.Running && s.Reason == "ok"
	}) {
		s, _ := state.Verifier("Slow")
		t.Fatalf("runner unusable after KillBatch; reason=%q running=%v", s.Reason, s.Running)
	}
}

func TestRunnerSkipsDisabledVerifiers(t *testing.T) {
	state := daemon.NewState()
	disabled := Verifier{
		Name:      "Disabled",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.1,"reason":"should not run"}\n'`},
		Timeout:   2 * time.Second,
	}
	enabled := Verifier{
		Name:      "Enabled",
		Direction: "S",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{disabled, enabled})
	defer r.Stop()
	state.SetVerifierDisabled("Disabled", true)

	r.Trigger("a.go")
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Enabled")
		return !s.Running && s.Reason == "ok"
	}) {
		s, _ := state.Verifier("Enabled")
		t.Fatalf("enabled verifier did not run; reason=%q running=%v", s.Reason, s.Running)
	}
	s, _ := state.Verifier("Disabled")
	if !s.Disabled {
		t.Fatal("disabled verifier lost disabled state")
	}
	if !s.ComputedAt.IsZero() {
		t.Fatalf("disabled verifier should not be computed, got %s", s.ComputedAt)
	}
	if s.Reason != "disabled" {
		t.Fatalf("disabled verifier reason: got %q, want disabled", s.Reason)
	}
}

func TestTriggerVerifierImmediateRunsOnlySelectedVerifier(t *testing.T) {
	state := daemon.NewState()
	a := Verifier{
		Name:      "Architect",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.1,"reason":"architect"}\n'`},
		Timeout:   2 * time.Second,
	}
	b := Verifier{
		Name:      "Test",
		Direction: "S",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"test"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{a, b})
	defer r.Stop()

	if ok := r.TriggerVerifierImmediate("Test"); !ok {
		t.Fatal("TriggerVerifierImmediate should accept a known enabled verifier")
	}
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Test")
		return !s.Running && s.Reason == "test"
	}) {
		s, _ := state.Verifier("Test")
		t.Fatalf("selected verifier did not run; reason=%q running=%v", s.Reason, s.Running)
	}
	architect, _ := state.Verifier("Architect")
	if architect.Reason != "awaiting first run" || !architect.ComputedAt.IsZero() {
		t.Fatalf("unselected verifier should not run, got %+v", architect)
	}
	if ok := r.TriggerVerifierImmediate("Missing"); ok {
		t.Fatal("unknown verifier should not be accepted")
	}
	state.SetVerifierDisabled("Test", true)
	if ok := r.TriggerVerifierImmediate("Test"); ok {
		t.Fatal("disabled verifier should not be accepted")
	}
}

// TestTriggerVerifierImmediateRunsInParallel guards the fix for the "hitting
// r on one verifier blocks r on another" bug: two distinct verifiers must be
// triggerable while one is still in flight.
func TestTriggerVerifierImmediateRunsInParallel(t *testing.T) {
	state := daemon.NewState()
	slow := Verifier{
		Name:      "Slow",
		Direction: "N",
		// Sleep so the first run is still in flight when we trigger the second.
		Command: []string{"sh", "-c", `sleep 0.3; printf '{"distance":0.1,"reason":"slow"}\n'`},
		Timeout: 5 * time.Second,
	}
	fast := Verifier{
		Name:      "Fast",
		Direction: "S",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"fast"}\n'`},
		Timeout:   5 * time.Second,
	}
	r := NewRunner(context.Background(), state, []Verifier{slow, fast})
	defer r.Stop()

	if ok := r.TriggerVerifierImmediate("Slow"); !ok {
		t.Fatal("first trigger should be accepted")
	}
	// Wait for Slow to be marked running before triggering Fast — otherwise
	// the test could pass for the wrong reason (Slow finishing too quickly).
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return s.Running
	}) {
		t.Fatal("Slow did not start running")
	}
	if ok := r.TriggerVerifierImmediate("Fast"); !ok {
		t.Fatal("second trigger should not be blocked while a different verifier is running")
	}
	if !waitFor(2*time.Second, func() bool {
		s, _ := state.Verifier("Fast")
		return !s.Running && s.Reason == "fast"
	}) {
		s, _ := state.Verifier("Fast")
		t.Fatalf("Fast did not complete while Slow was in flight; reason=%q running=%v", s.Reason, s.Running)
	}
	// Re-triggering the same verifier while it's still running must be a no-op.
	if ok := r.TriggerVerifierImmediate("Slow"); ok {
		t.Fatal("re-triggering an in-flight verifier should be rejected")
	}
	if !waitFor(3*time.Second, func() bool {
		s, _ := state.Verifier("Slow")
		return !s.Running && s.Reason == "slow"
	}) {
		s, _ := state.Verifier("Slow")
		t.Fatalf("Slow did not finish; reason=%q running=%v", s.Reason, s.Running)
	}
}

func TestNewRunnerSeedsVerifierConfig(t *testing.T) {
	state := daemon.NewState()
	verifiers := []Verifier{
		{
			Name:      "Architect",
			Direction: "N",
			Type:      TypeAgent,
			Timeout:   90 * time.Second,
			Agent: AgentConfig{
				Agent:    "codex",
				Model:    "gpt-5.5",
				Thinking: "high",
				Skill:    "./skills/architect/SKILL.md",
			},
		},
		{
			Name:      "Unit Tests",
			Direction: "S",
			Type:      TypeBinary,
			Binary: BinaryConfig{
				Command:    []string{"./scripts/test.sh"},
				PassReason: "tests pass",
				FailReason: "tests failed",
			},
		},
	}
	r := NewRunner(context.Background(), state, verifiers)
	defer r.Stop()

	agent, _ := state.Verifier("Architect")
	if agent.Config.Type != TypeAgent ||
		agent.Config.Agent != "codex" ||
		agent.Config.Model != "gpt-5.5" ||
		agent.Config.Thinking != "high" ||
		agent.Config.Timeout != "1m30s" {
		t.Fatalf("agent config not seeded: %+v", agent.Config)
	}

	binary, _ := state.Verifier("Unit Tests")
	if binary.Config.Type != TypeBinary ||
		len(binary.Config.Command) != 1 ||
		binary.Config.Command[0] != "./scripts/test.sh" ||
		binary.Config.PassReason != "tests pass" ||
		binary.Config.FailReason != "tests failed" {
		t.Fatalf("binary config not seeded: %+v", binary.Config)
	}
}

func TestNewRunnerSeedsDisabledVerifier(t *testing.T) {
	state := daemon.NewState()
	r := NewRunner(context.Background(), state, []Verifier{
		{
			Name:      "Architect",
			Direction: "N",
			Disabled:  true,
			Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"ok"}\n'`},
		},
	})
	defer r.Stop()

	status, ok := state.Verifier("Architect")
	if !ok {
		t.Fatal("verifier missing from state")
	}
	if !status.Disabled || status.Reason != "disabled" {
		t.Fatalf("disabled verifier not seeded correctly: %+v", status)
	}
}

// TestQuietPeriodSetsDefault locks the default and the override path.
func TestQuietPeriodSetsDefault(t *testing.T) {
	state := daemon.NewState()
	r := NewRunner(context.Background(), state, []Verifier{})
	defer r.Stop()
	if got := r.QuietPeriod(); got != DefaultQuietPeriod {
		t.Fatalf("default quiet period: got %s, want %s", got, DefaultQuietPeriod)
	}
	// 0 is a no-op so callers can pass through unset config without branching.
	r.SetQuietPeriod(0)
	if got := r.QuietPeriod(); got != DefaultQuietPeriod {
		t.Fatalf("SetQuietPeriod(0) should not change value; got %s", got)
	}
	r.SetQuietPeriod(7 * time.Second)
	if got := r.QuietPeriod(); got != 7*time.Second {
		t.Fatalf("override: got %s, want 7s", got)
	}
}
