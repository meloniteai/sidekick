// Package ipc defines the line-delimited JSON protocol spoken between the
// long-running `hud start` daemon and its short-lived peers (`hud hook`,
// `hud mcp`, `hud goal`).
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultSockRel = ".hud/sock"
	envSock        = "HUD_SOCK"
)

// SocketPath returns the daemon socket path. Defaults to $HOME/.hud/sock,
// overridable via $HUD_SOCK.
func SocketPath() (string, error) {
	if p := os.Getenv(envSock); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, defaultSockRel), nil
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
	TypeWrite   = "write"    // {file: string}                   -> {}
	TypeGoal    = "goal"     // {goal: string}                   -> {}
	TypeStatus  = "status"   // {}                               -> StatusData
	TypeExplain = "explain"  // {verifier: string}               -> ExplainData
	TypePing    = "ping"     // {}                               -> {pong: true}
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

// VerifierStatus is the per-verifier state surfaced to clients.
type VerifierStatus struct {
	Name       string    `json:"name"`
	Direction  string    `json:"direction"`
	Distance   float64   `json:"distance"`
	Reason     string    `json:"reason"`
	ComputedAt time.Time `json:"computed_at"`
	Running    bool      `json:"running"`
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
