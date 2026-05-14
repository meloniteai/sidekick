package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/daemon"
	hudtui "github.com/uriahlevy/hud/internal/hud"
	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/verifier"
)

// runnerHandler is the production EventHandler: writes trigger debounced
// verifier runs; goal updates only flow into State.
type runnerHandler struct {
	state  *daemon.State
	runner *verifier.Runner
}

func (h *runnerHandler) OnGoal(goal, worktree, baseRef string) {
	h.state.SetGoal(goal)
	// Re-anchor whenever the caller (typically the MCP server running as a
	// child of the agent) tells us its perspective. Empty fields leave the
	// existing anchor untouched so the bootstrap default from `hud start`
	// keeps working until the first MCP set-goal arrives.
	if worktree != "" {
		h.state.SetSessionWorktree(worktree)
	}
	if baseRef != "" {
		h.state.SetSessionBaseRef(baseRef)
	}
}
func (h *runnerHandler) OnWrite(file string) {
	h.state.RecordEdit(file)
	h.runner.Trigger(file)
}

// demoVerifiers is what `hud start` instantiates when no hud.yaml is found.
// They run the bundled coverage.sh-style stub script so the user gets a live
// HUD without writing any config first. Replaced by config loading in milestone 4.
func demoVerifiers() []verifier.Verifier {
	return []verifier.Verifier{
		{Name: "Architect", Direction: "N", Command: []string{"sh", "-c", "echo '{\"distance\":0.4,\"reason\":\"placeholder\"}'"}},
		{Name: "Test", Direction: "E", Command: []string{"sh", "-c", "echo '{\"distance\":0.5,\"reason\":\"placeholder\"}'"}},
		{Name: "Security", Direction: "S", Command: []string{"sh", "-c", "echo '{\"distance\":0.6,\"reason\":\"placeholder\"}'"}},
		{Name: "Deployment", Direction: "W", Command: []string{"sh", "-c", "echo '{\"distance\":0.7,\"reason\":\"placeholder\"}'"}},
	}
}

func newStartCmd() *cobra.Command {
	var headless bool
	var configPath string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the HUD daemon and TUI",
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

			available, quietPeriod, source, loadedConfigPath, err := loadVerifiers(configPath)
			if err != nil {
				return err
			}
			if len(available) < hudtui.MinSelected {
				return fmt.Errorf("at least %d verifiers must be configured (found %d in %s)",
					hudtui.MinSelected, len(available), source)
			}
			fmt.Fprintf(os.Stderr, "[hud] verifiers: %s\n", source)

			verifiers := available
			if !headless {
				selected, err := runLanding(available, version, sock)
				if err != nil {
					return err
				}
				verifiers = selected
				if loadedConfigPath != "" {
					if err := mirrorDisabledToConfig(loadedConfigPath, verifiers); err != nil {
						return fmt.Errorf("persist landing choices: %w", err)
					}
				}
			}
			fmt.Fprintf(os.Stderr, "[hud] enabled: %s\n", verifierNames(enabledVerifiers(verifiers)))

			state := daemon.NewState()
			state.SetSessionBaseRef(baseRef)
			state.SetSessionWorktree(worktree)
			state.SetVersion(version)
			runner := verifier.NewRunner(ctx, state, verifiers)
			runner.SetQuietPeriod(quietPeriod)
			fmt.Fprintf(os.Stderr, "[hud] quiet period: %s\n", runner.QuietPeriod())
			defer runner.Stop()

			handler := &runnerHandler{state: state, runner: runner}
			srv, err := acquireDaemonSocket(sock, state, handler, !headless)
			if err != nil {
				return err
			}
			defer srv.Close()

			serveErr := make(chan error, 1)
			go func() { serveErr <- srv.Serve(ctx) }()

			if headless {
				fmt.Fprintf(os.Stderr, "[hud] listening on %s (headless)\n", sock)
				return <-serveErr
			}

			manualTrigger := func() {
				state.SetGoal("manual trigger, goal unknown")
				runner.TriggerImmediate()
				state.LogEvent(daemon.EventInfo, "all verifiers triggered")
			}
			reloadConfig := func() error {
				if loadedConfigPath == "" {
					return nil
				}
				next, quiet, _, _, err := loadVerifiers(loadedConfigPath)
				if err != nil {
					state.LogEvent(daemon.EventError, "reload config failed: %v", err)
					return err
				}
				runner.ReplaceVerifiers(next)
				runner.SetQuietPeriod(quiet)
				state.LogEvent(daemon.EventInfo, "reloaded %d verifiers from %s", len(next), loadedConfigPath)
				return nil
			}
			p := tea.NewProgram(
				hudtui.New(state).
					WithManualTrigger(manualTrigger).
					WithTriggerVerifier(func(name string) {
						if ok := runner.TriggerVerifierImmediate(name); ok {
							state.LogEvent(daemon.EventInfo, "verifier %s triggered", name)
						}
					}).
					WithToggleVerifier(func(name string) {
						disabled, ok := state.ToggleVerifierDisabled(name)
						if !ok || loadedConfigPath == "" {
							return
						}
						if err := config.SetVerifierDisabled(loadedConfigPath, name, disabled); err != nil {
							state.LogEvent(daemon.EventError, "persist verifier toggle failed: %v", err)
							return
						}
					}).
					WithStopAll(runner.KillBatch).
					WithConfigEditor(loadedConfigPath).
					WithConfigSaved(reloadConfig),
				tea.WithAltScreen(),
			)
			if _, err := p.Run(); err != nil {
				cancel()
				<-serveErr
				return err
			}
			cancel()
			<-serveErr
			return nil
		},
	}
	cmd.Flags().BoolVar(&headless, "headless", false, "run only the daemon (no TUI); useful for tests")
	cmd.Flags().StringVar(&configPath, "config", "", "path to hud.yaml (default: nearest hud.yaml above cwd, else demo verifiers)")
	return cmd
}

