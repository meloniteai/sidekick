package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/ipc"
)

// status is a tiny dev/debug subcommand: prints the daemon's StatusReply as JSON.
// MCP `hud_status` (milestone 5) will use the same Send call.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the current HUD status as JSON (debug)",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := ipc.Send(ipc.Request{Type: ipc.TypeStatus})
			if err != nil {
				return err
			}
			var pretty json.RawMessage = resp.Data
			out, err := json.MarshalIndent(pretty, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
}
