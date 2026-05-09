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
	"github.com/spf13/cobra"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/daemon"
	hudtui "github.com/uriahlevy/hud/internal/hud"
	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/verifier"
)

// runnerHandler is the production EventHandler: writes trigger debounced
// verifier runs; goal updates flow into the State and kick a fresh run.
type runnerHandler struct {
	state  *daemon.State
	runner *verifier.Runner
}

func (h *runnerHandler) OnGoal(goal string) {
	h.state.SetGoal(goal)
	h.runner.Trigger("")
}
func (h *runnerHandler) OnWrite(file string) {
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

			available, quietPeriod, source, err := loadVerifiers(configPath)
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
			srv, err := daemon.Listen(sock, state, handler)
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

			p := tea.NewProgram(hudtui.New(state), tea.WithAltScreen())
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

// runPicker shows the opt-in selection screen and returns the chosen
// verifiers. Aborting the picker (q/ctrl+c) is treated as a clean exit.
func runPicker(available []verifier.Verifier) ([]verifier.Verifier, error) {
	picker := hudtui.NewPicker(available)
	final, err := tea.NewProgram(picker, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	pm, ok := final.(hudtui.PickerModel)
	if !ok {
		return nil, fmt.Errorf("picker returned unexpected model type %T", final)
	}
	if pm.Aborted() || !pm.Confirmed() {
		return nil, fmt.Errorf("aborted: no verifiers selected")
	}
	sel := pm.Selection()
	if len(sel) < hudtui.MinSelected {
		return nil, fmt.Errorf("picker returned %d verifiers, need at least %d", len(sel), hudtui.MinSelected)
	}
	return sel, nil
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
func loadVerifiers(configPath string) ([]verifier.Verifier, time.Duration, string, error) {
	f, path, err := config.Load(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return demoVerifiers(), 0, "demo (no hud.yaml found)", nil
	}
	if err != nil {
		return nil, 0, "", err
	}
	vs, err := f.Resolve(filepath.Dir(path))
	if err != nil {
		return nil, 0, "", err
	}
	quiet, err := f.ResolveQuietPeriod()
	if err != nil {
		return nil, 0, "", err
	}
	return vs, quiet, fmt.Sprintf("%d from %s", len(vs), path), nil
}
