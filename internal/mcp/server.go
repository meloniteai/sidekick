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

	srv.AddTool(
		mcp.NewTool("hud_status",
			append([]mcp.ToolOption{
				mcp.WithDescription(
					"Read the latest HUD verifier snapshot for the active session. " +
						"Returns the goal and each verifier's compass direction, " +
						"distance from goal (0=achieved, 1=far), and one-line reason. " +
						"Read-only: never triggers verifier recomputation."),
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
						"verifier's reason expanded for the agent's next decision."),
				mcp.WithString("verifier",
					mcp.Required(),
					mcp.Description("Verifier name (e.g. \"Architect\", \"Test\", \"Security\")"),
				),
			}, readOnly...)...,
		),
		explainHandler,
	)

	srv.AddTool(
		mcp.NewTool("hud_set_goal",
			mcp.WithDescription(
				"Set the active session goal that all HUD verifiers evaluate against. "+
					"Call this at the start of a task and whenever the goal materially shifts "+
					"(e.g. user pivots, sub-task begins). Calling this triggers a fresh verifier run."),
			mcp.WithString("goal",
				mcp.Required(),
				mcp.Description("One short sentence describing what the agent is currently trying to achieve."),
			),
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

func statusHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resp, err := ipc.Send(ipc.Request{Type: ipc.TypeStatus, Source: ipc.SourceMCP})
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
	data, err := ipc.MarshalData(ipc.GoalData{Goal: goal})
	if err != nil {
		return nil, err
	}
	if _, err := ipc.Send(ipc.Request{Type: ipc.TypeGoal, Source: ipc.SourceMCP, Data: data}); err != nil {
		return nil, fmt.Errorf("hud daemon unreachable (is `hud start` running?): %w", err)
	}
	return mcp.NewToolResultJSON(map[string]any{"goal": goal})
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
	resp, err := ipc.Send(ipc.Request{Type: ipc.TypeExplain, Source: ipc.SourceMCP, Data: data})
	if err != nil {
		return nil, fmt.Errorf("hud daemon unreachable: %w", err)
	}
	var v ipc.VerifierStatus
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		return nil, err
	}
	return mcp.NewToolResultJSON(v)
}
