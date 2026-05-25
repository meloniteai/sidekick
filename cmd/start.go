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
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	skauth "github.com/meloniteai/sidekick/internal/auth"
	"github.com/meloniteai/sidekick/internal/config"
	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/ipc"
	verifierregistry "github.com/meloniteai/sidekick/internal/registry"
	sidekicktui "github.com/meloniteai/sidekick/internal/sidekick"
	"github.com/meloniteai/sidekick/internal/telemetry"
	"github.com/meloniteai/sidekick/internal/verifier"
)

// runnerHandler is the production EventHandler: writes trigger debounced
// verifier runs; goal updates only flow into State.
type runnerHandler struct {
	runtimes *sessionRuntimeManager
}

func (h *runnerHandler) OnGoal(session *daemon.State, goal string) {
	if session.GoalLocked() {
		// A startup-pinned goal owns the episode; the agent's goal attempts are
		// no-ops and must not mint new telemetry sessions.
		session.SetGoal(goal)
		return
	}
	session.SetGoal(goal)
	// A telemetry session is one goal episode: open a fresh one on each goal
	// set (goal-set → next goal-set is the only unit in which "iterations to
	// converge" is meaningful).
	session.StartTelemetrySession(goal)
}
func (h *runnerHandler) OnWrite(session *daemon.State, file string) {
	// Tap the edit before RecordEdit dedups it, so repeated touches of the same
	// file stay countable in the telemetry.
	session.RecordTelemetryEdit(file)
	session.RecordEdit(file)
	if runner := h.runtimes.Runner(session); runner != nil {
		runner.Trigger(file)
	}
}

type sessionRuntime struct {
	runner     *verifier.Runner
	configPath string
}

type sessionRuntimeManager struct {
	mu      sync.Mutex
	ctx     context.Context
	version string
	// configPath is the config resolved (and possibly scope-selected) at
	// startup, pinned for the daemon's lifetime. NewSession passes it verbatim
	// to LoadFrom so a goal that anchors a *new* worktree loads this same file
	// instead of re-discovering one — that re-discovery is what let a session
	// silently flip global↔project scope mid-flight.
	configPath string
	emitter    telemetry.Emitter
	runtimes   map[*daemon.State]sessionRuntime
}

func newSessionRuntimeManager(ctx context.Context, version, configPath string) *sessionRuntimeManager {
	return &sessionRuntimeManager{
		ctx:        ctx,
		version:    version,
		configPath: configPath,
		runtimes:   map[*daemon.State]sessionRuntime{},
	}
}

func (m *sessionRuntimeManager) NewSession(anchor daemon.SessionAnchor) (*daemon.State, error) {
	verifiers, quietPeriod, _, loadedConfigPath, err := loadVerifiersFor(m.configPath, anchor.Worktree)
	if err != nil {
		return nil, err
	}
	if loadedConfigPath != "" && len(verifiers) < sidekicktui.MinSelected {
		return nil, fmt.Errorf("at least %d verifiers must be configured (found %d)", sidekicktui.MinSelected, len(verifiers))
	}
	state := daemon.NewState()
	state.SetSessionBaseRef(anchor.BaseRef)
	state.SetSessionWorktree(anchor.Worktree)
	state.SetVersion(m.version)
	state.SetEmitter(m.emitter)
	runner := verifier.NewRunner(m.ctx, state, verifiers)
	runner.SetQuietPeriod(quietPeriod)
	m.Register(state, runner, loadedConfigPath)
	state.LogEvent(daemon.EventInfo, "created session for %s", anchor.Worktree)
	return state, nil
}

func (m *sessionRuntimeManager) Register(state *daemon.State, runner *verifier.Runner, configPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimes[state] = sessionRuntime{runner: runner, configPath: configPath}
}

func (m *sessionRuntimeManager) Runner(state *daemon.State) *verifier.Runner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[state].runner
}

func (m *sessionRuntimeManager) ConfigPath(state *daemon.State) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[state].configPath
}

func (m *sessionRuntimeManager) SetConfigPath(state *daemon.State, configPath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.runtimes[state]
	rt.configPath = configPath
	m.runtimes[state] = rt
}

func (m *sessionRuntimeManager) Stop(state *daemon.State) {
	m.mu.Lock()
	rt := m.runtimes[state]
	delete(m.runtimes, state)
	m.mu.Unlock()
	if rt.runner != nil {
		rt.runner.Stop()
	}
}

