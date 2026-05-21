package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/meloniteai/sidekick/internal/ipc"
	"github.com/meloniteai/sidekick/internal/telemetry"
)

// newExportCmd dumps the local telemetry database so the raw rows can be
// analysed by hand — the only read path the Phase 1 MVP ships (no dashboards,
// no scoring). JSON dumps every table; CSV dumps one table named with --table.
func newExportCmd() *cobra.Command {
	var format string
	var table string
	var dbPath string

	c := &cobra.Command{
		Use:   "export",
		Short: "Dump the local telemetry database (JSON or CSV) for hand analysis",
		Long: `Dump the durable telemetry collected for this repo to stdout.

The database is selected from the current repo's fingerprint (the same scheme
the daemon socket uses), or pass --db to point at a specific file. JSON dumps
every table; CSV dumps a single table chosen with --table.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				cwd, _ := os.Getwd()
				p, err := ipc.TelemetryDBPath(cwd)
				if err != nil {
					return err
				}
				dbPath = p
			}
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("no telemetry database at %s (run a session first, or pass --db)", dbPath)
			}

			store, err := telemetry.OpenReadOnly(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()

			switch format {
			case "json":
				return telemetry.DumpJSON(store.DB(), os.Stdout)
			case "csv":
				if table == "" {
					return fmt.Errorf("--format csv requires --table (one of %v)", telemetry.Tables)
				}
				return telemetry.DumpCSV(store.DB(), table, os.Stdout)
			default:
				return fmt.Errorf("unknown --format %q (want json or csv)", format)
			}
		},
	}
	c.Flags().StringVar(&format, "format", "json", "output format: json (all tables) or csv (one --table)")
	c.Flags().StringVar(&table, "table", "", fmt.Sprintf("table to dump as CSV (one of %v)", telemetry.Tables))
	c.Flags().StringVar(&dbPath, "db", "", "path to the telemetry SQLite file (default: per-repo fingerprint under ~/.sidekick/telemetry)")
	return c
}
