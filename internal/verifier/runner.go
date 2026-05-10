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
	// batchCancel is non-nil while a batch's verifier subprocesses are
	// in-flight. KillBatch cancels it to terminate just the current batch
	// without tearing down the runner (Stop is the full-shutdown path).
	batchCancel context.CancelFunc
	// killed is set when KillBatch fires mid-batch, so the post-batch
	// reschedule path knows to drop any writes that landed during the kill
	// instead of immediately starting a new batch.
	killed bool

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
		state.UpsertVerifier(initialStatus(v))
	}
	return r
}

// ReplaceVerifiers updates the runner and state after hud.yaml is edited at
// runtime. Same-named verifier status is preserved by daemon.State.
func (r *Runner) ReplaceVerifiers(verifiers []Verifier) {
	r.mu.Lock()
	r.verifiers = append([]Verifier(nil), verifiers...)
	r.mu.Unlock()

	statuses := make([]ipc.VerifierStatus, 0, len(verifiers))
	for _, v := range verifiers {
		statuses = append(statuses, initialStatus(v))
	}
	r.state.ReplaceVerifiers(statuses)
}

func initialStatus(v Verifier) ipc.VerifierStatus {
	status := ipc.VerifierStatus{
		Name:      v.Name,
		Direction: v.Direction,
		Distance:  1.0, // assume far from goal until first run
		Reason:    "awaiting first run",
		Disabled:  v.Disabled,
		Config:    verifierConfig(v),
	}
	if v.Disabled {
		status.Reason = "disabled"
	}
	return status
}

func verifierConfig(v Verifier) ipc.VerifierConfig {
	cfg := ipc.VerifierConfig{
		Type: v.kind(),
	}
	if v.Timeout > 0 {
		cfg.Timeout = v.Timeout.String()
	}
	switch v.kind() {
	case TypeAgent:
		cfg.Agent = resolveAgent(v.Agent.Agent)
		cfg.Model = v.Agent.Model
		cfg.Thinking = v.Agent.Thinking
		cfg.Skill = v.Agent.Skill
	case TypeBinary:
		cfg.Command = append([]string(nil), v.Binary.Command...)
		cfg.PassReason = v.Binary.PassReason
		cfg.FailReason = v.Binary.FailReason
	case TypeCommand:
		cfg.Command = append([]string(nil), v.Command...)
	}
	return cfg
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

// Stop cancels any in-flight runs and tears down the runner permanently.
// Use KillBatch instead if you only want to abort the current batch and
// keep accepting future triggers.
func (r *Runner) Stop() { r.cancel() }

// KillBatch terminates any in-flight verifier subprocesses and discards
// pending or scheduled work. The runner stays usable: subsequent Trigger
// or TriggerImmediate calls schedule fresh batches as normal. Per-verifier
// distances are preserved (Reason becomes "stopped") so the last known
// score remains visible to the user.
func (r *Runner) KillBatch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.changedFiles = map[string]struct{}{}
	if r.batchCancel != nil {
		r.killed = true
		r.batchCancel()
	}
}

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
	r.timer = time.AfterFunc(delay, func() { r.runBatch("") })
}

// TriggerImmediate schedules a batch to run immediately, bypassing the quiet
// period. If a batch is already running or scheduled, this is a no-op.
func (r *Runner) TriggerImmediate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil || r.running {
		return
	}
	r.timer = time.AfterFunc(0, func() { r.runBatch("") })
}

// TriggerVerifierImmediate schedules a single verifier to run immediately,
// bypassing the quiet period. It returns false when the verifier is unknown,
// disabled, or another batch is already scheduled/running.
func (r *Runner) TriggerVerifierImmediate(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil || r.running {
		return false
	}
	found := false
	for _, v := range r.verifiers {
		if v.Name == name {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if cur, ok := r.state.Verifier(name); ok && cur.Disabled {
		return false
	}
	r.timer = time.AfterFunc(0, func() { r.runBatch(name) })
	return true
}

// RunNow runs all verifiers synchronously (used in tests).
func (r *Runner) RunNow() { r.runBatch("") }

func (r *Runner) runBatch(only string) {
	r.mu.Lock()
	verifiers := append([]Verifier(nil), r.verifiers...)
	files := make([]string, 0, len(r.changedFiles))
	for f := range r.changedFiles {
		files = append(files, f)
	}
	r.changedFiles = map[string]struct{}{}
	r.timer = nil
	r.lastBatchAt = time.Now()
	r.running = true
	batchCtx, batchCancel := context.WithCancel(r.ctx)
	r.batchCancel = batchCancel
	r.mu.Unlock()

	cwd, _ := os.Getwd()
	session := Session{
		Goal:           r.state.Goal(),
		SessionBaseRef: r.state.SessionBaseRef(),
		ChangedFiles:   files,
		LastMessages:   transcript.LastMessages(cwd, transcriptTurns),
	}

	var wg sync.WaitGroup
	for _, v := range verifiers {
		if only != "" && v.Name != only {
			continue
		}
		wg.Add(1)
		go func(v Verifier) {
			defer wg.Done()
			if cur, ok := r.state.Verifier(v.Name); ok && cur.Disabled {
				return
			}
			r.state.MarkRunning(v.Name, true)
			res, err := v.Verify(batchCtx, session)
			cur, ok := r.state.Verifier(v.Name)
			if !ok {
				return
			}
			if cur.Disabled {
				cur.Running = false
				r.state.UpsertVerifier(cur)
				return
			}
			cur.Running = false
			cur.ComputedAt = time.Now()
			if err != nil {
				if batchCtx.Err() != nil {
					// User-initiated stop: keep the previously displayed
					// distance and just label the row so the kill is visible.
					cur.Reason = "stopped"
				} else {
					cur.Reason = "error: " + err.Error()
					cur.Distance = 1.0
					fmt.Fprintf(os.Stderr, "[hud] verifier %s failed: %v\n", v.Name, err)
				}
			} else {
				cur.Distance = res.Distance
				cur.Reason = res.Reason
			}
			r.state.UpsertVerifier(cur)
		}(v)
	}
	wg.Wait()

	r.mu.Lock()
	batchCancel()
	r.batchCancel = nil
	r.running = false
	killed := r.killed
	r.killed = false
	switch {
	case killed:
		// User pressed stop: discard anything that landed during the kill
		// rather than immediately rescheduling against their wishes.
		r.changedFiles = map[string]struct{}{}
	case len(r.changedFiles) > 0:
		// Writes accumulated during the run; schedule the next batch.
		// A long-running batch may already have consumed the quiet period,
		// in which case scheduleLocked fires immediately.
		r.scheduleLocked()
	}
	r.mu.Unlock()
}
