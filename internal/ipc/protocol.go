// Package ipc defines the line-delimited JSON protocol spoken between the
// long-running `sidekick start` daemon and its short-lived peers (`sidekick hook`,
// `sidekick mcp`, `sidekick goal`).
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
	defaultSockRel = ".sidekick/sock"
	envSock        = "SIDEKICK_SOCK"
	envTelemetryDB = "SIDEKICK_TELEMETRY_DB"
)

// SocketPath returns the daemon socket path for the process cwd. See
// SocketPathFor for the full doc; this is the legacy parameterless entry
// point used by daemon-side code (`sidekick start`, the menubar) and short-lived
// CLI peers (`sidekick goal`, `sidekick hook`) that inherit the operator's real
// shell cwd.
func SocketPath() (string, error) {
	return SocketPathFor("")
}

// SocketPathFor returns the daemon socket path resolved from the supplied
// caller cwd. When cwd is empty the fingerprint is taken from the current
// process cwd (legacy behaviour). When non-empty the fingerprint is taken
// from `git -C cwd rev-parse --git-common-dir`, which is the right thing
// for the MCP server: its process cwd is whatever the agent harness had
// at spawn time and goes stale the moment the agent moves between
// worktrees, but the agent itself always knows where it is and can pass
// that path through.
//
// Falls back to the legacy single-socket $HOME/.sidekick/sock when no git
// common dir can be resolved (preserves the demo path that runs outside a
// repository).
//
// Override with $SIDEKICK_SOCK for tests and unusual deployments.
func SocketPathFor(cwd string) (string, error) {
	if p := os.Getenv(envSock); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if fp := repoFingerprintFor(cwd); fp != "" {
		return filepath.Join(home, ".sidekick", "sockets", fp+".sock"), nil
	}
	return filepath.Join(home, defaultSockRel), nil
}

// TelemetryDBPath returns the telemetry SQLite path for the repo resolved from
// cwd, mirroring the per-repo-fingerprint socket naming. Every worktree of a
// repo shares one database — the daemon is the single writer — so collection
// stays coherent no matter which worktree drove a session. Falls back to a
// single default DB outside a repo. Override with $SIDEKICK_TELEMETRY_DB.
func TelemetryDBPath(cwd string) (string, error) {
	if p := os.Getenv(envTelemetryDB); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	fp := repoFingerprintFor(cwd)
	if fp == "" {
		fp = "default"
	}
	return filepath.Join(home, ".sidekick", "telemetry", fp+".db"), nil
}

// RepoFingerprint returns the stable short hash identifying the git repo
// resolved from cwd, the same value the per-repo socket and telemetry DB paths
// are keyed on. Used to resolve a repo to its backend project so every worktree
// of a repo maps to one project. Returns "" when cwd is not in a git repo.
func RepoFingerprint(cwd string) string {
	return repoFingerprintFor(cwd)
}