func (m *sessionRuntimeManager) StopAll() {
	m.mu.Lock()
	states := make([]*daemon.State, 0, len(m.runtimes))
	for s := range m.runtimes {
		states = append(states, s)
	}
	m.mu.Unlock()
	for _, s := range states {
		m.Stop(s)
	}
}

func newStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Sidekick daemon and TUI",
	}
	bindStart(cmd)
	return cmd
}

// bindStart wires the daemon+TUI handler so both `sidekick` and `sidekick start` launch the TUI.
func bindStart(cmd *cobra.Command) {
	var headless bool
	var configPath string
	var startGoal string
	var telemetryMode string
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
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

		available, quietPeriod, source, loadedConfigPath, err := loadVerifiersFor(configPath, worktree)
		if err != nil {
			return err
		}
		landingChoices, err := configChoicesForLanding(configPath, worktree)
		if err != nil {
			return err
		}
		quietByPath := map[string]time.Duration{}
		for _, choice := range landingChoices {
			if _, quiet, _, _, err := loadVerifiersFor(choice.Path, worktree); err == nil {
				quietByPath[choice.Path] = quiet
			}
		}
		sessionIdleTimeout := daemon.DefaultSessionIdleTimeout
		if idle, set, err := loadSessionIdleTimeout(configPath, worktree); err != nil {
			return err
		} else if set {
			sessionIdleTimeout = idle
		}
		if loadedConfigPath != "" && len(available) < sidekicktui.MinSelected {
			return fmt.Errorf("at least %d verifiers must be configured (found %d in %s)",
				sidekicktui.MinSelected, len(available), source)
		}
		fmt.Fprintf(os.Stderr, "[sidekick] verifiers: %s\n", source)

		backendURL := resolveBackendURL(loadedConfigPath, worktree)
		useBackend := backendURL != "" // auto: backend when one is configured
		switch strings.ToLower(strings.TrimSpace(telemetryMode)) {
		case "", telemetryModeAuto:
		case telemetryModeLocal:
			useBackend = false
		case telemetryModeBackend:
			useBackend = true
		default:
			return fmt.Errorf("unknown --telemetry-mode %q (want auto, local, or backend)", telemetryMode)
		}

		verifiers := available
		if !headless && len(available) > 0 {
			selected, selectedConfigPath, selectedBackendURL, selectedBackend, err := runLanding(available, version, sock, landingChoices, backendURL, useBackend)
			if err != nil {
				return err
			}
			verifiers = selected
			useBackend = selectedBackend
			// The landing may have switched config scope to one with a different
			// backend.url, so adopt the sink it resolved for the chosen scope.
			backendURL = selectedBackendURL
			if selectedConfigPath != "" {
				loadedConfigPath = selectedConfigPath
				if quiet, ok := quietByPath[selectedConfigPath]; ok {
					quietPeriod = quiet
				}
				if idle, set, err := loadSessionIdleTimeout(selectedConfigPath, worktree); err != nil {
					return err
				} else if set {
					sessionIdleTimeout = idle
				}
			}
			if loadedConfigPath != "" {
				if err := mirrorDisabledToConfig(loadedConfigPath, verifiers); err != nil {
					return fmt.Errorf("persist landing choices: %w", err)
				}
			}
		}
		fmt.Fprintf(os.Stderr, "[sidekick] enabled: %s\n", verifierNames(enabledVerifiers(verifiers)))

		sinkMode := telemetryModeLocal
		if useBackend {
			sinkMode = telemetryModeBackend
		}
		emitter := openTelemetry(worktree, sinkMode, backendURL)
		if emitter != nil {
			defer emitter.Close()
		}

		state := daemon.NewState()
		state.SetSessionBaseRef(baseRef)
		state.SetSessionWorktree(worktree)
		state.SetVersion(version)
		state.SetEmitter(emitter)
		if trimmed := strings.TrimSpace(startGoal); trimmed != "" {
			state.LockGoal(trimmed)
			// A pinned goal never flows through OnGoal, so open its telemetry
			// episode here.
			state.StartTelemetrySession(trimmed)
			fmt.Fprintf(os.Stderr, "[sidekick] goal locked to: %s\n", trimmed)
		}
		runner := verifier.NewRunner(ctx, state, verifiers)
		runner.SetQuietPeriod(quietPeriod)
		fmt.Fprintf(os.Stderr, "[sidekick] quiet period: %s\n", runner.QuietPeriod())
		fmt.Fprintf(os.Stderr, "[sidekick] session idle timeout: %s\n", sessionIdleTimeout)

		// Seed the manager with the path the startup session actually resolved
		// (after any landing scope choice), not the raw --config flag. Otherwise
		// later worktree-anchored sessions re-discover config from their own dir
		// and can flip global↔project scope mid-session.
		runtimes := newSessionRuntimeManager(ctx, version, loadedConfigPath)
		runtimes.emitter = emitter
		runtimes.Register(state, runner, loadedConfigPath)
		defer runtimes.StopAll()
		daemonRegistry := daemon.NewRegistry(state, runtimes.NewSession)
		daemonRegistry.SetCleanup(runtimes.Stop)
		daemonRegistry.SetIdleTimeout(sessionIdleTimeout)
		// Append a liveness heartbeat per live session on its own cadence; the
		// session end is derived from the last one.
		daemonRegistry.SetHeartbeat(func() {
			daemonRegistry.EachSession(func(s *daemon.State) { s.EmitHeartbeat() })
		})
		go daemonRegistry.StartGC(ctx, time.Minute)
		go daemonRegistry.StartHeartbeat(ctx, daemon.DefaultHeartbeatInterval)

		handler := &runnerHandler{runtimes: runtimes}
		srv, err := acquireDaemonSocket(sock, daemonRegistry, handler, !headless)
		if err != nil {
			return err
		}
		defer srv.Close()

		serveErr := make(chan error, 1)
		go func() { serveErr <- srv.Serve(ctx) }()

		if headless {
			fmt.Fprintf(os.Stderr, "[sidekick] listening on %s (headless)\n", sock)
			return <-serveErr
		}

		manualTrigger := func() {
			session := daemonRegistry.DisplayedSession()
			session.SetGoal("unset")
			if runner := runtimes.Runner(session); runner != nil {
				runner.TriggerImmediate()
			}
			session.LogEvent(daemon.EventInfo, "all verifiers triggered")
		}
		reloadConfig := func() error {
			session := daemonRegistry.DisplayedSession()
			path := runtimes.ConfigPath(session)
			if path == "" {
				return nil
			}
			next, quiet, _, _, err := loadVerifiersFor(path, session.SessionWorktree())
			if err != nil {
				session.LogEvent(daemon.EventError, "reload config failed: %v", err)
				return err
			}
			if runner := runtimes.Runner(session); runner != nil {
				runner.ReplaceVerifiers(next)
				runner.SetQuietPeriod(quiet)
			}
			session.LogEvent(daemon.EventInfo, "reloaded %d verifiers from %s", len(next), path)
			return nil
		}
		adoptConfig := func(path string) error {
			session := daemonRegistry.DisplayedSession()
			runtimes.SetConfigPath(session, path)
			return reloadConfig()
		}
		copyVerifier := func(name string, target verifierregistry.Scope) (string, error) {
			session := daemonRegistry.DisplayedSession()
			sourcePath := runtimes.ConfigPath(session)
			res, err := verifierregistry.CopyVerifier(verifierregistry.CopyVerifierOptions{
				SourcePath:  sourcePath,
				Target:      target,
				ProjectPath: sidekicktui.ProjectInstallPath(sourcePath, session.SessionWorktree()),
				Name:        name,
			})
			if err != nil {
				return "", err
			}
			if res.Path == sourcePath {
				if err := reloadConfig(); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("copied %s to %s", res.FinalName, res.Path), nil
		}
		deleteVerifier := func(name string) (string, error) {
			session := daemonRegistry.DisplayedSession()
			path := runtimes.ConfigPath(session)
			if path == "" {
				return "", fmt.Errorf("no active sidekick.yaml")
			}
			removed, savedPath, err := config.RemoveVerifier(path, name)
			if err != nil {
				return "", err
			}
			if err := reloadConfig(); err != nil {
				return "", err
			}
			session.LogEvent(daemon.EventInfo, "removed verifier %s from %s", removed.Name, savedPath)
			return fmt.Sprintf("removed %s", removed.Name), nil
		}
		p := tea.NewProgram(
			sidekicktui.NewRegistry(daemonRegistry).
				WithManualTrigger(manualTrigger).
				WithTriggerVerifier(func(name string) {
					session := daemonRegistry.DisplayedSession()
					if runner := runtimes.Runner(session); runner != nil && runner.TriggerVerifierImmediate(name) {
						session.LogEvent(daemon.EventInfo, "verifier %s triggered", name)
					}
				}).
				WithToggleVerifier(func(name string) {
					session := daemonRegistry.DisplayedSession()
					disabled, ok := session.ToggleVerifierDisabled(name)
					path := runtimes.ConfigPath(session)
					if !ok || path == "" {
						return
					}
					if err := config.SetVerifierDisabled(path, name, disabled); err != nil {
						session.LogEvent(daemon.EventError, "persist verifier toggle failed: %v", err)
						return
					}
				}).
				WithStopAll(func() {
					if runner := runtimes.Runner(daemonRegistry.DisplayedSession()); runner != nil {
						runner.KillBatch()
					}
				}).
				WithConfigEditor(loadedConfigPath).
				WithConfigPathFunc(func() string { return runtimes.ConfigPath(daemonRegistry.DisplayedSession()) }).
				WithConfigInstalled(adoptConfig).
				WithCopyVerifier(copyVerifier).
				WithDeleteVerifier(deleteVerifier).
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
	}
	cmd.Flags().BoolVar(&headless, "headless", false, "run only the daemon (no TUI); useful for tests")
	cmd.Flags().StringVar(&configPath, "config", "", "path to sidekick.yaml (default: nearest sidekick.yaml above cwd)")
	cmd.Flags().StringVar(&startGoal, "goal", "", "pin the session goal up-front; the agent's sidekick_set_goal calls become no-ops while this is set")
	cmd.Flags().StringVar(&telemetryMode, "telemetry-mode", telemetryModeAuto, "telemetry sink: auto (backend when configured, else local), local, or backend")
}

// acquireDaemonSocket calls daemon.Listen and, if the socket is held by
// a daemon that still answers a probe, offers an interactive "start
// anyway" prompt that unlinks the old socket and retries. When
// interactive is false (headless/non-TTY) the underlying error is
// returned unchanged so callers see the same failure they did before.
func acquireDaemonSocket(sock string, registry *daemon.Registry, handler daemon.EventHandler, interactive bool) (*daemon.Server, error) {
	srv, err := daemon.Listen(sock, registry, handler)
	if err == nil {
		return srv, nil
	}
	if !interactive || !errors.Is(err, daemon.ErrDaemonRunning) {
		return nil, err
	}
	var ok bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Another sidekick daemon is listening on this socket.").
			Description(fmt.Sprintf(
				"Socket: %s\n\nThis is usually a leftover from a previous sidekick that didn't exit cleanly.\nStart anyway will replace the old socket; any orphaned daemon process is left running but unreachable.",
				sock,
			)).
			Affirmative("Start anyway").
			Negative("Cancel").
			Value(&ok),
	)).WithTheme(sidekicktui.HuhTheme())
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
	return daemon.Listen(sock, registry, handler)
}

