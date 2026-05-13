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

func (h *runnerHandler) OnGoal(goal string) {
	h.state.SetGoal(goal)
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

			baseRef, err := captureSessionBaseRef()
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[hud] session base ref: %s\n", baseRef)

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
				selected, err := runPicker(available)
				if err != nil {
					return err
				}
				verifiers = selected
			}
			fmt.Fprintf(os.Stderr, "[hud] enabled: %s\n", verifierNames(verifiers))

			state := daemon.NewState()
			state.SetSessionBaseRef(baseRef)
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

// runPicker shows the opt-in selection screen and returns the chosen
// verifiers. Aborting the picker (esc/ctrl+c) is treated as a clean exit.
func runPicker(available []verifier.Verifier) ([]verifier.Verifier, error) {
	selected := make([]string, len(available))
	opts := make([]huh.Option[string], len(available))
	nameWidth := 0
	for _, v := range available {
		if n := len(v.Name); n > nameWidth {
			nameWidth = n
		}
	}
	for i, v := range available {
		selected[i] = v.Name
		label := fmt.Sprintf("%-*s  %s", nameWidth, v.Name, v.Direction)
		opts[i] = huh.NewOption(label, v.Name).Selected(true)
	}

	field := huh.NewMultiSelect[string]().
		Title("HUD — choose verifiers").
		Description(fmt.Sprintf("pick at least %d. ↑/↓ move · space toggle · enter start · esc abort", hudtui.MinSelected)).
		Options(opts...).
		Value(&selected).
		Validate(func(s []string) error {
			if len(s) < hudtui.MinSelected {
				return fmt.Errorf("select at least %d verifier (currently %d)", hudtui.MinSelected, len(s))
			}
			return nil
		})

	form := huh.NewForm(huh.NewGroup(field)).WithTheme(hudtui.HuhTheme())
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, fmt.Errorf("aborted: no verifiers selected")
		}
		return nil, err
	}

	out := filterPickerSelection(available, selected)
	if len(out) < hudtui.MinSelected {
		return nil, fmt.Errorf("picker returned %d verifiers, need at least %d", len(out), hudtui.MinSelected)
	}
	return out, nil
}

// filterPickerSelection returns the verifiers from available whose name
// appears in selectedNames, preserving the order of available (not the
// order of selectedNames). Names in selectedNames that don't match any
// verifier are silently dropped. Duplicate names are emitted once.
func filterPickerSelection(available []verifier.Verifier, selectedNames []string) []verifier.Verifier {
	keep := make(map[string]bool, len(selectedNames))
	for _, n := range selectedNames {
		keep[n] = true
	}
	out := make([]verifier.Verifier, 0, len(selectedNames))
	for _, v := range available {
		if keep[v.Name] {
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

// captureSessionBaseRef snapshots the current `git rev-parse HEAD` so
// LLM-backed verifiers can diff cumulative session work against it.
//
// If the cwd is not a git repository (or has no commits yet) we refuse
// to start: the persona rubrics are written assuming `git diff
// $SESSION_BASE_REF` works, and silently degrading would make every
// verifier score the wrong thing.
func captureSessionBaseRef() (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("hud requires git on PATH (verifiers diff session work against HEAD)")
	}
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	// Distinguish "not a repo" from "repo but no commits yet" so the
	// hint is actionable.
	check := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if checkErr := check.Run(); checkErr != nil {
		return "", fmt.Errorf("hud requires a git repository in this directory.\n" +
			"Verifiers score cumulative session work by diffing against HEAD.\n" +
			"Run `git init && git add -A && git commit -m \"init\"` and try again.")
	}
	return "", fmt.Errorf("hud requires at least one commit on HEAD.\n" +
		"Verifiers diff session work against HEAD; an empty repo has nothing to diff.\n" +
		"Run `git commit --allow-empty -m \"init\"` (or stage and commit your work) and try again.")
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
