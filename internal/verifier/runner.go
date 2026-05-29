package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/ipc"
	"github.com/meloniteai/sidekick/internal/telemetry"
	"github.com/meloniteai/sidekick/internal/transcript"
)

// transcriptTurns is the number of most-recent user/assistant messages we
// pull from CC's JSONL transcript and forward to verifier subprocesses.
const transcriptTurns = 12

// historyDepth is how many recent results we retain per verifier in memory.
// Sized for "is this verifier flaky" trend reads via sidekick_explain. Lost on
// daemon restart — persistence is a v0.2 concern.
const historyDepth = 32

// appendHistory adds p to the tail of the ring buffer, evicting the oldest
// entry when capacity is reached. Returns a fresh slice so callers don't
// need to worry about aliasing the daemon State map.
func appendHistory(prev []ipc.HistoryPoint, p ipc.HistoryPoint) []ipc.HistoryPoint {
	if len(prev) < historyDepth {
		return append(append([]ipc.HistoryPoint(nil), prev...), p)
	}
	out := make([]ipc.HistoryPoint, historyDepth)
	copy(out, prev[len(prev)-historyDepth+1:])
	out[historyDepth-1] = p
	return out
}

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
	findingsCap  int
	timer        *time.Timer
	changedFiles map[string]struct{}
	lastBatchAt  time.Time
	running      bool
	// batchCancel is non-nil while a batch's verifier subprocesses are
	// in-flight. KillBatch cancels it to terminate just the current batch
	// without tearing down the runner (Stop is the full-shutdown path).
	batchCancel context.CancelFunc
	// singleCancels tracks in-flight per-verifier single-trigger runs so the
	// user can fire "r" on several verifiers in parallel — each gets its own
	// cancel so KillBatch can tear them down alongside any batch.
	singleCancels map[string]context.CancelFunc
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
		verifiers:     verifiers,
		state:         state,
		quietPeriod:   DefaultQuietPeriod,
		findingsCap:   DefaultFindingsCap,
		changedFiles:  map[string]struct{}{},
		singleCancels: map[string]context.CancelFunc{},
		ctx:           ctx,
		cancel:        cancel,
	}
	for _, v := range verifiers {
		state.UpsertVerifier(initialStatus(v))
	}
	return r
}

// ReplaceVerifiers updates the runner and state after sidekick.yaml is edited at
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
		Status:    ipc.StatusPending,
		Disabled:  v.Disabled,
		Config:    verifierConfig(v),
	}
	if v.Disabled {
		status.Reason = "disabled"
		status.Status = ipc.StatusDisabled
	}
	return status
}