// runLanding shows the start-of-session landing screen (Sidekick wordmark, version,
// session socket, verifier multi-select) and returns the full verifier set
// with each entry's Disabled flag aligned to the landing toggle state.
// Disabled rows are intentionally kept in the slice so the Sidekick footer can
// re-enable them mid-session without a restart. Aborting with esc/ctrl+c is
// reported as a clean shutdown so the user sees the same "aborted: no
// verifiers selected" message they used to get from the huh picker. The
// landing screen itself lives in internal/sidekick so the visual styling stays
// adjacent to the command palette it mirrors.
func runLanding(available []verifier.Verifier, version, socketPath string, choices []sidekicktui.LandingConfigChoice, defaultBackendURL string, backendDefault bool) ([]verifier.Verifier, string, string, bool, error) {
	cwd, _ := os.Getwd()
	model := sidekicktui.NewLanding(available, version, socketPath, cwd)
	if len(choices) > 1 {
		model = model.WithConfigChoices(choices)
	}
	model = model.WithTelemetry(defaultBackendURL, backendDefault)
	p := tea.NewProgram(model, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return nil, "", "", false, err
	}
	landing, ok := final.(sidekicktui.Landing)
	if !ok {
		return nil, "", "", false, fmt.Errorf("landing screen returned unexpected model %T", final)
	}
	if landing.Aborted() || !landing.Confirmed() {
		return nil, "", "", false, fmt.Errorf("aborted: no verifiers selected")
	}
	if landing.EnabledCount() < sidekicktui.MinSelected {
		return nil, "", "", false, fmt.Errorf("landing returned %d enabled verifiers, need at least %d", landing.EnabledCount(), sidekicktui.MinSelected)
	}
	return landing.Verifiers(), landing.ConfigPath(), landing.BackendURL(), landing.TelemetryBackend(), nil
}

