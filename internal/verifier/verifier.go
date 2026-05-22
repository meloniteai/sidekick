// Package verifier defines the subprocess-backed verifier interface and runner.
//
// Sidekick supports three verifier types:
//   - command: reads session JSON on stdin and writes {distance, reason}.
//   - agent: loads a SKILL.md rubric and invokes a configured agent CLI.
//   - binary: maps command exit status to pass/fail distance.
//
// The command protocol remains the extension point for fully custom verifiers.
package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

// Session is the context piped to verifier subprocesses on stdin.
//
// SessionBaseRef is the git SHA `HEAD` pointed at when the session was
// anchored; SessionWorktree is the absolute path to that anchored git
// worktree. LLM-backed verifiers diff the working tree at
// SessionWorktree against SessionBaseRef to score cumulative session
// work rather than the last debounced write.
type Session struct {
	Goal            string   `json:"goal"`
	SessionBaseRef  string   `json:"session_base_ref,omitempty"`
	SessionWorktree string   `json:"session_worktree,omitempty"`
	RecentDiff      string   `json:"recent_diff,omitempty"`
	ChangedFiles    []string `json:"changed_files,omitempty"`
	LastMessages    []string `json:"last_messages,omitempty"`
	VerifierName    string   `json:"verifier_name"`
}

// Finding is one attributed unit of distance from goal. Path is nullable: a
// verifier whose judgment is genuinely tree-wide (e.g. "the suite does not
// pass") emits a single finding with Path == "".
type Finding struct {
	Path     string  `json:"path,omitempty"`   // repo-relative; "" == tree-global
	Symbol   string  `json:"symbol,omitempty"` // function/type/identifier, optional
	Line     int     `json:"line,omitempty"`   // 1-based, optional
	Distance float64 `json:"distance"`         // 0.0..1.0 for this unit
	Reason   string  `json:"reason"`           // one short sentence
}

// Result is what a verifier subprocess prints on stdout.
//
// Findings carry per-file (and optionally per-symbol) attribution. Distance and
// Reason remain the rolled-up scalar the compass consumes; when Findings is
// non-empty they are DERIVED from it (Distance = max finding distance), so the
// two grains cannot drift. An empty Findings set with a passing status means the
// goal is met for this lens.
//
// Status is optional in the on-the-wire JSON; the runner promotes a missing
// value to ipc.StatusOK on success. Verifiers that genuinely cannot score
// (e.g. tooling missing, no diff to evaluate) should return Status="unknown"
// rather than fabricating a distance — the runner then preserves the
// previous distance instead of pretending the score moved.
type Result struct {
	Findings []Finding  `json:"findings,omitempty"`
	Distance float64    `json:"distance"`
	Reason   string     `json:"reason"`
	Status   string     `json:"status,omitempty"`
	Usage    *UsageInfo `json:"-"`
}

// UsageInfo carries per-run token / cost telemetry surfaced to the daemon.
// Populated by agent verifiers that scrape it from the underlying CLI;
// nil for command/binary verifiers.
type UsageInfo struct {
	Model        string
	InputTokens  int
	OutputTokens int
	CacheReads   int
	CacheWrites  int
	CostUSD      float64
	DurationMS   int64
}

// Verifier type names.
const (
	TypeCommand = "command"
	TypeAgent   = "agent"
	TypeBinary  = "binary"
)

// Verifier is a single configured verifier.
type Verifier struct {
	Name        string
	Direction   string // N, NE, E, SE, S, SW, W, NW
	Type        string
	Disabled    bool
	Command     []string
	Timeout     time.Duration // 0 → 60s default
	Agent       AgentConfig
	Binary      BinaryConfig
	Permissions Permissions

	// Source provenance — populated when the verifier was loaded from a
	// remote URL via `sidekick verifier add` (or an inline `source:` block in
	// sidekick.yaml). "local" / "" means an in-tree script.
	Source    string // "local" | "remote"
	SourceURL string
	SHA256    string
}

// Permissions is the permission declaration carried with each verifier.
// Network/Filesystem/Env are advisory in v0.1 (surfaced in the TUI for
// human review; future versions may enforce via sandbox-exec / landlock).
// AllowedTools is enforced for the Claude agent: entries are appended to
// the hardcoded baseline list at spawn time so verifier authors can widen
// what their subprocess is allowed to call without losing the safe
// defaults.
type Permissions struct {
	Network      bool
	Filesystem   string // "read-only" | "read-write" | "none"
	Env          []string
	AllowedTools []string
}