// repoFingerprintFor returns a stable short hash identifying the git repo
// resolved from `cwd` (when non-empty) or the current process cwd. Returns
// "" when not in a repo.
//
// The fingerprint hashes the absolute `git rev-parse --git-common-dir`
// path, which is the *shared* .git directory across a repo and all of its
// linked worktrees. This means the main repo and every worktree of it
// collapse onto a single socket — `sidekick start` can run in the trunk while
// an agent in a worktree (or a `sidekick hook` fired from that worktree)
// transparently dials the same daemon. The daemon routes each request to a
// per-worktree session inside that shared socket.
//
// We intentionally do *not* use `--show-toplevel`: that returns the
// worktree's own path and produced a distinct fingerprint per worktree,
// stranding worktree-side clients whenever the Sidekick daemon lived in the
// trunk (or vice versa, e.g. after a Sidekick restart from a different cwd).
func repoFingerprintFor(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return ""
	}
	// `--git-common-dir` returns a path relative to the command's working
	// directory (e.g. ".git" when run inside the toplevel) when invoked
	// from the main repo, but an *absolute realpath* when invoked from a
	// linked worktree. Normalize both into an absolute, symlink-free
	// canonical path so the trunk and its worktrees hash identically —
	// otherwise the symlink layer that macOS imposes on /var/folders,
	// /tmp, etc. silently produces two different fingerprints for the
	// same .git directory.
	abs := raw
	if !filepath.IsAbs(abs) {
		base := cwd
		if base == "" {
			base, _ = os.Getwd()
		}
		abs = filepath.Join(base, raw)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	abs = filepath.Clean(abs)
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// Request is a single command sent to the daemon. Source is an optional
// hint that lets the daemon distinguish MCP-originated traffic from CLI/hook
// traffic so the TUI header can show separate "last socket" and "last MCP"
// timestamps. Senders may leave it empty.
type Request struct {
	Type   string          `json:"type"`
	Source string          `json:"source,omitempty"`
	Cwd    string          `json:"cwd,omitempty"`
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
//
// Worktree and BaseRef are legacy optional fields. Current callers should
// route with Request.Cwd; the daemon resolves that cwd to the per-worktree
// session and captures the anchor itself.
type GoalData struct {
	Goal     string `json:"goal"`
	Worktree string `json:"worktree,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`
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
// verifier so clients and agents can read trends via sidekick_explain. Status is
// denormalised (pending/error rows are still kept so the user sees flakiness,
// not just successful runs).
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

// VerifierConfig is the resolved sidekick.yaml metadata surfaced with verifier
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
//
// AnyRunning and RunningVerifiers surface in-flight verifier work at the top
// level so MCP agents can recognise that the displayed Distance/Reason fields
// are stale and need to be re-read after a brief wait. The per-verifier
// Running bool carries the same information row-by-row.
type StatusReply struct {
	Goal              string           `json:"goal"`
	GoalLocked        bool             `json:"goal_locked,omitempty"`
	Verifiers         []VerifierStatus `json:"verifiers"`
	OverallDistance   float64          `json:"overall_distance"`
	AnyRunning        bool             `json:"any_running"`
	RunningVerifiers  []string         `json:"running_verifiers,omitempty"`
	Version           string           `json:"version,omitempty"`
	LastSocketAt      time.Time        `json:"last_socket_at"`
	LastMCPAt         time.Time        `json:"last_mcp_at"`
	Worktree          string           `json:"worktree,omitempty"`
	SessionBaseRef    string           `json:"session_base_ref,omitempty"`
	SessionCount      int              `json:"session_count,omitempty"`
	DisplayedWorktree string           `json:"displayed_worktree,omitempty"`
	Sessions          []SessionSummary `json:"sessions,omitempty"`
}

// SessionSummary is a compact row for UIs that need to switch between
// per-worktree sessions without fetching every verifier detail.
type SessionSummary struct {
	Worktree     string    `json:"worktree"`
	Label        string    `json:"label,omitempty"`
	Goal         string    `json:"goal,omitempty"`
	AnyRunning   bool      `json:"any_running,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	Displayed    bool      `json:"displayed,omitempty"`
}

// GoalAck is returned by TypeGoal so the caller (MCP server or CLI) can
// see what the daemon actually retained. When Locked is true the daemon
// rejected the goal update (a startup-locked goal is in force) and Goal
// is the locked value the agent should keep using.
type GoalAck struct {
	Goal   string `json:"goal"`
	Locked bool   `json:"locked,omitempty"`
}

// Send dials the daemon using the process cwd to pick the socket. Use
// SendFrom from contexts where the process cwd is stale (notably the MCP
// server, whose cwd is frozen at agent-harness spawn time).
func Send(req Request) (Response, error) {
	return SendFrom(req, "")
}

// SendFrom dials the daemon, writes one request, reads one response, and
// closes. The socket is resolved from `cwd` (or the process cwd when cwd
// is empty) — see SocketPathFor for the rationale.
func SendFrom(req Request, cwd string) (Response, error) {
	cwd = strings.TrimSpace(cwd)
	if req.Cwd == "" && cwd != "" {
		req.Cwd = cwd
	}
	sock, err := SocketPathFor(cwd)
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