func configChoicesForLanding(configPath, worktree string) ([]sidekicktui.LandingConfigChoice, error) {
	if configPath != "" {
		return nil, nil
	}
	d, err := config.Discover(worktree)
	if err != nil {
		return nil, err
	}
	if d.ProjectPath == "" || d.GlobalPath == "" || sameConfigPath(d.ProjectPath, d.GlobalPath) {
		return nil, nil
	}
	var choices []sidekicktui.LandingConfigChoice
	for _, item := range []struct {
		label string
		path  string
	}{
		{label: "project", path: d.ProjectPath},
		{label: "global", path: d.GlobalPath},
	} {
		vs, _, _, _, err := loadVerifiersFor(item.path, worktree)
		if err != nil {
			return nil, err
		}
		choices = append(choices, sidekicktui.LandingConfigChoice{
			Label:      item.label,
			Path:       item.path,
			Verifiers:  vs,
			BackendURL: resolveBackendURL(item.path, worktree),
		})
	}
	return choices, nil
}

func sameConfigPath(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		aa = filepath.Clean(a)
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		bb = filepath.Clean(b)
	}
	if resolved, err := filepath.EvalSymlinks(aa); err == nil {
		aa = resolved
	}
	if resolved, err := filepath.EvalSymlinks(bb); err == nil {
		bb = resolved
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// mirrorDisabledToConfig persists each verifier's Disabled flag back to
// sidekick.yaml so the file always reflects the active session's choices. This
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
// false. Used for the boot-time "[sidekick] enabled: ..." log so the operator
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

// Telemetry sink modes selectable via --telemetry-mode, the splash screen, and
// the backend.url config default. "auto" resolves to backend when a URL is
// configured, else local.
const (
	telemetryModeAuto    = "auto"
	telemetryModeLocal   = "local"
	telemetryModeBackend = "backend"
)

// openTelemetry opens the telemetry sink for the resolved mode, or returns a nil
// Emitter (which makes every telemetry call a no-op) when collection is disabled
// or the sink can't be opened. Telemetry must never block the daemon from
// starting, so a failure is logged and swallowed; a backend that can't be
// reached falls back to the local store rather than losing the session. Returns
// the interface type so the disabled case is a genuine nil interface, not a
// typed nil.
func openTelemetry(worktree, mode, backendURL string) telemetry.Emitter {
	if !telemetryEnabled() {
		return nil
	}
	if mode == telemetryModeBackend {
		if e := openRemoteTelemetry(worktree, backendURL); e != nil {
			return e
		}
		fmt.Fprintf(os.Stderr, "[sidekick] backend telemetry unavailable; falling back to local store\n")
	}
	path, err := ipc.TelemetryDBPath(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sidekick] telemetry disabled: %v\n", err)
		return nil
	}
	store, err := telemetry.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sidekick] telemetry disabled: %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "[sidekick] telemetry sink: LOCAL %s\n", path)
	return store
}

