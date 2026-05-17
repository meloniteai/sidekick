// Package cmd defines the cobra subcommands for the `hud` binary.
package cmd

import (
	"github.com/spf13/cobra"
)

// version is the hud release, sourced from the //go:embed'd `version` file at
// the repo root and injected via [New]. Surfaced by `hud --version`, reported
// to MCP clients, and shown in the TUI header.
var version string

// hudSkillBody is the embedded contents of skills/hud/SKILL.md, injected
// at startup so `hud install` can write it into the agent's skill dirs.
var hudSkillBody []byte

// New returns the root command, fully assembled. v is the release string
// (typically the contents of the repo-root `version` file). skill is the
// embedded SKILL.md body shipped with the binary (typically the contents
// of skills/hud/SKILL.md, also //go:embed'd at the repo root).
func New(v string, skill []byte) *cobra.Command {
	version = v
	hudSkillBody = skill
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
	root.AddCommand(newInstallCmd())
	return root
}
