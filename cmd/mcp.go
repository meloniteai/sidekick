package cmd

import (
	"github.com/spf13/cobra"

	sidekickmcp "github.com/meloniteai/sidekick/internal/mcp"
)

func newMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Sidekick MCP server (stdio) — invoked by Claude Code",
		Long: `Reads MCP requests on stdin and proxies sidekick_status / sidekick_explain to the
running 'sidekick start' daemon over its Unix socket. Add to .claude/settings.json:

  "mcpServers": {
    "sidekick": {"command": "sidekick", "args": ["mcp"]}
  }`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sidekickmcp.Run(cmd.Context(), version)
		},
	}
}