func verifierConfig(v Verifier) ipc.VerifierConfig {
	cfg := ipc.VerifierConfig{
		Type:      v.kind(),
		Source:    v.Source,
		SourceURL: v.SourceURL,
		SHA256:    v.SHA256,
		Permissions: ipc.VerifierPermissions{
			Network:    v.Permissions.Network,
			Filesystem: v.Permissions.Filesystem,
			Env:        append([]string(nil), v.Permissions.Env...),
		},
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
// sidekick.yaml resolved without branching on "unset").
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

// SetFindingsCap overrides the maximum findings stored per run. A non-positive
// value disables the cap. See prepareFindings for the overflow behaviour.
func (r *Runner) SetFindingsCap(n int) {
	r.mu.Lock()
	r.findingsCap = n
	r.mu.Unlock()
}

// findingsCapValue reads the configured cap under lock.
func (r *Runner) findingsCapValue() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findingsCap
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
	for name, cancel := range r.singleCancels {
		cancel()
		delete(r.singleCancels, name)
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

// TriggerVerifierImmediate launches a single verifier in its own goroutine,
// bypassing the quiet period and the global batch lock so the user can fire
// several verifiers in parallel from the TUI. Returns false when the verifier
// is unknown, disabled, or is already running (either as a single trigger or
// as part of an in-flight batch).
func (r *Runner) TriggerVerifierImmediate(name string) bool {
	r.mu.Lock()
	var target Verifier
	found := false
	for _, v := range r.verifiers {
		if v.Name == name {
			target = v
			found = true
			break
		}
	}
	if !found {
		r.mu.Unlock()
		return false
	}
	cur, ok := r.state.Verifier(name)
	if !ok || cur.Disabled {
		r.mu.Unlock()
		return false
	}
	if _, busy := r.singleCancels[name]; busy {
		r.mu.Unlock()
		return false
	}
	if cur.Running {
		// In flight as part of a batch — let that one complete.
		r.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(r.ctx)
	r.singleCancels[name] = cancel
	r.mu.Unlock()

	go r.runSingle(target, ctx)
	return true
}

// runSingle executes one verifier outside of the batch machinery, so an
// individual "r" trigger doesn't block on (or get blocked by) an unrelated
// run. Session has no ChangedFiles because the user explicitly opted into
// running just this one — drained files belong to the batch path.
func (r *Runner) runSingle(v Verifier, ctx context.Context) {
	defer func() {
		r.mu.Lock()
		delete(r.singleCancels, v.Name)
		r.mu.Unlock()
	}()

	cwd, _ := os.Getwd()
	session := Session{
		Goal:           r.state.Goal(),
		SessionBaseRef: r.state.SessionBaseRef(),
		LastMessages:   transcript.LastMessages(cwd, transcriptTurns),
	}
	// Single "r"-key runs carry no batch/file context, so they are recorded as
	// batch-less verifier_runs (empty batch id).
	r.executeVerifier(ctx, v, session, "")
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
	worktree := r.state.SessionWorktree()
	transcriptDir := worktree
	if transcriptDir == "" {
		transcriptDir = cwd
	}
	session := Session{
		Goal:            r.state.Goal(),
		SessionBaseRef:  r.state.SessionBaseRef(),
		SessionWorktree: worktree,
		ChangedFiles:    files,
		LastMessages:    transcript.LastMessages(transcriptDir, transcriptTurns),
	}

	batchID := r.recordBatch(files)

	var wg sync.WaitGroup
	for _, v := range verifiers {
		if only != "" && v.Name != only {
			continue
		}
		r.mu.Lock()
		_, single := r.singleCancels[v.Name]
		r.mu.Unlock()
		if single {
			// Already running individually via "r"; let that one own the slot
			// so we don't double-mark Running or race on UpsertVerifier.
			continue
		}
		wg.Add(1)
		go func(v Verifier) {
			defer wg.Done()
			r.executeVerifier(batchCtx, v, session, batchID)
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

// executeVerifier runs one verifier and folds its result into State. Shared
// by the batch and single-trigger paths so they update state identically.
// batchID attributes the resulting verifier_run to its batch; it is "" for
// single ("r"-key) runs. Verify() is timed for every verifier type so the
// telemetry latency lens covers command/binary verifiers (whose Usage is nil).
func (r *Runner) executeVerifier(ctx context.Context, v Verifier, session Session, batchID string) {
	if cur, ok := r.state.Verifier(v.Name); ok && cur.Disabled {
		return
	}
	r.state.MarkRunning(v.Name, true)
	start := time.Now()
	res, err := v.Verify(ctx, session)
	elapsed := time.Since(start).Milliseconds()
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
	stopped := false
	var findings []Finding
	switch {
	case err != nil && ctx.Err() != nil:
		// User-initiated stop: keep the previously displayed distance
		// and just label the row so the kill is visible.
		cur.Reason = "stopped"
		stopped = true
		// Status unchanged.
		r.state.LogEvent(daemon.EventInfo, "verifier %s stopped", v.Name)
	case err != nil:
		cur.Reason = "error: " + err.Error()
		cur.Distance = 1.0
		cur.Status = ipc.StatusError
		r.state.LogEvent(daemon.EventError, "verifier %s failed: %v", v.Name, err)
	case res.Status == ipc.StatusUnknown:
		// Verifier ran but couldn't score. Preserve the prior distance
		// so the compass doesn't lie; just surface the reason+status.
		cur.Reason = res.Reason
		cur.Status = ipc.StatusUnknown
		r.state.LogEvent(daemon.EventInfo, "verifier %s unknown: %s", v.Name, res.Reason)
	default:
		cur.Distance = res.Distance
		cur.Reason = res.Reason
		cur.Status = res.Status
		if cur.Status == "" {
			cur.Status = ipc.StatusOK
		}
		findings = prepareFindings(res.Findings, session.SessionWorktree, r.findingsCapValue())
		r.state.LogEvent(daemon.EventInfo, "verifier %s %s d=%.2f", v.Name, cur.Status, cur.Distance)
	}
	if res.Usage != nil {
		cur.LastUsage = &ipc.AgentUsage{
			Model:        res.Usage.Model,
			InputTokens:  res.Usage.InputTokens,
			OutputTokens: res.Usage.OutputTokens,
			CacheReads:   res.Usage.CacheReads,
			CacheWrites:  res.Usage.CacheWrites,
			CostUSD:      res.Usage.CostUSD,
			DurationMS:   res.Usage.DurationMS,
		}
	}
	cur.History = appendHistory(cur.History, ipc.HistoryPoint{
		Distance:   cur.Distance,
		Status:     cur.Status,
		ComputedAt: cur.ComputedAt,
	})
	r.state.UpsertVerifier(cur)

	// A stopped run produced no measurement (the user killed it), so it is not
	// recorded — only genuine evaluations become telemetry.
	if !stopped {
		r.recordVerifierRun(batchID, v, cur, elapsed, res.Usage, findings)
	}
}

// recordBatch emits one batch row and returns its id, which threads into each
// verifier_run so a distance can be attributed to the files that triggered it.
// Returns "" (and records nothing) when telemetry is disabled or no goal
// episode is active.
func (r *Runner) recordBatch(files []string) string {
	emitter := r.state.Emitter()
	sid := r.state.TelemetrySessionID()
	if emitter == nil || sid == "" {
		return ""
	}
	id := telemetry.NewID()
	if err := emitter.RecordBatch(telemetry.BatchRecord{
		BatchID:   id,
		SessionID: sid,
		TS:        time.Now(),
		FileSet:   files,
		FileCount: len(files),
		BaseRef:   r.state.SessionBaseRef(),
	}); err != nil {
		r.state.LogEvent(daemon.EventError, "telemetry: record batch: %v", err)
		return id
	}
	r.state.IncTelemetryBatchCount()
	return id
}

// recordVerifierRun emits one verifier_run row and its attributed findings. cur
// carries the folded scalar (distance/reason/status); usage is nil for
// command/binary verifiers; findings is empty for a passing verifier or when the
// run could not be attributed. The run row is written first so its id can parent
// the findings.
func (r *Runner) recordVerifierRun(batchID string, v Verifier, cur ipc.VerifierStatus, durationMS int64, usage *UsageInfo, findings []Finding) {
	emitter := r.state.Emitter()
	sid := r.state.TelemetrySessionID()
	if emitter == nil || sid == "" {
		return
	}
	// verifier_version is stamped on EVERY run (agent/command/binary, in every
	// status) so each judgment is join-able to a versioned rubric. A read/marshal
	// failure is fail-safe: the version is "" and the run is still recorded, but
	// the failure is logged so silent un-versioned judgments stay observable.
	ver, verErr := verifierVersionErr(v)
	if verErr != nil {
		r.state.LogEvent(daemon.EventError, "telemetry: verifier_version for %s: %v", v.Name, verErr)
	}
	rec := telemetry.VerifierRunRecord{
		BatchID:         batchID,
		SessionID:       sid,
		VerifierName:    v.Name,
		VerifierVersion: ver,
		Distance:        cur.Distance,
		Reason:          cur.Reason,
		Status:          cur.Status,
		DurationMS:      durationMS,
		TS:              time.Now(),
	}
	if usage != nil {
		rec.InputTokens = usage.InputTokens
		rec.OutputTokens = usage.OutputTokens
		rec.CacheReads = usage.CacheReads
		rec.CacheWrites = usage.CacheWrites
		rec.CostUSD = usage.CostUSD
	}
	runID, err := emitter.RecordVerifierRun(rec)
	if err != nil {
		r.state.LogEvent(daemon.EventError, "telemetry: record verifier_run: %v", err)
		return
	}
	if len(findings) == 0 {
		return
	}
	now := time.Now()
	// Anchor each finding to a stable content hash so a session-time judgment
	// joins to the eventual committed PR hunk. f.Path is already the repo-
	// relative key from prepareFindings (post-NormalizeRepoPath); the session
	// base ref is the stable HEAD the working tree is dirty against. A line-
	// bearing finding gets a hunkHash; a tree-global / no-line finding (Path==""
	// or Line==0) carries only the whole-file dirtyDiffHash (both "" when there
	// is no file or no diff). Empty hashes are tolerated downstream.
	worktree := r.state.SessionWorktree()
	baseRef := r.state.SessionBaseRef()
	frecs := make([]telemetry.FindingRecord, 0, len(findings))
	for _, f := range findings {
		var hunkHash, dirtyDiffHash string
		if f.Path != "" {
			hunkHash, dirtyDiffHash = telemetry.HunkAnchor(worktree, baseRef, f.Path, f.Line)
		}
		frecs = append(frecs, telemetry.FindingRecord{
			SessionID:     sid,
			BatchID:       batchID,
			VerifierName:  v.Name,
			FilePath:      f.Path,
			Symbol:        f.Symbol,
			Line:          f.Line,
			Distance:      f.Distance,
			Reason:        f.Reason,
			HunkHash:      hunkHash,
			DirtyDiffHash: dirtyDiffHash,
			TS:            now,
		})
	}
	if err := emitter.RecordFindings(runID, frecs); err != nil {
		r.state.LogEvent(daemon.EventError, "telemetry: record findings: %v", err)
	}
}

// versionInput is the exact, deterministic set of inputs that define a
// verifier_version. The spec formula is
//
//	verifier_version = sha256(resolvedSkillBody + agent + model + thinking + direction)[:12]
//
// We serialize these five inputs (plus the type discriminator and the
// type-appropriate provenance below) as a json.Marshal of this struct, in
// fixed field order, with no maps — so the bytes are identical across processes,
// runs, and machines. Any reimplementation (e.g. the weightless worker stamping
// INDUCTION versions) MUST serialize the same fields in the same order; see
// docs/anchor-hunk-normalization.md (verifier_version section) for the contract.
//
// Why the extra fields beyond the canonical five:
//   - Type discriminates agent vs command vs binary so two configs that share a
//     direction but differ in kind never collide.
//   - SkillBody is the FRONTMATTER-STRIPPED skill contents (not the path), so two
//     identical rubrics at different paths share a version and a one-byte body
//     edit changes it. It is hashed verbatim (no whitespace collapse): a reworded
//     rubric is a re-induction. Empty for command/binary verifiers.
//   - Command is the resolved argv for command/binary verifiers; empty for agent.
//   - Source/SourceURL/SHA256 bind a remote rubric's content identity so a
//     re-fetched, modified remote artifact cannot masquerade as the old version.
//
// Fields NOT in this struct (Permissions, Timeout, the skill PATH) are
// deliberately excluded and MUST NOT affect the version.
type versionInput struct {
	Type      string `json:"type"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	Direction string `json:"direction"`
	SkillBody string `json:"skill_body"`
	Command   string `json:"command"`
	Source    string `json:"source"`
	SourceURL string `json:"source_url"`
	SHA256    string `json:"sha256"`
}

// verifierVersion is the fail-safe wrapper: it returns the 12-hex version and
// swallows the error (empty string), for callers and tests that only care about
// the value. Use verifierVersionErr at the recording site so a read/marshal
// failure can be logged rather than silently dropped.
func verifierVersion(v Verifier) string {
	ver, _ := verifierVersionErr(v)
	return ver
}

// verifierVersionErr computes verifier_version = sha256(resolvedSkillBody +
// agent + model + thinking + direction [+ type/command/provenance])[:12].
//
// It returns ("", err) when the agent's skill body cannot be read or the input
// cannot be marshaled — fail-safe, never a panic: the caller still records the
// run with an empty version and surfaces the error. Direction is case-normalized
// (uppercased) so "n" and "N" hash identically. Output is exactly 12 lowercase
// hex chars.
func verifierVersionErr(v Verifier) (string, error) {
	in := versionInput{
		Type:      v.kind(),
		Direction: strings.ToUpper(strings.TrimSpace(v.Direction)),
		Source:    v.Source,
		SourceURL: v.SourceURL,
		SHA256:    v.SHA256,
	}
	switch v.kind() {
	case TypeAgent:
		in.Agent = resolveAgent(v.Agent.Agent)
		in.Model = v.Agent.Model
		in.Thinking = v.Agent.Thinking
		// resolvedSkillBody: the frontmatter-stripped file contents, not the
		// path. A binary/command verifier has no skill and never reads a file.
		if strings.TrimSpace(v.Agent.Skill) != "" {
			body, err := skillBody(v.Agent.Skill)
			if err != nil {
				return "", err
			}
			in.SkillBody = body
		}
	case TypeBinary:
		in.Command = strings.Join(v.Binary.Command, "\x00")
	case TypeCommand:
		in.Command = strings.Join(v.Command, "\x00")
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12], nil
}
