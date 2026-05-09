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

// DefaultDebounce coalesces bursts of file-write events from a single
// agent edit operation (Edit/MultiEdit can emit several within milliseconds).
const DefaultDebounce = 2 * time.Second

// Runner schedules verifier subprocess runs in response to file-write events
// and writes their results into the daemon's State.
type Runner struct {
	verifiers []Verifier
	state     *daemon.State
	debounce  time.Duration

	mu           sync.Mutex
	timer        *time.Timer
	changedFiles map[string]struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// NewRunner returns a Runner bound to the given state and registered
// verifiers. Each verifier is also seeded into State so the TUI shows them
// from boot.
func NewRunner(parent context.Context, state *daemon.State, verifiers []Verifier) *Runner {
	ctx, cancel := context.WithCancel(parent)
	r := &Runner{
		verifiers:    verifiers,
		state:        state,
		debounce:     DefaultDebounce,
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

// Stop cancels any in-flight runs.
func (r *Runner) Stop() { r.cancel() }

// Trigger registers a changed file and schedules a debounced batch run.
func (r *Runner) Trigger(file string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if file != "" {
		r.changedFiles[file] = struct{}{}
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.debounce, r.runBatch)
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
}
