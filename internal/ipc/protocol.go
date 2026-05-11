// Package ipc defines the line-delimited JSON protocol spoken between the
// long-running `hud start` daemon and its short-lived peers (`hud hook`,
// `hud mcp`, `hud goal`).
package ipc

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSockRel = ".hud/sock"
	envSock        = "HUD_SOCK"
)

// SocketPath returns the daemon socket path. Defaults to a repo-scoped
// path under $HOME/.hud/sockets/<fingerprint>.sock so multiple projects
// can run concurrent HUD daemons. Falls back to the legacy single-socket
// $HOME/.hud/sock when the cwd is not a git repository (avoids breaking
// the demo path that runs without a project).
//
// Override with $HUD_SOCK for tests and unusual deployments.
func SocketPath() (string, error) {
	if p := os.Getenv(envSock); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if fp := repoFingerprint(); fp != "" {
		return filepath.Join(home, ".hud", "sockets", fp+".sock"), nil
	}
	return filepath.Join(home, defaultSockRel), nil
}

// repoFingerprint returns a stable short hash of the git toplevel of the
// current working directory, or "" when not in a repo. Worktrees of the
// same repo share a fingerprint via `git rev-parse --show-superproject-working-tree`
// fallback to --show-toplevel.
func repoFingerprint() string {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:12]
}

// Request is a single command sent to the daemon. Source is an optional
// hint that lets the daemon distinguish MCP-originated traffic from CLI/hook
// traffic so the TUI header can show separate "last socket" and "last MCP"
// timestamps. Senders may leave it empty.
type Request struct {
	Type   string          `json:"type"`
	Source string          `json:"source,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Known Request.Source tags. The daemon treats anything else as non-MCP.
const SourceMCP = "mcp"

// Response wraps every reply.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Known request types.
const (
	TypeWrite   = "write"   // {file: string}                   -> {}
	TypeGoal    = "goal"    // {goal: string}                   -> {}
	TypeStatus  = "status"  // {}                               -> StatusData
	TypeExplain = "explain" // {verifier: string}               -> ExplainData
	TypePing    = "ping"    // {}                               -> {pong: true}
)

// WriteData is the payload for TypeWrite.
type WriteData struct {
	File string `json:"file"`
}

// GoalData is the payload for TypeGoal.
type GoalData struct {
	Goal string `json:"goal"`
}

// ExplainData is the payload for TypeExplain.
type ExplainData struct {
	Verifier string `json:"verifier"`
}

// Verifier outcome statuses. Distinct from the Running flag (which is purely
// a "currently executing" indicator). Status reflects the last completed run.
//
//   - StatusPending: never produced a result yet (initial state).
//   - StatusOK: subprocess completed and emitted a parseable score.
//   - StatusError: subprocess failed (non-zero exit, timeout, missing binary).
//   - StatusUnknown: agent ran but its output could not be parsed; previous
//     distance is preserved so the compass does not lie.
//   - StatusStale: cached result predates a config edit or other invalidation.
//   - StatusDisabled: user toggled the verifier off; not running future batches.
const (
	StatusPending  = "pending"
	StatusOK       = "ok"
	StatusError    = "error"
	StatusUnknown  = "unknown"
	StatusStale    = "stale"
	StatusDisabled = "disabled"
)

// VerifierStatus is the per-verifier state surfaced to clients.
//
// Status disambiguates the cases that previously collapsed onto a magic
// distance value (parse failure → 0.5, subprocess error → 1.0). Clients
// should prefer Status over inferring outcome from distance/reason text.
type VerifierStatus struct {
	Name       string         `json:"name"`
	Direction  string         `json:"direction"`
	Distance   float64        `json:"distance"`
	Reason     string         `json:"reason"`
	Status     string         `json:"status,omitempty"`
	ComputedAt time.Time      `json:"computed_at"`
	Running    bool           `json:"running"`
	Disabled   bool           `json:"disabled,omitempty"`
	History    []HistoryPoint `json:"history,omitempty"`
	LastUsage  *AgentUsage    `json:"last_usage,omitempty"`
	Config     VerifierConfig `json:"config,omitempty"`
}

// HistoryPoint is one prior verifier result, kept in a small ring buffer per
// verifier so the TUI can render a sparkline and agents can read trends via
// hud_explain. Status is denormalised (pending/error rows are still kept so
// the user sees flakiness, not just successful runs).
type HistoryPoint struct {
	Distance   float64   `json:"distance"`
	Status     string    `json:"status,omitempty"`
	ComputedAt time.Time `json:"computed_at"`
}

// AgentUsage records token counts and (optionally) cost for the most recent
// agent verifier run. Empty when the agent CLI did not surface usage data.
type AgentUsage struct {
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CacheReads   int     `json:"cache_reads,omitempty"`
	CacheWrites  int     `json:"cache_writes,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
}

// VerifierConfig is the resolved hud.yaml metadata surfaced with verifier
// status so clients can explain what is running without reparsing config.
type VerifierConfig struct {
	Type        string              `json:"type,omitempty"`
	Command     []string            `json:"command,omitempty"`
	Timeout     string              `json:"timeout,omitempty"`
	Agent       string              `json:"agent,omitempty"`
	Model       string              `json:"model,omitempty"`
	Thinking    string              `json:"thinking,omitempty"`
	Skill       string              `json:"skill,omitempty"`
	PassReason  string              `json:"pass_reason,omitempty"`
	FailReason  string              `json:"fail_reason,omitempty"`
	Permissions VerifierPermissions `json:"permissions,omitempty"`
	Source      string              `json:"source,omitempty"` // "local" | "remote"
	SourceURL   string              `json:"source_url,omitempty"`
	SHA256      string              `json:"sha256,omitempty"`
}

// VerifierPermissions is an advisory declaration of what a verifier intends
// to do at runtime. v0.1 enforcement is informational — values are surfaced
// in the TUI on first run so the user can decline before execution. Future
// versions may wire them into platform sandboxes (sandbox-exec, landlock).
type VerifierPermissions struct {
	Network    bool     `json:"network,omitempty"`
	Filesystem string   `json:"filesystem,omitempty"` // "read-only" | "read-write" | "none"
	Env        []string `json:"env,omitempty"`        // env var allowlist
}

// StatusReply is returned by TypeStatus.
type StatusReply struct {
	Goal            string           `json:"goal"`
	Verifiers       []VerifierStatus `json:"verifiers"`
	OverallDistance float64          `json:"overall_distance"`
	Version         string           `json:"version,omitempty"`
	LastSocketAt    time.Time        `json:"last_socket_at"`
	LastMCPAt       time.Time        `json:"last_mcp_at"`
}

// Send dials the daemon, writes one request, reads one response, and closes.
func Send(req Request) (Response, error) {
	sock, err := SocketPath()
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("dial daemon: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// MarshalData is a convenience for setting Request.Data from any struct.
func MarshalData(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}