// acquireDaemonSocket calls daemon.Listen and, if the socket is held by
// a daemon that still answers a probe, offers an interactive "start
// anyway" prompt that unlinks the old socket and retries. When
// interactive is false (headless/non-TTY) the underlying error is
// returned unchanged so callers see the same failure they did before.
func acquireDaemonSocket(sock string, state *daemon.State, handler daemon.EventHandler, interactive bool) (*daemon.Server, error) {
	srv, err := daemon.Listen(sock, state, handler)
	if err == nil {
		return srv, nil
	}
	if !interactive || !errors.Is(err, daemon.ErrDaemonRunning) {
		return nil, err
	}
	var ok bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Another hud daemon is listening on this socket.").
			Description(fmt.Sprintf(
				"Socket: %s\n\nThis is usually a leftover from a previous hud that didn't exit cleanly.\nStart anyway will replace the old socket; any orphaned daemon process is left running but unreachable.",
				sock,
			)).
			Affirmative("Start anyway").
			Negative("Cancel").
			Value(&ok),
	)).WithTheme(hudtui.HuhTheme())
	if formErr := form.Run(); formErr != nil {
		if errors.Is(formErr, huh.ErrUserAborted) {
			return nil, err
		}
		return nil, formErr
	}
	if !ok {
		return nil, err
	}
	if rmErr := daemon.RemoveSocket(sock); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return nil, fmt.Errorf("remove old socket: %w", rmErr)
	}
	return daemon.Listen(sock, state, handler)
}

