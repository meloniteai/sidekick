package verifier

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/daemon"
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