// AgentConfig configures a native agent-backed verifier. AllowedTools
// mirrors Permissions.AllowedTools — copied during config.Resolve so
// agentCommand (which only sees AgentConfig) can honour it without having
// to reach back to the parent Verifier.
type AgentConfig struct {
	Agent        string
	Model        string
	Thinking     string
	Skill        string
	Custom       CustomAgent
	AllowedTools []string
}

// CustomAgent configures a user-supplied agent CLI invoked via templated
// argv. The command is text/template-substituted with {{.Model}},
// {{.Thinking}}, and {{.Skill}} per element before exec; stdin is fed
// the assembled prompt body when StdinFmt is "" or "prompt".
type CustomAgent struct {
	Command  []string
	StdinFmt string
}

// BinaryConfig configures a native pass/fail verifier. By default it is
// exit-code-only (the Tier-1 floor). Format/OutputFile opt into SARIF parsing
// (Tier 2); FailRegex opts into per-line extraction (Tier 3). Resolution
// precedence is Format > FailRegex > floor.
type BinaryConfig struct {
	Command    []string
	PassReason string
	FailReason string
	Format     string // "" (exit-code only) | "sarif"
	OutputFile string // optional path (relative to the worktree) to read structured output from
	FailRegex  string // named-group regex (file/line/reason/symbol) extracting findings from text
}

// DefaultTimeout applies when Verifier.Timeout is zero.
const DefaultTimeout = 60 * time.Second

// Verify runs the configured verifier. The returned Result.Distance is clamped
// to [0, 1] before being returned.
func (v Verifier) Verify(ctx context.Context, s Session) (Result, error) {
	switch v.kind() {
	case TypeCommand:
		return v.verifyCommand(ctx, s)
	case TypeAgent:
		return v.verifyAgent(ctx, s)
	case TypeBinary:
		return v.verifyBinary(ctx, s)
	default:
		return Result{}, fmt.Errorf("unknown verifier type %q", v.Type)
	}
}

func (v Verifier) kind() string {
	t := v.Type
	if t == "llm" {
		t = TypeAgent
	}
	if t == "" {
		return TypeCommand
	}
	return t
}

func (v Verifier) verifyCommand(ctx context.Context, s Session) (Result, error) {
	if len(v.Command) == 0 {
		return Result{}, errors.New("verifier has empty command")
	}
	stdinJSON, subCtx, cancel, err := v.prepare(ctx, s)
	if err != nil {
		return Result{}, err
	}
	defer cancel()

	stdout, stderr, err := runSubprocess(subCtx, v.Command, stdinJSON, verifierEnv(s), s.SessionWorktree)
	if err != nil {
		return Result{}, fmt.Errorf("verifier %s exited with error: %w (stderr: %s)",
			v.Name, err, strings.TrimSpace(stderr))
	}
	return parseCommandResult(v.Name, stdout)
}

func (v Verifier) verifyAgent(ctx context.Context, s Session) (Result, error) {
	if v.Agent.Skill == "" {
		return Result{}, errors.New("agent verifier requires agent.skill")
	}
	prompt, err := BuildAgentPrompt(v, s)
	if err != nil {
		return Result{}, err
	}
	cmd, err := agentCommand(v.Agent)
	if err != nil {
		return Result{}, err
	}
	_, subCtx, cancel, err := v.prepare(ctx, s)
	if err != nil {
		return Result{}, err
	}
	defer cancel()

	start := time.Now()
	stdout, stderr, err := runSubprocess(subCtx, cmd, []byte(prompt), verifierEnv(s), s.SessionWorktree)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return Result{}, fmt.Errorf("agent verifier %s exited with error: %w (stderr: %s)",
			v.Name, err, strings.TrimSpace(stderr))
	}
	res, err := parseAgentResult(v.Name, resolveAgent(v.Agent.Agent), stdout)
	if err != nil {
		return res, err
	}
	if res.Usage != nil {
		res.Usage.DurationMS = elapsed
		if res.Usage.Model == "" {
			res.Usage.Model = v.Agent.Model
		}
	}
	return res, nil
}

