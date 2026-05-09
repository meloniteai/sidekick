package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/ipc"
)

// claudeCodeHookInput is the subset of fields we read from CC's hook JSON.
// Claude Code pipes a JSON document to stdin for every hook invocation; the
// shape varies by event but `tool_input` (PostToolUse) is stable.
type claudeCodeHookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		FilePath string `json:"file_path"`
		// Edit/MultiEdit also carry file_path; NotebookEdit uses notebook_path.
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
}

func newHookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hook <event>",
		Short: "Forward a Claude Code hook event to the running daemon",
		Long: `Reads CC's hook JSON from stdin and forwards a normalized event to the daemon.
Supported events: write (PostToolUse on Write/Edit/MultiEdit/NotebookEdit).

Goals are set by the agent itself via the hud_set_goal MCP tool, not by a hook.

Hooks must succeed silently and never block the agent, so any error here is
logged to stderr and the command always exits 0.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() { os.Exit(0) }() // never block CC
			event := strings.ToLower(args[0])

			raw, _ := io.ReadAll(os.Stdin)
			var in claudeCodeHookInput
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &in); err != nil {
					fmt.Fprintf(os.Stderr, "[hud hook] ignoring non-JSON stdin: %v\n", err)
				}
			}

			switch event {
			case "write":
				file := in.ToolInput.FilePath
				if file == "" {
					file = in.ToolInput.NotebookPath
				}
				if file == "" {
					return nil
				}
				return forward(ipc.TypeWrite, ipc.WriteData{File: file})
			default:
				fmt.Fprintf(os.Stderr, "[hud hook] unknown event %q\n", event)
				return nil
			}
		},
	}
	return c
}

func forward(reqType string, data any) error {
	rawData, err := ipc.MarshalData(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[hud hook] marshal: %v\n", err)
		return nil
	}
	if _, err := ipc.Send(ipc.Request{Type: reqType, Data: rawData}); err != nil {
		fmt.Fprintf(os.Stderr, "[hud hook] daemon unreachable: %v\n", err)
	}
	return nil
}