// openRemoteTelemetry resolves the repo's backend project and returns an emitter
// bound to it, or nil (logged) when no URL is configured or the backend can't be
// reached so the caller can fall back to local. Returns the concrete type so the
// caller's nil check is unambiguous.
func openRemoteTelemetry(worktree, backendURL string) *telemetry.RemoteEmitter {
	target, ok := resolveBackendTargetForStart(backendURL)
	if strings.TrimSpace(target.APIBase) == "" {
		fmt.Fprintf(os.Stderr, "[sidekick] backend telemetry: no url configured\n")
		return nil
	}
	if !ok || target.Token == "" {
		fmt.Fprintf(os.Stderr, "[sidekick] backend telemetry: missing CLI auth; run sidekick login --org <slug> --api-base %s\n", skauth.RootAPIBase(target.APIBase))
	}
	e, err := telemetry.OpenRemote(target.APIBase, ipc.RepoFingerprint(worktree), filepath.Base(worktree), worktree, telemetry.WithAuthTokenProvider(backendAuthTokenProvider(backendURL)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sidekick] backend telemetry: %v\n", err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "[sidekick] telemetry sink: BACKEND %s (healthcheck ok, project %s)\n", target.APIBase, e.ProjectID())
	return e
}

func resolveBackendTargetForStart(backendURL string) (skauth.BackendTarget, bool) {
	authPath, err := skauth.AuthFilePath()
	if err != nil {
		return skauth.BackendTarget{APIBase: strings.TrimSpace(backendURL)}, false
	}
	target, ok, err := skauth.ResolveBackendTarget(authPath, backendURL)
	if err != nil || !ok {
		return skauth.BackendTarget{APIBase: strings.TrimSpace(backendURL)}, false
	}
	return target, true
}

func backendAuthTokenProvider(backendURL string) func() string {
	return func() string {
		target, ok := resolveBackendTargetForStart(backendURL)
		if !ok {
			return ""
		}
		return target.Token
	}
}

// resolveBackendURL picks the remote telemetry URL from the environment
// (SIDEKICK_BACKEND_URL, for headless deployments) or the loaded sidekick.yaml's
// backend.url. Empty means no backend is configured.
func resolveBackendURL(configPath, startDir string) string {
	if v := strings.TrimSpace(os.Getenv("SIDEKICK_BACKEND_URL")); v != "" {
		return v
	}
	f, _, err := config.LoadFrom(configPath, startDir)
	if err == nil {
		if v := strings.TrimSpace(f.Backend.URL); v != "" {
			return v
		}
	}
	authPath, err := skauth.AuthFilePath()
	if err != nil {
		return ""
	}
	profile, ok, err := skauth.CurrentProfile(authPath)
	if err != nil || !ok {
		return ""
	}
	return profile.APIBase
}

// telemetryEnabled reports whether collection is on. Default is on (the build
// is dogfooded); set SIDEKICK_TELEMETRY to 0/false/off/no to disable.
func telemetryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SIDEKICK_TELEMETRY"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// captureSessionAnchor snapshots both the current `git rev-parse HEAD`
// and the git toplevel so LLM-backed verifiers can diff cumulative
// session work against a specific worktree.
//
// The toplevel is what makes worktrees behave correctly: verifier
// subprocesses run with this as their cwd, so `git diff
// $SESSION_BASE_REF` evaluates the right tree no matter where `sidekick
// start` itself was launched from. The MCP `sidekick_set_goal` handler
// re-anchors both values from the agent's perspective whenever a goal
// is set.
//
// If the cwd is not a git repository (or has no commits yet) we refuse
// to start: the persona rubrics are written assuming `git diff
// $SESSION_BASE_REF` works, and silently degrading would make every
// verifier score the wrong thing.
func captureSessionAnchor() (baseRef, worktree string, err error) {
	if _, lookErr := exec.LookPath("git"); lookErr != nil {
		return "", "", fmt.Errorf("sidekick requires git on PATH (verifiers diff session work against HEAD)")
	}
	head, headErr := exec.Command("git", "rev-parse", "HEAD").Output()
	if headErr != nil {
		// Distinguish "not a repo" from "repo but no commits yet" so the
		// hint is actionable.
		check := exec.Command("git", "rev-parse", "--is-inside-work-tree")
		if checkErr := check.Run(); checkErr != nil {
			return "", "", fmt.Errorf("sidekick requires a git repository in this directory.\n" +
				"Verifiers score cumulative session work by diffing against HEAD.\n" +
				"Run `git init && git add -A && git commit -m \"init\"` and try again.")
		}
		return "", "", fmt.Errorf("sidekick requires at least one commit on HEAD.\n" +
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
// from sidekick.yaml. When no sidekick.yaml is found, the returned slice is empty and
// the returned config path is "" — callers boot vanilla and let the user
// add verifiers via `sidekick verifier add` or the in-TUI editor. The returned
// string is a short description of the source for logging.
func loadVerifiers(configPath string) ([]verifier.Verifier, time.Duration, string, string, error) {
	return loadVerifiersFor(configPath, "")
}

func loadVerifiersFor(configPath, startDir string) ([]verifier.Verifier, time.Duration, string, string, error) {
	f, path, err := config.LoadFrom(configPath, startDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, "no sidekick.yaml found", "", nil
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

func loadSessionIdleTimeout(configPath, startDir string) (time.Duration, bool, error) {
	f, _, err := config.LoadFrom(configPath, startDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return f.ResolveSessionIdleTimeout()
}
