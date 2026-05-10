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
	"regexp"
	"strings"
	"time"
)

// Session is the context piped to verifier subprocesses on stdin.
//
// SessionBaseRef is the git SHA `HEAD` pointed at when `hud start` ran;
// LLM-backed verifiers diff the working tree against it to score
// cumulative session work rather than the last debounced write.
type Session struct {
	Goal           string   `json:"goal"`
	SessionBaseRef string   `json:"session_base_ref,omitempty"`
	RecentDiff     string   `json:"recent_diff,omitempty"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
	LastMessages   []string `json:"last_messages,omitempty"`
	VerifierName   string   `json:"verifier_name"`
}

// Result is what a verifier subprocess prints on stdout.
type Result struct {
	Distance float64 `json:"distance"`
	Reason   string  `json:"reason"`
}

// Verifier type names.
const (
	TypeCommand = "command"
	TypeAgent   = "agent"
	TypeBinary  = "binary"
)

// Verifier is a single configured verifier.
type Verifier struct {
	Name      string
	Direction string // N, NE, E, SE, S, SW, W, NW
	Type      string
	Disabled  bool
	Command   []string
	Timeout   time.Duration // 0 → 60s default
	Agent     AgentConfig
	Binary    BinaryConfig
}

// AgentConfig configures a native agent-backed verifier.
type AgentConfig struct {
	Agent    string
	Model    string
	Thinking string
	Skill    string
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

	stdout, stderr, err := runSubprocess(subCtx, v.Command, stdinJSON, nil)
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

	stdout, stderr, err := runSubprocess(subCtx, cmd, []byte(prompt), []string{
		"SESSION_BASE_REF=" + s.SessionBaseRef,
		"HUD_VERIFIER=1", // prevents HUD hooks from overriding the main session goal
	})
	if err != nil {
		return Result{}, fmt.Errorf("agent verifier %s exited with error: %w (stderr: %s)",
			v.Name, err, strings.TrimSpace(stderr))
	}
	return parseAgentResult(v.Name, resolveAgent(v.Agent.Agent), stdout)
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

	stdout, stderr, err := runSubprocess(subCtx, v.Binary.Command, stdinJSON, []string{
		"SESSION_BASE_REF=" + s.SessionBaseRef,
	})
	if err == nil {
		reason := v.Binary.PassReason
		if reason == "" {
			reason = "passed"
		}
		return Result{Distance: 0, Reason: reason}, nil
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
	return Result{Distance: 1, Reason: reason}, nil
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

func runSubprocess(ctx context.Context, argv []string, stdin []byte, extraEnv []string) (string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), stderr.String(), err
	}
	return stdout.String(), stderr.String(), nil
}

func parseCommandResult(name, stdout string) (Result, error) {
	line := lastNonEmptyLine(stdout)
	if line == "" {
		return Result{}, fmt.Errorf("verifier %s produced no stdout", name)
	}
	var r Result
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		return Result{}, fmt.Errorf("verifier %s bad json (%q): %w", name, line, err)
	}
	return clampResult(r), nil
}

func parseAgentResult(name, agent, stdout string) (Result, error) {
	result := stdout
	if strings.EqualFold(agent, "claude") {
		var envelope struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope); err == nil && envelope.Result != "" {
			result = envelope.Result
		}
	}
	obj := lastDistanceObject(result)
	if obj == "" {
		obj = lastDistanceObject(stdout)
	}
	if obj == "" {
		return Result{Distance: 0.5, Reason: fmt.Sprintf("%s could not parse agent output", name)}, nil
	}
	var r Result
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return Result{Distance: 0.5, Reason: fmt.Sprintf("%s could not parse agent output", name)}, nil
	}
	return clampResult(r), nil
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

var distanceObjectPattern = regexp.MustCompile(`\{[^{}]*"distance"[^{}]*\}`)

func lastDistanceObject(s string) string {
	matches := distanceObjectPattern.FindAllString(s, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
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
	return fmt.Sprintf(`%s

---

## Session context

Verifier name: %s
Active goal: %s
Session base ref ($SESSION_BASE_REF): %s
Recently changed files (last write batch, for orientation only — score the cumulative diff, not this list): %s

## Output contract (HUD verifier mode)

After your evaluation, output exactly one final line of JSON, with no
other text on that line:

{"distance": <number 0.0..1.0>, "reason": "<one short sentence>"}

- 0.0 = the goal is fully satisfied through the cumulative session work.
- 1.0 = it is maximally unsatisfied.
- The reason is the single most load-bearing observation — what should
  change the agent's next decision — not a summary.
- No commentary after the JSON line.
`, body, name, goal, base, files), nil
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
