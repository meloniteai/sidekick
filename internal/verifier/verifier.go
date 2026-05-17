// Package verifier defines the subprocess-backed verifier interface and runner.
//
// HUD supports three verifier types:
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
	"strings"
	"text/template"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
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

// Result is what a verifier subprocess prints on stdout.
//
// Status is optional in the on-the-wire JSON; the runner promotes a missing
// value to ipc.StatusOK on success. Verifiers that genuinely cannot score
// (e.g. tooling missing, no diff to evaluate) should return Status="unknown"
// rather than fabricating a distance — the runner then preserves the
// previous distance instead of pretending the score moved.
type Result struct {
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
	// remote URL via `hud verifier add` (or an inline `source:` block in
	// hud.yaml). "local" / "" means an in-tree script.
	Source    string // "local" | "remote"
	SourceURL string
	SHA256    string
}

// Permissions is the advisory permission declaration carried with each
// verifier. v0.1 surfaces these in the TUI on first run for trust-on-first-use;
// future versions may map them onto sandbox-exec / landlock for enforcement.
type Permissions struct {
	Network    bool
	Filesystem string // "read-only" | "read-write" | "none"
	Env        []string
}

// AgentConfig configures a native agent-backed verifier.
type AgentConfig struct {
	Agent    string
	Model    string
	Thinking string
	Skill    string
	Custom   CustomAgent
}

// CustomAgent configures a user-supplied agent CLI invoked via templated
// argv. The command is text/template-substituted with {{.Model}},
// {{.Thinking}}, and {{.Skill}} per element before exec; stdin is fed
// the assembled prompt body when StdinFmt is "" or "prompt".
type CustomAgent struct {
	Command  []string
	StdinFmt string
}

// BinaryConfig configures a native pass/fail verifier.
type BinaryConfig struct {
	Command    []string
	PassReason string
	FailReason string
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

	stdout, stderr, err := runSubprocess(subCtx, v.Binary.Command, stdinJSON, verifierEnv(s), s.SessionWorktree)
	if err == nil {
		reason := v.Binary.PassReason
		if reason == "" {
			reason = "passed"
		}
		return Result{Distance: 0, Reason: reason, Status: ipc.StatusOK}, nil
	}
	reason := v.Binary.FailReason
	if reason == "" {
		reason = lastNonEmptyLine(stderr)
		if reason == "" {
			reason = lastNonEmptyLine(stdout)
		}
		if reason == "" {
			reason = err.Error()
		}
	}
	return Result{Distance: 1, Reason: reason, Status: ipc.StatusOK}, nil
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
		"HUD_VERIFIER=1", // prevents HUD hooks from overriding the main session goal
	}
}

func parseCommandResult(name, stdout string) (Result, error) {
	// Fast path: verifier emits a single JSON line as its last non-empty line.
	if line := lastNonEmptyLine(stdout); line != "" {
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err == nil {
			return promoteOK(clampResult(r)), nil
		}
	}
	// Robust path: scan for any top-level JSON object containing a "distance"
	// field. Tolerates trailing log lines and JSON output mixed with prose.
	if obj := findLastDistanceObject(stdout); obj != "" {
		var r Result
		if err := json.Unmarshal([]byte(obj), &r); err == nil {
			return promoteOK(clampResult(r)), nil
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
	obj := findLastDistanceObject(result)
	if obj == "" {
		obj = findLastDistanceObject(stdout)
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
	return promoteOK(clampResult(r)), nil
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

func clampResult(r Result) Result {
	if r.Distance < 0 {
		r.Distance = 0
	}
	if r.Distance > 1 {
		r.Distance = 1
	}
	return r
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

// findLastDistanceObject scans s left-to-right for top-level JSON object
// literals and returns the last one that contains a "distance" key. The
// scanner is brace-aware and string-aware, so braces inside JSON strings
// (e.g. inside reason text) do not break extraction. Returns "" if none.
//
// This replaces the prior regex `\{[^{}]*"distance"[^{}]*\}` which silently
// dropped any object containing nested braces or quoted text containing
// braces — a class of false negatives that bucketed valid output as
// "could-not-parse" and forced agents into a fabricated 0.5 score.
func findLastDistanceObject(s string) string {
	const distanceKey = `"distance"`
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
				// Cheap pre-check: look for the literal key. Won't false-match
				// inside strings because we tracked them above.
				if strings.Contains(candidate, distanceKey) {
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

## Output contract (HUD verifier mode)

After your evaluation, output exactly one final line of JSON, with no
other text on that line:

{"distance": <number 0.0..1.0>, "reason": "<one short sentence>"}

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

The "status":"unknown" tag tells HUD to keep the prior distance and
flag the row as not-yet-evaluable, instead of fabricating a score.

### Reason field

The reason is the single most load-bearing observation — what should
change the agent's next decision — not a summary of what changed.

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
		args = append(args,
			"--allowedTools",
			strings.Join([]string{
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
			}, ","),
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
