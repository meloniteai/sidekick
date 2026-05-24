// Package cmd defines the cobra subcommands for the `sidekick` binary.
package cmd

import (
	"github.com/spf13/cobra"
)

// version is the sidekick release, sourced from the //go:embed'd `version` file at
// the repo root and injected via [New]. Surfaced by `sidekick --version`, reported
// to MCP clients, and shown in the TUI header.
var version string

// sidekickSkillBody is the embedded contents of skills/sidekick/SKILL.md, injected
// at startup so `sidekick install` can write it into the agent's skill dirs.
var sidekickSkillBody []byte

// New returns the root command, fully assembled. v is the release string
// (typically the contents of the repo-root `version` file). skill is the
// embedded SKILL.md body shipped with the binary (typically the contents
// of skills/sidekick/SKILL.md, also //go:embed'd at the repo root).
func New(v string, skill []byte) *cobra.Command {
	version = v
	sidekickSkillBody = skill
	root := &cobra.Command{
		Use:           "sidekick",
		Short:         "A live Sidekick-like TUI for agentic coding sessions",
		Long:          "Run `sidekick` to start the daemon and TUI. Use `sidekick --help` for available subcommands.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	bindStart(root)
	root.AddCommand(newStartCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newGoalCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newLoginCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newMcpCmd())
	root.AddCommand(newMenubarCmd())
	root.AddCommand(newVerifierCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newExportCmd())
	return root
}
