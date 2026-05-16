package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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

			baseRef, worktree, err := captureSessionAnchor()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[hud] session base ref: %s\n", baseRef)
			fmt.Fprintf(os.Stderr, "[hud] session worktree: %s\n", worktree)

			verifiers, quietPeriod, source, loadedConfigPath, err := loadVerifiers(configPath)
			if err != nil {
				return err
			}
			sessionIdleTimeout := daemon.DefaultSessionIdleTimeout
			if idle, set, err := loadSessionIdleTimeout(configPath, worktree); err != nil {
				return err
			} else if set {
				sessionIdleTimeout = idle
			}
			if len(verifiers) < hudtui.MinSelected {
				return fmt.Errorf("at least %d verifiers must be configured (found %d in %s)",
					hudtui.MinSelected, len(verifiers), source)
			}
			fmt.Fprintf(os.Stderr, "[hud] verifiers: %s\n", source)
			fmt.Fprintf(os.Stderr, "[hud] enabled: %s\n", verifierNames(verifiers))

			state := daemon.NewState()
			state.SetSessionBaseRef(baseRef)
			state.SetSessionWorktree(worktree)
			state.SetVersion(version)
			runner := verifier.NewRunner(ctx, state, verifiers)
			runner.SetQuietPeriod(quietPeriod)
			fmt.Fprintf(os.Stderr, "[hud] quiet period: %s\n", runner.QuietPeriod())
			fmt.Fprintf(os.Stderr, "[hud] session idle timeout: %s\n", sessionIdleTimeout)

			runtimes := newSessionRuntimeManager(ctx, version, configPath)
			runtimes.Register(state, runner, loadedConfigPath)
			defer runtimes.StopAll()
			registry := daemon.NewRegistry(state, runtimes.NewSession)
			registry.SetCleanup(runtimes.Stop)
			registry.SetIdleTimeout(sessionIdleTimeout)
			go registry.StartGC(ctx, time.Minute)

			handler := &runnerHandler{runtimes: runtimes}
			srv, err := acquireDaemonSocket(sock, registry, handler, true)
			if err != nil {
				return err
			}
			defer srv.Close()

			serveErr := make(chan error, 1)
			go func() { serveErr <- srv.Serve(ctx) }()
			fmt.Fprintf(os.Stderr, "[hud] listening on %s (menubar)\n", sock)

			manualTrigger := func() {
				session := registry.DisplayedSession()
				session.SetGoal("manual trigger, goal unknown")
				if runner := runtimes.Runner(session); runner != nil {
					runner.TriggerImmediate()
				}
			}
			runErr := menubar.Run(ctx, registry, menubar.Actions{
				Trigger: manualTrigger,
				StopRun: func() {
					if runner := runtimes.Runner(registry.DisplayedSession()); runner != nil {
						runner.KillBatch()
					}
				},
				SwitchSession: registry.SwitchDisplayed,
				Quit:          cancel,
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
