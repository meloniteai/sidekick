package cmd

import (
	"github.com/spf13/cobra"

	hudmcp "github.com/uriahlevy/hud/internal/mcp"
)

// version is stamped at build time; default keeps `go run` working. Surfaced
// in the TUI header and reported by `hud mcp` to the MCP client.
var version = "dev"

func newMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the HUD MCP server (stdio) — invoked by Claude Code",
		Long: `Reads MCP requests on stdin and proxies hud_status / hud_explain to the
running 'hud start' daemon over its Unix socket. Add to .claude/settings.json:

  "mcpServers": {
    "hud": {"command": "hud", "args": ["mcp"]}
  }`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return hudmcp.Run(cmd.Context(), version)
		},
	}
}