func (v Verifier) verifyBinary(ctx context.Context, s Session) (Result, error) {
	if len(v.Binary.Command) == 0 {
		return Result{}, errors.New("binary verifier requires binary.command")
	}
	stdinJSON, subCtx, cancel, err := v.prepare(ctx, s)
	if err != nil {
		return Result{}, err
	}
	defer cancel()

	stdout, stderr, runErr := runSubprocess(subCtx, v.Binary.Command, stdinJSON, verifierEnv(s), s.SessionWorktree)

	// Tiers 2-3: when structured extraction is configured, the findings are the
	// signal independent of exit code (linters often exit non-zero merely
	// because they reported findings).
	if findings, active := v.extractBinaryFindings(s.SessionWorktree, stdout, stderr); active && len(findings) > 0 {
		return finalizeResult(Result{Findings: findings, Status: ipc.StatusOK}), nil
	}

	if runErr == nil {
		return finalizeResult(Result{Distance: 0, Reason: v.binaryPassReason(), Status: ipc.StatusOK}), nil
	}
	// Tier-1 floor: an exit-code-only binary (or a non-zero run with nothing
	// extractable) can't attribute friction to a file, so a failure becomes one
	// tree-global finding (finalizeResult wraps it).
	return finalizeResult(Result{Distance: 1, Reason: v.binaryFailReason(stdout, stderr, runErr), Status: ipc.StatusOK}), nil
}

func (v Verifier) binaryPassReason() string {
	if v.Binary.PassReason != "" {
		return v.Binary.PassReason
	}
	return "passed"
}

func (v Verifier) binaryFailReason(stdout, stderr string, runErr error) string {
	if v.Binary.FailReason != "" {
		return v.Binary.FailReason
	}
	if r := lastNonEmptyLine(stderr); r != "" {
		return r
	}
	if r := lastNonEmptyLine(stdout); r != "" {
		return r
	}
	return runErr.Error()
}

// extractBinaryFindings runs the configured Tier-2/3 extractor. The bool reports
// whether any extractor was active (so the caller can distinguish "no findings"
// from "no extractor configured"). Precedence is Format > FailRegex.
func (v Verifier) extractBinaryFindings(worktree, stdout, stderr string) ([]Finding, bool) {
	switch {
	case strings.EqualFold(v.Binary.Format, "sarif"):
		data := stdout
		if v.Binary.OutputFile != "" {
			path := v.Binary.OutputFile
			if !filepath.IsAbs(path) && worktree != "" {
				path = filepath.Join(worktree, path)
			}
			b, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				return nil, true
			}
			data = string(b)
		}
		return parseSARIF(data), true
	case v.Binary.FailRegex != "":
		return extractRegexFindings(v.Binary.FailRegex, stdout+"\n"+stderr), true
	default:
		return nil, false
	}
}

// sarifLog is the minimal subset of the SARIF 2.1.0 schema we read: each run's
// results with their level, message, and first physical location.
type sarifLog struct {
	Runs []struct {
		Results []struct {
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

// parseSARIF maps each SARIF result to a finding. Returns nil on unparseable
// input so the caller falls back to the exit-code floor.
func parseSARIF(data string) []Finding {
	var log sarifLog
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &log); err != nil {
		return nil
	}
	var out []Finding
	for _, run := range log.Runs {
		for _, res := range run.Results {
			f := Finding{Reason: res.Message.Text, Distance: sarifLevelDistance(res.Level)}
			if len(res.Locations) > 0 {
				loc := res.Locations[0].PhysicalLocation
				f.Path = loc.ArtifactLocation.URI
				f.Line = loc.Region.StartLine
			}
			out = append(out, f)
		}
	}
	return out
}

// sarifLevelDistance is the fixed severity mapping (per the spec decision):
// error 1.0, warning 0.5, note 0.25; anything else defaults to warning.
func sarifLevelDistance(level string) float64 {
	switch strings.ToLower(level) {
	case "error":
		return 1.0
	case "note", "none":
		return 0.25
	case "warning":
		return 0.5
	default:
		return 0.5
	}
}

// extractRegexFindings runs a named-group regex over each line, emitting one
// finding per match. Recognised groups: file, line, reason (or message), symbol.
// Each finding takes the binary fail distance (1.0). Returns nil on a bad regex.
func extractRegexFindings(pattern, text string) []Finding {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	names := re.SubexpNames()
	var out []Finding
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := Finding{Distance: 1.0}
		for i, name := range names {
			if i == 0 || name == "" || i >= len(m) {
				continue
			}
			switch name {
			case "file":
				f.Path = m[i]
			case "line":
				if n, err := strconv.Atoi(m[i]); err == nil {
					f.Line = n
				}
			case "reason", "message":
				f.Reason = m[i]
			case "symbol":
				f.Symbol = m[i]
			}
		}
		out = append(out, f)
	}
	return out
}

func (v Verifier) prepare(ctx context.Context, s Session) ([]byte, context.Context, context.CancelFunc, error) {
	timeout := v.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	subCtx, cancel := context.WithTimeout(ctx, timeout)

	s.VerifierName = v.Name
	stdinJSON, err := json.Marshal(s)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("marshal session: %w", err)
	}
	return append(stdinJSON, '\n'), subCtx, cancel, nil
}

