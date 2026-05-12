// Package cmd defines the cobra subcommands for the `hud` binary.
package cmd

import (
	"github.com/spf13/cobra"
)

// version is the hud release, sourced from the //go:embed'd `version` file at
// the repo root and injected via [New]. Surfaced by `hud --version`, reported
// to MCP clients, and shown in the TUI header.
var version string

// New returns the root command, fully assembled. v is the release string
// (typically the contents of the repo-root `version` file).
func New(v string) *cobra.Command {
	version = v
	root := &cobra.Command{
		Use:           "hud",
		Short:         "A live HUD-like TUI for agentic coding sessions",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newStartCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newGoalCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newMcpCmd())
	root.AddCommand(newMenubarCmd())
	root.AddCommand(newVerifierCmd())
	return root
}
