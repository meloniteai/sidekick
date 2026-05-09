// Package verifier defines the subprocess-backed verifier interface and runner.
//
// A Verifier is a command that reads a single JSON line of session context on
// stdin and writes a single JSON line of {distance, reason} on stdout. This
// keeps the implementation language-agnostic: an LLM-backed verifier wraps
// `claude -p`, a deterministic one wraps `go test -cover`, etc.
package verifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

// Verifier is a single configured verifier.
type Verifier struct {
	Name      string
	Direction string // N, NE, E, SE, S, SW, W, NW
	Command   []string
	Timeout   time.Duration // 0 → 60s default
}

// DefaultTimeout applies when Verifier.Timeout is zero.
const DefaultTimeout = 60 * time.Second

// Verify runs the verifier command, piping the session JSON to its stdin and
// parsing {distance, reason} from its stdout. The returned Result.Distance is
// clamped to [0, 1] before being returned.
func (v Verifier) Verify(ctx context.Context, s Session) (Result, error) {
	if len(v.Command) == 0 {
		return Result{}, errors.New("verifier has empty command")
	}
	timeout := v.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s.VerifierName = v.Name
	stdinJSON, err := json.Marshal(s)
	if err != nil {
		return Result{}, fmt.Errorf("marshal session: %w", err)
	}

	cmd := exec.CommandContext(subCtx, v.Command[0], v.Command[1:]...)
	cmd.Stdin = bytes.NewReader(append(stdinJSON, '\n'))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("verifier %s exited with error: %w (stderr: %s)",
			v.Name, err, strings.TrimSpace(stderr.String()))
	}

	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		return Result{}, fmt.Errorf("verifier %s produced no stdout", v.Name)
	}
	// Tolerate leading/trailing log lines: take the last non-empty line.
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		out = bytes.TrimSpace(out[i+1:])
	}

	var r Result
	if err := json.Unmarshal(out, &r); err != nil {
		return Result{}, fmt.Errorf("verifier %s bad json (%q): %w", v.Name, string(out), err)
	}
	if r.Distance < 0 {
		r.Distance = 0
	}
	if r.Distance > 1 {
		r.Distance = 1
	}
	return r, nil
}
