// Package mcp wires the read-only HUD tools onto an MCP stdio server.
//
// The server is a thin proxy: every tool call dials the running `hud start`
// daemon over its Unix socket and returns the JSON snapshot. It never runs
// verifiers itself — recomputation is exclusively triggered by file-write
// hooks per the MVP spec.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/uriahlevy/hud/internal/ipc"
)

// Run constructs the MCP server, wires the tools, and serves over stdio
// until ctx is canceled or stdin is closed.
func Run(ctx context.Context, version string) error {
	srv := server.NewMCPServer("hud", version)

	readOnly := []mcp.ToolOption{
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}

	// cwdArg is added to every tool so the agent can tell the MCP server
	// which worktree it is currently operating in. The MCP server's own
	// process cwd is whatever the agent harness had at spawn time and goes
	// stale the moment the agent moves between worktrees — without this
	// hint we can't resolve the repo's shared socket from a worktree or
	// route the request to the right per-worktree daemon session.
	cwdArg := mcp.WithString("cwd",
		mcp.Description(
			"Absolute path to the agent's current working directory (typically "+
				"the worktree the agent is editing in). Pass this on every call so "+
				"the HUD server can resolve the repo's shared daemon socket and "+
				"route to the right worktree session. One HUD daemon "+
				"serves a repo and all of its linked worktrees, so `hud start` may "+
				"live in the trunk while the agent works in a worktree. The MCP "+
				"server's own process cwd is frozen at session start and goes "+
				"stale when the agent switches worktrees. Omit only when not in a "+
				"git repo."),
	)

	srv.AddTool(
		mcp.NewTool("hud_status",
			append([]mcp.ToolOption{
				mcp.WithDescription(
					"Read the latest HUD verifier snapshot for the active session. " +
						"Returns the goal and each verifier's compass direction, " +
						"distance from goal (0=achieved, 1=far), and one-line reason. " +
						"Read-only: never triggers verifier recomputation. " +
						"When any_running is true (or running_verifiers is non-empty), " +
						"one or more verifiers are still computing; the distance and " +
						"reason fields reflect the previous run and may be stale. " +
						"Wait a few seconds and call hud_status again to read fresh " +
						"scores before acting on them."),
				cwdArg,
			}, readOnly...)...,
		),
		statusHandler,
	)

	srv.AddTool(
		mcp.NewTool("hud_explain",
			append([]mcp.ToolOption{
				mcp.WithDescription(
					"Read the detailed last-known state of a single HUD verifier by name. " +
						"Use this when hud_status shows a high distance and you want the " +
						"verifier's reason expanded for the agent's next decision. " +
						"If the returned running flag is true, the verifier is still " +
						"computing and distance/reason reflect the previous run; wait " +
						"a few seconds and re-query for fresh results."),
				mcp.WithString("verifier",
					mcp.Required(),
					mcp.Description("Verifier name (e.g. \"Architect\", \"Test\", \"Security\")"),
				),
				cwdArg,
			}, readOnly...)...,
		),
		explainHandler,
	)

	srv.AddTool(
		mcp.NewTool("hud_set_goal",
			mcp.WithDescription(
				"Set the active session goal that all HUD verifiers evaluate against. "+
					"Call this at the start of a task and whenever the goal materially shifts "+
					"(e.g. user pivots, sub-task begins). This only updates the goal; "+
					"file-write hooks trigger verifier recomputation."),
			mcp.WithString("goal",
				mcp.Required(),
				mcp.Description("One short sentence describing what the agent is currently trying to achieve."),
			),
			cwdArg,
			mcp.WithReadOnlyHintAnnotation(false),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithOpenWorldHintAnnotation(false),
		),
		setGoalHandler,
	)

	// ServeStdio blocks; we let it return on stdin close. The ctx is wired
	// for symmetry with future transports.
	_ = ctx
	return server.ServeStdio(srv)
}

// callerCwd extracts the optional "cwd" argument from a tool call. The
// MCP server passes this through to ipc.SendFrom and the anchor resolver
// so the agent's logical worktree wins over the MCP process's stale cwd.
func callerCwd(req mcp.CallToolRequest) string {
	cwd, _ := req.GetArguments()["cwd"].(string)
	return strings.TrimSpace(cwd)
}

func statusHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resp, err := ipc.SendFrom(ipc.Request{Type: ipc.TypeStatus, Source: ipc.SourceMCP}, callerCwd(req))
	if err != nil {
		return nil, fmt.Errorf("hud daemon unreachable (is `hud start` running?): %w", err)
	}
	var reply ipc.StatusReply
	if err := json.Unmarshal(resp.Data, &reply); err != nil {
		return nil, err
	}
	return mcp.NewToolResultJSON(reply)
}

func setGoalHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	goal, _ := args["goal"].(string)
	if goal == "" {
		return nil, errors.New("goal argument is required")
	}
	// The agent passes its current worktree via the cwd arg. Anchoring
	// from that path (not from the MCP process cwd, which is stale)
	// ensures the daemon re-points at the right worktree when the goal
	// moves, even if `hud start` was launched elsewhere (e.g. the trunk).
	cwd := callerCwd(req)
	worktree, baseRef := resolveSessionAnchor(cwd)
	data, err := ipc.MarshalData(ipc.GoalData{
		Goal:     goal,
		Worktree: worktree,
		BaseRef:  baseRef,
	})
	if err != nil {
		return nil, err
	}
	if _, err := ipc.SendFrom(ipc.Request{Type: ipc.TypeGoal, Source: ipc.SourceMCP, Data: data}, cwd); err != nil {
		return nil, fmt.Errorf("hud daemon unreachable (is `hud start` running?): %w", err)
	}
	return mcp.NewToolResultJSON(map[string]any{
		"goal":     goal,
		"worktree": worktree,
		"base_ref": baseRef,
	})
}

// resolveSessionAnchor returns the absolute worktree path and HEAD SHA
// resolved from `cwd` when non-empty, or the process cwd otherwise. Empty
// return values tell the daemon to leave the existing anchor untouched
// rather than overwriting it with garbage.
func resolveSessionAnchor(cwd string) (worktree, baseRef string) {
	gitCmd := func(args ...string) ([]byte, error) {
		c := exec.Command("git", args...)
		if cwd != "" {
			c.Dir = cwd
		}
		return c.Output()
	}
	top, err := gitCmd("rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	worktree = strings.TrimSpace(string(top))
	head, err := gitCmd("rev-parse", "HEAD")
	if err != nil {
		// Worktree resolves but no commits — return worktree alone so
		// the daemon still pins the right tree; base ref stays unset.
		return worktree, ""
	}
	return worktree, strings.TrimSpace(string(head))
}

func explainHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	name, _ := args["verifier"].(string)
	if name == "" {
		return nil, errors.New("verifier argument is required")
	}
	data, err := ipc.MarshalData(ipc.ExplainData{Verifier: name})
	if err != nil {
		return nil, err
	}
	resp, err := ipc.SendFrom(ipc.Request{Type: ipc.TypeExplain, Source: ipc.SourceMCP, Data: data}, callerCwd(req))
	if err != nil {
		return nil, fmt.Errorf("hud daemon unreachable: %w", err)
	}
	var v ipc.VerifierStatus
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		return nil, err
	}
	return mcp.NewToolResultJSON(v)
}
