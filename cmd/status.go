package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/meloniteai/sidekick/internal/ipc"
)

// status is a tiny dev/debug subcommand: prints the daemon's StatusReply as JSON.
// MCP `sidekick_status` (milestone 5) will use the same Send call.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the current Sidekick status as JSON (debug)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			resp, err := ipc.SendFrom(ipc.Request{Type: ipc.TypeStatus}, cwd)
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
