package verifier

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/transcript"
)

// transcriptTurns is the number of most-recent user/assistant messages we
// pull from CC's JSONL transcript and forward to verifier subprocesses.
const transcriptTurns = 12

// DefaultQuietPeriod is the minimum gap between batch starts when the user
// hasn't configured one. Bursts of writes inside the window are coalesced;
// once the window elapses the queued batch fires, so no change is dropped.
const DefaultQuietPeriod = 2 * time.Second

// Runner schedules verifier subprocess runs in response to file-write events
// and writes their results into the daemon's State. It enforces a minimum
// quiet period between batch starts to keep LLM-backed verifier costs
// bounded, while guaranteeing that a coalesced burst still triggers a run.
type Runner struct {
	verifiers []Verifier
	state     *daemon.State

	mu           sync.Mutex
	quietPeriod  time.Duration
	timer        *time.Timer
	changedFiles map[string]struct{}
	lastBatchAt  time.Time
	running      bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewRunner returns a Runner bound to the given state and registered
// verifiers. Each verifier is also seeded into State so the TUI shows them
// from boot. Use SetQuietPeriod to override the default coalescing window.
func NewRunner(parent context.Context, state *daemon.State, verifiers []Verifier) *Runner {
	ctx, cancel := context.WithCancel(parent)
	r := &Runner{
		verifiers:    verifiers,
		state:        state,
		quietPeriod:  DefaultQuietPeriod,
		changedFiles: map[string]struct{}{},
		ctx:          ctx,
		cancel:       cancel,
	}
	for _, v := range verifiers {
		state.UpsertVerifier(ipc.VerifierStatus{
			Name:      v.Name,
			Direction: v.Direction,
			Distance:  1.0, // assume far from goal until first run
			Reason:    "awaiting first run",
		})
	}
	return r
}

// SetQuietPeriod overrides the minimum gap between batch starts. Pass 0 to
// keep the existing value (callers wiring config can pass through whatever
// hud.yaml resolved without branching on "unset").
func (r *Runner) SetQuietPeriod(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.quietPeriod = d
	r.mu.Unlock()
}

// QuietPeriod reports the current configured minimum gap between batch
// starts. Useful for tests and for the daemon to log on boot.
func (r *Runner) QuietPeriod() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quietPeriod
}

// Stop cancels any in-flight runs.
func (r *Runner) Stop() { r.cancel() }

// Trigger registers a changed file and ensures a batch run will happen no
// later than quietPeriod after the previous batch's start.
//
// If a batch is already scheduled or actively running, the new file simply
// joins the pending set — we never reset the timer (debounce-style), because
// a stream of edits could otherwise starve the verifiers indefinitely. The
// post-run hook reschedules whenever changes accumulated mid-batch.
func (r *Runner) Trigger(file string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if file != "" {
		r.changedFiles[file] = struct{}{}
	}
	if r.timer != nil || r.running {
		return
	}
	r.scheduleLocked()
}

// scheduleLocked arms r.timer to fire at lastBatchAt+quietPeriod, or
// immediately if we're already past that point (or have never run). Caller
// must hold r.mu.
func (r *Runner) scheduleLocked() {
	delay := time.Duration(0)
	if !r.lastBatchAt.IsZero() {
		delay = r.quietPeriod - time.Since(r.lastBatchAt)
		if delay < 0 {
			delay = 0
		}
	}
	r.timer = time.AfterFunc(delay, r.runBatch)
}

// RunNow runs all verifiers synchronously (used in tests).
func (r *Runner) RunNow() { r.runBatch() }

func (r *Runner) runBatch() {
	r.mu.Lock()
	files := make([]string, 0, len(r.changedFiles))
	for f := range r.changedFiles {
		files = append(files, f)
	}
	r.changedFiles = map[string]struct{}{}
	r.timer = nil
	r.lastBatchAt = time.Now()
	r.running = true
	r.mu.Unlock()

	cwd, _ := os.Getwd()
	session := Session{
		Goal:           r.state.Goal(),
		SessionBaseRef: r.state.SessionBaseRef(),
		ChangedFiles:   files,
		LastMessages:   transcript.LastMessages(cwd, transcriptTurns),
	}

	var wg sync.WaitGroup
	for _, v := range r.verifiers {
		wg.Add(1)
		go func(v Verifier) {
			defer wg.Done()
			r.state.MarkRunning(v.Name, true)
			res, err := v.Verify(r.ctx, session)
			cur, _ := r.state.Verifier(v.Name)
			cur.Running = false
			cur.ComputedAt = time.Now()
			if err != nil {
				cur.Reason = "error: " + err.Error()
				cur.Distance = 1.0
				fmt.Fprintf(os.Stderr, "[hud] verifier %s failed: %v\n", v.Name, err)
			} else {
				cur.Distance = res.Distance
				cur.Reason = res.Reason
			}
			r.state.UpsertVerifier(cur)
		}(v)
	}
	wg.Wait()

	// If writes accumulated during the run, schedule the next batch now —
	// possibly immediately, since a long-running batch will already have
	// consumed (often more than) the quiet period.
	r.mu.Lock()
	r.running = false
	if len(r.changedFiles) > 0 {
		r.scheduleLocked()
	}
	r.mu.Unlock()
}