func runSubprocess(ctx context.Context, argv []string, stdin []byte, extraEnv []string, dir string) (string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	if dir != "" {
		// Pin the subprocess to the session-anchored worktree so verifier
		// git commands evaluate the right tree regardless of where the
		// daemon happens to be running. Empty falls through to the
		// daemon's own cwd (preserves pre-anchor behaviour and tests).
		cmd.Dir = dir
	}
	if len(extraEnv) > 0 {
		env := os.Environ()
		// macOS git calls confstr(_CS_DARWIN_USER_TEMP_DIR) when TMPDIR is unset
		// and intermittently fails with EIO, polluting stderr with a benign
		// warning. Setting TMPDIR explicitly skips the failing syscall.
		if os.Getenv("TMPDIR") == "" {
			env = append(env, "TMPDIR="+os.TempDir())
		}
		cmd.Env = append(env, extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// WaitDelay bounds the post-cancellation drain. Without it, cmd.Wait
	// can hang indefinitely if the child dies but the I/O-copying goroutines
	// Go starts for cmd.Stdin/Stdout/Stderr stay blocked on pipes the kernel
	// hasn't yet torn down. KillBatch + flaky tests on macOS observed this.
	cmd.WaitDelay = 500 * time.Millisecond

	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), nil
}

func verifierEnv(s Session) []string {
	return []string{
		"SESSION_BASE_REF=" + s.SessionBaseRef,
		"SESSION_WORKTREE=" + s.SessionWorktree,
		"SIDEKICK_VERIFIER=1", // prevents Sidekick hooks from overriding the main session goal
	}
}

func parseCommandResult(name, stdout string) (Result, error) {
	// Fast path: verifier emits a single JSON line as its last non-empty line.
	if line := lastNonEmptyLine(stdout); line != "" {
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			return promoteOK(finalizeResult(r)), nil
		}
	}
	// Robust path: scan for any top-level JSON object that looks like a result
	// (a "findings" or "distance" key). Tolerates trailing log lines and JSON
	// output mixed with prose.
	if obj := findLastResultObject(stdout); obj != "" {
		var r Result
		if err := json.Unmarshal([]byte(obj), &r); err == nil {
			return promoteOK(finalizeResult(r)), nil
		}
	}
	if strings.TrimSpace(stdout) == "" {
		return Result{}, fmt.Errorf("verifier %s produced no stdout", name)
	}
	return Result{}, fmt.Errorf("verifier %s bad json: %q", name, lastNonEmptyLine(stdout))
}

