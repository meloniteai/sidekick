// Package cmd defines the cobra subcommands for the `hud` binary.
package cmd

import (
	"github.com/spf13/cobra"
)

// New returns the root command, fully assembled.
func New() *cobra.Command {
	root := &cobra.Command{
		Use:           "hud",
		Short:         "A live HUD-like TUI for agentic coding sessions",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newStartCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newGoalCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newMcpCmd())
	return root
}
