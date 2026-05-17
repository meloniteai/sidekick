package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/meloniteai/sidekick/internal/daemon"
	sidekicktui "github.com/meloniteai/sidekick/internal/sidekick"
	"github.com/meloniteai/sidekick/internal/ipc"
	"github.com/meloniteai/sidekick/internal/menubar"
	"github.com/meloniteai/sidekick/internal/verifier"
)

func newMenubarCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "menubar",
		Short: "Start the Sidekick daemon as a macOS menu bar item",
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
			fmt.Fprintf(os.Stderr, "[sidekick] session base ref: %s\n", baseRef)
			fmt.Fprintf(os.Stderr, "[sidekick] session worktree: %s\n", worktree)

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
			if loadedConfigPath != "" && len(verifiers) < sidekicktui.MinSelected {
				return fmt.Errorf("at least %d verifiers must be configured (found %d in %s)",
					sidekicktui.MinSelected, len(verifiers), source)
			}
			fmt.Fprintf(os.Stderr, "[sidekick] verifiers: %s\n", source)
			fmt.Fprintf(os.Stderr, "[sidekick] enabled: %s\n", verifierNames(verifiers))

			state := daemon.NewState()
			state.SetSessionBaseRef(baseRef)
			state.SetSessionWorktree(worktree)
			state.SetVersion(version)
			runner := verifier.NewRunner(ctx, state, verifiers)
			runner.SetQuietPeriod(quietPeriod)
			fmt.Fprintf(os.Stderr, "[sidekick] quiet period: %s\n", runner.QuietPeriod())
			fmt.Fprintf(os.Stderr, "[sidekick] session idle timeout: %s\n", sessionIdleTimeout)

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
			fmt.Fprintf(os.Stderr, "[sidekick] listening on %s (menubar)\n", sock)

			manualTrigger := func() {
				session := registry.DisplayedSession()
				session.SetGoal("unset")
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
	cmd.Flags().StringVar(&configPath, "config", "", "path to sidekick.yaml (default: nearest sidekick.yaml above cwd)")
	return cmd
}