func parseAgentResult(name, agent, stdout string) (Result, error) {
	result := stdout
	var usage *UsageInfo
	if strings.EqualFold(agent, "claude") {
		// Claude --output-format=json wraps the model's output in an envelope
		// alongside usage telemetry. We unwrap to read the actual response,
		// and harvest token counts as a side-effect for cost reporting.
		var envelope struct {
			Result       string  `json:"result"`
			TotalCostUSD float64 `json:"total_cost_usd"`
			DurationMS   int64   `json:"duration_ms"`
			NumTurns     int     `json:"num_turns"`
			SessionID    string  `json:"session_id"`
			Usage        struct {
				InputTokens              int    `json:"input_tokens"`
				OutputTokens             int    `json:"output_tokens"`
				CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
				ServiceTier              string `json:"service_tier"`
			} `json:"usage"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err == nil && envelope.Result != "" {
			result = envelope.Result
			usage = &UsageInfo{
				Model:        envelope.Model,
				InputTokens:  envelope.Usage.InputTokens,
				OutputTokens: envelope.Usage.OutputTokens,
				CacheReads:   envelope.Usage.CacheReadInputTokens,
				CacheWrites:  envelope.Usage.CacheCreationInputTokens,
				CostUSD:      envelope.TotalCostUSD,
				DurationMS:   envelope.DurationMS,
			}
		}
	}
	obj := findLastResultObject(result)
	if obj == "" {
		obj = findLastResultObject(stdout)
	}
	if obj == "" {
		return Result{
			Status: ipc.StatusUnknown,
			Reason: fmt.Sprintf("%s could not parse agent output", name),
			Usage:  usage,
		}, nil
	}
	var r Result
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return Result{
			Status: ipc.StatusUnknown,
			Reason: fmt.Sprintf("%s could not parse agent output", name),
			Usage:  usage,
		}, nil
	}
	r.Usage = usage
	return promoteOK(finalizeResult(r)), nil
}

// promoteOK fills in a missing Status field. Verifiers may explicitly set
// Status="unknown" to signal "I ran but cannot score this run" (e.g. tooling
// missing); leave any non-empty value alone.
func promoteOK(r Result) Result {
	if r.Status == "" {
		r.Status = ipc.StatusOK
	}
	return r
}

// clamp01 constrains d to the [0, 1] distance scale.
func clamp01(d float64) float64 {
	if d < 0 {
		return 0
	}
	if d > 1 {
		return 1
	}
	return d
}

// rollUpFindings reduces findings to their worst (max) distance — the roll-up
// the compass scalar is derived from. Caller guarantees a non-empty slice.
func rollUpFindings(findings []Finding) (float64, string) {
	maxIdx := 0
	for i := range findings {
		if findings[i].Distance > findings[maxIdx].Distance {
			maxIdx = i
		}
	}
	return findings[maxIdx].Distance, findings[maxIdx].Reason
}

// finalizeResult reconciles a parsed result's two grains: findings drive the
// scalar via roll-up, while a legacy scalar with friction (distance > 0) is
// wrapped as one tree-global finding so back-compat output still produces a row.
func finalizeResult(r Result) Result {
	for i := range r.Findings {
		r.Findings[i].Distance = clamp01(r.Findings[i].Distance)
	}
	if len(r.Findings) > 0 {
		r.Distance, r.Reason = rollUpFindings(r.Findings)
		return r
	}
	r.Distance = clamp01(r.Distance)
	if r.Distance > 0 {
		r.Findings = []Finding{{Distance: r.Distance, Reason: r.Reason}}
	} else if r.Reason == "" {
		// A clean pass with no findings still needs a human-readable reason so
		// the compass shows why, not a blank cell.
		r.Reason = "no issues found for this lens"
	}
	return r
}

// DefaultFindingsCap bounds findings stored per run so a noisy linter can't dump
// thousands of rows; the lowest-distance overflow folds into one "+K more" marker.
const DefaultFindingsCap = 50

// normalizeFindingPath returns a clean repo-relative key for p against worktree.
// A path that escapes the worktree (or can't be resolved) returns "" so it is
// stored as a tree-global finding rather than under a bad key.
func normalizeFindingPath(worktree, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		if worktree == "" {
			return ""
		}
		rel, err := filepath.Rel(worktree, p)
		if err != nil {
			return ""
		}
		p = rel
	}
	p = filepath.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(p)
}

// prepareFindings normalizes finding paths and applies the cap, keeping the
// highest-distance findings and folding the remainder into one "+K more" marker.
func prepareFindings(findings []Finding, worktree string, cap int) []Finding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]Finding, len(findings))
	for i, f := range findings {
		f.Path = normalizeFindingPath(worktree, f.Path)
		out[i] = f
	}
	if cap > 0 && len(out) > cap {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Distance > out[j].Distance })
		remainder := out[cap:]
		marker := Finding{
			Distance: remainder[0].Distance,
			Reason:   fmt.Sprintf("+%d more findings beyond the cap of %d", len(remainder), cap),
		}
		out = append(out[:cap:cap], marker)
	}
	return out
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// findLastResultObject scans s left-to-right for top-level JSON object literals
// and returns the last one that looks like a verifier result — it contains a
// "findings" or "distance" key. The scanner is brace-aware and string-aware, so
// braces inside JSON strings (e.g. inside reason text) do not break extraction.
// Returns "" if none.
//
// This replaces the prior regex `\{[^{}]*"distance"[^{}]*\}` which silently
// dropped any object containing nested braces or quoted text containing
// braces — a class of false negatives that bucketed valid output as
// "could-not-parse" and forced agents into a fabricated 0.5 score.
func findLastResultObject(s string) string {
	const distanceKey = `"distance"`
	const findingsKey = `"findings"`
	var best string
	depth := 0
	start := -1
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				candidate := s[start : i+1]
				// Cheap pre-check: look for either literal key. Won't false-match
				// inside strings because we tracked them above.
				if strings.Contains(candidate, findingsKey) || strings.Contains(candidate, distanceKey) {
					best = candidate
				}
				start = -1
			}
		}
	}
	return best
}

// BuildAgentPrompt builds the native agent verifier prompt.
func BuildAgentPrompt(v Verifier, s Session) (string, error) {
	body, err := skillBody(v.Agent.Skill)
	if err != nil {
		return "", err
	}
	name := s.VerifierName
	if name == "" {
		name = v.Name
	}
	files := "<none>"
	if len(s.ChangedFiles) > 0 {
		files = strings.Join(s.ChangedFiles, ", ")
	}
	goal := s.Goal
	if goal == "" {
		goal = "<no goal set>"
	}
	base := s.SessionBaseRef
	if base == "" {
		base = "<unset; fall back to HEAD>"
	}
	worktree := s.SessionWorktree
	if worktree == "" {
		worktree = "<unset; fall back to cwd>"
	}
	return fmt.Sprintf(`%s

---

## Session context

Verifier name: %s
Active goal: %s
Session base ref ($SESSION_BASE_REF): %s
Session worktree ($SESSION_WORKTREE): %s
Recently changed files (last write batch, for orientation only — score the cumulative diff, not this list): %s

## How to evaluate

You evaluate the cumulative work in the current session — every change
since the session started, not just the most recent edit — through your
lens (above) against the active goal.

All git commands MUST target the session worktree explicitly via "git
-C $SESSION_WORKTREE …", because the agent may be operating in a
worktree separate from where your subprocess was launched. Running git
without -C will evaluate the wrong tree and produce a misleading score.

1. Run "git -C $SESSION_WORKTREE diff $SESSION_BASE_REF --stat" to size
   the change.
2. Run "git -C $SESSION_WORKTREE diff $SESSION_BASE_REF" to read
   cumulative changes. For large diffs, scope by directory or filetype
   relevant to your lens (e.g.
   "git -C $SESSION_WORKTREE diff $SESSION_BASE_REF -- internal/auth/").
3. Run "git -C $SESSION_WORKTREE status --porcelain" to find untracked
   files; read any that look substantive — they are part of the session
   too. Use absolute paths under $SESSION_WORKTREE when reading them.
4. Score the resulting state, not the volume of work. A small, well-
   placed change should score better than a large, sprawling one.

## Output contract (Sidekick verifier mode)

After your evaluation, output exactly one final line of JSON, with no
other text on that line:

{"reason": "<one short sentence: your overall judgment>", "findings": [{"path": "<repo-relative path under $SESSION_WORKTREE, or null if tree-wide>", "distance": <number 0.0..1.0>, "reason": "<one short sentence>"}]}

- Always set the top-level "reason" to a one-sentence overall judgment — even
  when there are no findings — so the compass always shows why.
- Emit one finding per file you can attribute a problem to. Paths MUST be
  relative to $SESSION_WORKTREE.
- If a judgment is genuinely tree-wide (e.g. the test suite does not pass),
  emit a single finding with "path": null.
- An empty findings array means the goal is met for your lens; still give the
  top-level reason (e.g. "comments are concise and purposeful").
- Reuse the score anchors below for each finding's distance.

### Score anchors (use these — don't invent your own scale)

Quantize to one of these five anchor points unless you have strong
evidence for a value in between. Anchored scoring keeps the compass
comparable across runs and across verifiers — drifting freely on a
0..1 scale produces noise the agent cannot act on.

- 0.00 — Goal fully satisfied for your dimension; nothing meaningful
  to improve from your perspective.
- 0.25 — Minor friction. The shape is right; small polish remaining.
  Agent should keep moving toward the goal, not pivot.
- 0.50 — A real concern. The cumulative work is on the right track
  but has a specific weakness the agent should address before the
  next milestone.
- 0.75 — Blocking issue. The current trajectory will produce a result
  that fails your dimension; the agent should change plan now.
- 1.00 — Goal contradicted, or no diff to evaluate. The session has
  not made progress (or has regressed) on your dimension.

If you genuinely cannot evaluate (tooling missing, no diff at all,
prerequisite step not yet done), output:

{"distance": 0.0, "reason": "<why>", "status": "unknown"}

The "status":"unknown" tag tells Sidekick to keep the prior distance and
flag the row as not-yet-evaluable, instead of fabricating a score.

### Reason field

Each finding's reason is the single most load-bearing observation — what
should change the agent's next decision — not a summary of what changed.

- No commentary after the JSON line.
`, body, name, goal, base, worktree, files), nil
}

func skillBody(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read llm.skill %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return string(raw), nil
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return string(raw), nil
}

func agentCommand(c AgentConfig) ([]string, error) {
	agent := resolveAgent(c.Agent)
	if agent == "custom" {
		if len(c.Custom.Command) == 0 {
			return nil, errors.New("agent: custom requires llm.custom.command")
		}
		return renderCustomCommand(c)
	}
	switch agent {
	case "claude":
		args := []string{
			"claude", "-p",
			"--output-format", "json",
			"--disable-slash-commands",
			"--strict-mcp-config",
			"--mcp-config", `{"mcpServers":{}}`,
		}
		if c.Model != "" {
			args = append(args, "--model", c.Model)
		}
		if c.Thinking != "" {
			args = append(args, "--effort", c.Thinking)
		}
		baselineTools := []string{
			// The runtime prompt mandates "git -C $SESSION_WORKTREE …" so
			// the verifier reads the session worktree, not the daemon cwd.
			// Without this entry claude's prefix-match allowlist blocks
			// every git command the prompt asks for.
			"Bash(git -C:*)",
			"Bash(git diff:*)",
			"Bash(git diff)",
			"Bash(git status:*)",
			"Bash(git log:*)",
			"Bash(git show:*)",
			"Bash(git ls-files:*)",
			"Bash(git rev-parse:*)",
			"Read", "Grep", "Glob",
		}
		args = append(args,
			"--allowedTools",
			strings.Join(mergeAllowedTools(baselineTools, c.AllowedTools), ","),
		)
		return args, nil
	case "codex":
		args := []string{"codex", "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules"}
		if c.Model != "" {
			args = append(args, "--model", c.Model)
		}
		if c.Thinking != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", c.Thinking))
		}
		args = append(args, "--sandbox", "read-only", "-")
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported agent.agent %q", c.Agent)
	}
}

// mergeAllowedTools appends extras onto baseline preserving baseline
// order; extras are deduped (case-sensitive) against the combined set so
// the same tool can't appear twice on the command line. Verifier authors
// can widen the allowlist but not narrow it — baseline entries always
// survive.
func mergeAllowedTools(baseline, extras []string) []string {
	if len(extras) == 0 {
		return baseline
	}
	seen := make(map[string]struct{}, len(baseline)+len(extras))
	out := make([]string, 0, len(baseline)+len(extras))
	for _, t := range baseline {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, t := range extras {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func resolveAgent(agent string) string {
	agent = strings.ToLower(agent)
	if agent == "" {
		return "claude"
	}
	return agent
}

// renderCustomCommand substitutes {{.Model}}, {{.Thinking}}, {{.Skill}}
// into each element of the user-supplied argv via text/template.
// Substitution happens per-arg so users can write argv pieces like
// "--model={{.Model}}" without worrying about quoting.
func renderCustomCommand(c AgentConfig) ([]string, error) {
	data := struct {
		Model    string
		Thinking string
		Skill    string
	}{Model: c.Model, Thinking: c.Thinking, Skill: c.Skill}

	out := make([]string, 0, len(c.Custom.Command))
	for i, raw := range c.Custom.Command {
		// Skip parsing for args that don't contain a template — saves
		// allocations and avoids surprising failures on stray braces.
		if !strings.Contains(raw, "{{") {
			out = append(out, raw)
			continue
		}
		t, err := template.New(fmt.Sprintf("arg%d", i)).Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("custom agent argv[%d] template: %w", i, err)
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("custom agent argv[%d] execute: %w", i, err)
		}
		out = append(out, b.String())
	}
	return out, nil
}