// runLanding shows the start-of-session landing screen (HUD wordmark, version,
// session socket, verifier multi-select) and returns the full verifier set
// with each entry's Disabled flag aligned to the landing toggle state.
// Disabled rows are intentionally kept in the slice so the HUD footer can
// re-enable them mid-session without a restart. Aborting with esc/ctrl+c is
// reported as a clean shutdown so the user sees the same "aborted: no
// verifiers selected" message they used to get from the huh picker. The
// landing screen itself lives in internal/hud so the visual styling stays
// adjacent to the command palette it mirrors.
func runLanding(available []verifier.Verifier, version, socketPath string) ([]verifier.Verifier, error) {
	cwd, _ := os.Getwd()
	model := hudtui.NewLanding(available, version, socketPath, cwd)
	p := tea.NewProgram(model, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	landing, ok := final.(hudtui.Landing)
	if !ok {
		return nil, fmt.Errorf("landing screen returned unexpected model %T", final)
	}
	if landing.Aborted() || !landing.Confirmed() {
		return nil, fmt.Errorf("aborted: no verifiers selected")
	}
	if landing.EnabledCount() < hudtui.MinSelected {
		return nil, fmt.Errorf("landing returned %d enabled verifiers, need at least %d", landing.EnabledCount(), hudtui.MinSelected)
	}
	return landing.Verifiers(), nil
}

// mirrorDisabledToConfig persists each verifier's Disabled flag back to
// hud.yaml so the file always reflects the active session's choices. This
// is the yaml→landing→yaml round trip that keeps the persisted config in
// sync with what the user just confirmed.
func mirrorDisabledToConfig(path string, verifiers []verifier.Verifier) error {
	for _, v := range verifiers {
		if err := config.SetVerifierDisabled(path, v.Name, v.Disabled); err != nil {
			return err
		}
	}
	return nil
}

// enabledVerifiers returns the subset of verifiers whose Disabled flag is
// false. Used for the boot-time "[hud] enabled: ..." log so the operator
// sees what will actually run, not what was offered.
func enabledVerifiers(vs []verifier.Verifier) []verifier.Verifier {
	out := make([]verifier.Verifier, 0, len(vs))
	for _, v := range vs {
		if !v.Disabled {
			out = append(out, v)
		}
	}
	return out
}

func verifierNames(vs []verifier.Verifier) string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Name
	}
	return strings.Join(names, ", ")
}

// captureSessionAnchor snapshots both the current `git rev-parse HEAD`
// and the git toplevel so LLM-backed verifiers can diff cumulative
// session work against a specific worktree.
//
// The toplevel is what makes worktrees behave correctly: verifier
// subprocesses run with this as their cwd, so `git diff
// $SESSION_BASE_REF` evaluates the right tree no matter where `hud
// start` itself was launched from. The MCP `hud_set_goal` handler
// re-anchors both values from the agent's perspective whenever a goal
// is set.
//
// If the cwd is not a git repository (or has no commits yet) we refuse
// to start: the persona rubrics are written assuming `git diff
// $SESSION_BASE_REF` works, and silently degrading would make every
// verifier score the wrong thing.
func captureSessionAnchor() (baseRef, worktree string, err error) {
	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		return "", "", fmt.Errorf("hud requires git on PATH (verifiers diff session work against HEAD)")
	}
	head, headErr := exec.Command("git", "rev-parse", "HEAD").Output()
	if headErr != nil {
		// Distinguish "not a repo" from "repo but no commits yet" so the
		// hint is actionable.
		check := exec.Command("git", "rev-parse", "--is-inside-work-tree")
		if checkErr := check.Run(); checkErr != nil {
			return "", "", fmt.Errorf("hud requires a git repository in this directory.\n" +
				"Verifiers score cumulative session work by diffing against HEAD.\n" +
				"Run `git init && git add -A && git commit -m \"init\"` and try again.")
		}
		return "", "", fmt.Errorf("hud requires at least one commit on HEAD.\n" +
			"Verifiers diff session work against HEAD; an empty repo has nothing to diff.\n" +
			"Run `git commit --allow-empty -m \"init\"` (or stage and commit your work) and try again.")
	}
	top, topErr := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if topErr != nil {
		// rev-parse HEAD succeeded, so we are inside a repo; an error here is
		// unexpected. Fall back to leaving the worktree unset rather than
		// failing the boot — the MCP re-anchor path will fill it in.
		return strings.TrimSpace(string(head)), "", nil
	}
	return strings.TrimSpace(string(head)), strings.TrimSpace(string(top)), nil
}

// loadVerifiers returns runtime verifiers and the configured quiet period
// from hud.yaml, falling back to the built-in demo set (and runtime default
// quiet period) when no config exists. The returned string is a short
// description of the source for logging.
func loadVerifiers(configPath string) ([]verifier.Verifier, time.Duration, string, string, error) {
	f, path, err := config.Load(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return demoVerifiers(), 0, "demo (no hud.yaml found)", "", nil
	}
	if err != nil {
		return nil, 0, "", "", err
	}
	vs, err := f.Resolve(filepath.Dir(path))
	if err != nil {
		return nil, 0, "", "", err
	}
	quiet, err := f.ResolveQuietPeriod()
	if err != nil {
		return nil, 0, "", "", err
	}
	return vs, quiet, fmt.Sprintf("%d from %s", len(vs), path), path, nil
}
