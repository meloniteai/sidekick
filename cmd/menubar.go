package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/daemon"
	hudtui "github.com/uriahlevy/hud/internal/hud"
	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/menubar"
	"github.com/uriahlevy/hud/internal/verifier"
)

func newMenubarCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "menubar",
		Short: "Start the HUD daemon as a macOS menu bar item",
		RunE: func(cmd *cobra.Command, args []string) error {
			sock, err := ipc.SocketPath()
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			baseRef, err := captureSessionBaseRef()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[hud] session base ref: %s\n", baseRef)

			verifiers, quietPeriod, source, err := loadVerifiers(configPath)
			if err != nil {
				return err
			}
			if len(verifiers) < hudtui.MinSelected {
				return fmt.Errorf("at least %d verifiers must be configured (found %d in %s)",
					hudtui.MinSelected, len(verifiers), source)
			}
			fmt.Fprintf(os.Stderr, "[hud] verifiers: %s\n", source)
			fmt.Fprintf(os.Stderr, "[hud] enabled: %s\n", verifierNames(verifiers))

			state := daemon.NewState()
			state.SetSessionBaseRef(baseRef)
			state.SetVersion(version)
			runner := verifier.NewRunner(ctx, state, verifiers)
			runner.SetQuietPeriod(quietPeriod)
			fmt.Fprintf(os.Stderr, "[hud] quiet period: %s\n", runner.QuietPeriod())
			defer runner.Stop()

			handler := &runnerHandler{state: state, runner: runner}
			srv, err := daemon.Listen(sock, state, handler)
			if err != nil {
				return err
			}
			defer srv.Close()

			serveErr := make(chan error, 1)
			go func() { serveErr <- srv.Serve(ctx) }()
			fmt.Fprintf(os.Stderr, "[hud] listening on %s (menubar)\n", sock)

			manualTrigger := func() {
				state.SetGoal("manual trigger, goal unknown")
				runner.TriggerImmediate()
			}
			runErr := menubar.Run(ctx, state, menubar.Actions{
				Trigger: manualTrigger,
				StopRun: runner.KillBatch,
				Quit:    cancel,
			})
			cancel()
			if err := <-serveErr; err != nil && runErr == nil {
				return err
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to hud.yaml (default: nearest hud.yaml above cwd, else demo verifiers)")
	return cmd
}
