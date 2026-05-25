package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	skauth "github.com/meloniteai/sidekick/internal/auth"
	"github.com/meloniteai/sidekick/internal/config"
	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/verifier"
)

func TestRunnerHandlerGoalDoesNotTriggerVerifiers(t *testing.T) {
	state := daemon.NewState()
	v := verifier.Verifier{
		Name:      "Counter",
		Direction: "N",
		Command:   []string{"sh", "-c", `printf '{"distance":0.2,"reason":"ok"}\n'`},
		Timeout:   2 * time.Second,
	}
	r := verifier.NewRunner(context.Background(), state, []verifier.Verifier{v})
	defer r.Stop()
	runtimes := newSessionRuntimeManager(context.Background(), "test", "")
	runtimes.Register(state, r, "")
	h := &runnerHandler{runtimes: runtimes}

	h.OnGoal(state, "ship without eager verifier runs")
	time.Sleep(100 * time.Millisecond)

	if got := state.Goal(); got != "ship without eager verifier runs" {
		t.Fatalf("goal: got %q", got)
	}
	s, _ := state.Verifier("Counter")
	if !s.ComputedAt.IsZero() || s.Reason != "awaiting first run" {
		t.Fatalf("goal set should not compute verifier; computed=%s reason=%q", s.ComputedAt, s.Reason)
	}

	h.OnWrite(state, "cmd/start.go")
	if !waitForStartTest(2*time.Second, func() bool {
		s, _ := state.Verifier("Counter")
		return !s.Running && s.Reason == "ok"
	}) {
		s, _ := state.Verifier("Counter")
		t.Fatalf("write should compute verifier; running=%v reason=%q", s.Running, s.Reason)
	}
}

func TestRunnerHandlerLockedGoalSurvivesOnGoal(t *testing.T) {
	state := daemon.NewState()
	state.LockGoal("operator-pinned goal")
	runtimes := newSessionRuntimeManager(context.Background(), "test", "")
	h := &runnerHandler{runtimes: runtimes}

	h.OnGoal(state, "agent override")

	if got := state.Goal(); got != "operator-pinned goal" {
		t.Fatalf("goal: got %q, want locked value", got)
	}
	if !state.GoalLocked() {
		t.Fatal("GoalLocked: got false after agent override attempt")
	}
}

// TestMirrorDisabledToConfig writes each landing-chosen Disabled flag back
// to sidekick.yaml so the persisted file reflects the active session. A row the
// user re-enabled at landing should flip from disabled:true to disabled:false
// on disk; a row they disabled should flip the other way.
func TestMirrorDisabledToConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidekick.yaml")
	if err := os.WriteFile(path, []byte(`goal_source: prompt
verifiers:
  - name: Architect
    type: command
    direction: N
    command: ["sh", "-c", "echo {}"]
  - name: Security
    type: command
    direction: S
    disabled: true
    command: ["sh", "-c", "echo {}"]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Landing flipped both rows: Architect now off, Security now on.
	verifiers := []verifier.Verifier{
		{Name: "Architect", Disabled: true},
		{Name: "Security", Disabled: false},
	}
	if err := mirrorDisabledToConfig(path, verifiers); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	f, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := map[string]bool{}
	for _, v := range f.Verifiers {
		got[v.Name] = v.Disabled
	}
	if !got["Architect"] || got["Security"] {
		t.Fatalf("yaml not mirrored: Architect=%v Security=%v, want true false", got["Architect"], got["Security"])
	}
}

// TestEnabledVerifiers covers the small filter helper used for the boot-time
// "[sidekick] enabled: ..." log. Disabled rows must be omitted so the operator
// sees what will actually run.
func TestEnabledVerifiers(t *testing.T) {
	vs := []verifier.Verifier{
		{Name: "A"},
		{Name: "B", Disabled: true},
		{Name: "C"},
	}
	got := enabledVerifiers(vs)
	if len(got) != 2 || got[0].Name != "A" || got[1].Name != "C" {
		names := make([]string, len(got))
		for i, v := range got {
			names[i] = v.Name
		}
		t.Fatalf("enabled = %v, want [A C]", names)
	}
}

func TestBindStartDispatch(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, "sidekick"},
		{[]string{"start"}, "start"},
	}
	for _, tc := range cases {
		root := New("test", nil)
		target, _, err := root.Find(tc.args)
		if err != nil {
			t.Fatalf("Find(%v): %v", tc.args, err)
		}
		if target.Name() != tc.want {
			t.Fatalf("dispatch %v: got %q, want %q", tc.args, target.Name(), tc.want)
		}
		if target.RunE == nil {
			t.Fatalf("dispatch %v: target %q has nil RunE", tc.args, target.Name())
		}
	}
}

func TestBindStartContract(t *testing.T) {
	cases := []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"root", func() *cobra.Command { return New("test", nil) }},
		{"start", func() *cobra.Command {
			// The start subcommand is reachable as a child of the root.
			root := New("test", nil)
			for _, c := range root.Commands() {
				if c.Name() == "start" {
					return c
				}
			}
			t.Fatalf("start subcommand not found on root")
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.cmd()
			for _, name := range []string{"headless", "config", "goal"} {
				if c.Flags().Lookup(name) == nil {
					t.Errorf("%s missing --%s flag", tc.name, name)
				}
			}
			if err := c.ParseFlags([]string{"--headless", "--config", "/tmp/x", "--goal", "ship"}); err != nil {
				t.Fatalf("%s ParseFlags: %v", tc.name, err)
			}
			if got, _ := c.Flags().GetBool("headless"); !got {
				t.Errorf("%s --headless did not parse to true", tc.name)
			}
			if got, _ := c.Flags().GetString("config"); got != "/tmp/x" {
				t.Errorf("%s --config = %q, want /tmp/x", tc.name, got)
			}
			if got, _ := c.Flags().GetString("goal"); got != "ship" {
				t.Errorf("%s --goal = %q, want ship", tc.name, got)
			}
			if c.RunE == nil {
				t.Fatalf("%s.RunE is nil; would print help instead of launching the TUI", tc.name)
			}
			if c.Args == nil {
				t.Fatalf("%s.Args is nil; unknown positional args would fall through to TUI", tc.name)
			}
			if err := c.Args(c, []string{"unexpected-positional"}); err == nil {
				t.Fatalf("%s accepted an unknown positional arg; expected NoArgs rejection", tc.name)
			}
		})
	}
}

func TestResolveBackendURLFallsBackToAuthProfile(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", filepath.Join(dir, "missing-global.yaml"))
	if err := skauth.PutProfile(authFile, skauth.Profile{OrgSlug: "acme", APIBase: "https://sidekick.example/api", Token: "sk_live_token"}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	if got := resolveBackendURL("", dir); got != "https://sidekick.example/api" {
		t.Fatalf("resolveBackendURL = %q", got)
	}
}

func TestResolveBackendTargetForStartScopesConfiguredURL(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	if err := skauth.PutProfile(authFile, skauth.Profile{OrgSlug: "acme", APIBase: "https://sidekick.example/api", Token: "sk_live_token"}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	target, ok := resolveBackendTargetForStart("https://override.example/api")
	if !ok {
		t.Fatalf("resolveBackendTargetForStart ok = false")
	}
	if target.APIBase != "https://override.example/api/orgs/acme" {
		t.Fatalf("APIBase = %q", target.APIBase)
	}
	if target.Token != "sk_live_token" {
		t.Fatalf("token = %q", target.Token)
	}
}

func TestBackendAuthTokenProviderReloadsAuthFile(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	if err := skauth.PutProfile(authFile, skauth.Profile{OrgSlug: "acme", APIBase: "https://sidekick.example/api", Token: "sk_live_old"}); err != nil {
		t.Fatalf("PutProfile old: %v", err)
	}
	provider := backendAuthTokenProvider("https://override.example/api")
	if got := provider(); got != "sk_live_old" {
		t.Fatalf("provider before refresh = %q", got)
	}

	if err := skauth.PutProfile(authFile, skauth.Profile{OrgSlug: "acme", APIBase: "https://sidekick.example/api", Token: "sk_live_new"}); err != nil {
		t.Fatalf("PutProfile new: %v", err)
	}
	if got := provider(); got != "sk_live_new" {
		t.Fatalf("provider after refresh = %q", got)
	}
}

// TestNewSessionPinsStartupConfigScope guards the scope-stability invariant: a
// goal that anchors a *new* worktree must keep the config the session started
// on. When the manager is seeded with a path (the startup-resolved scope),
// NewSession loads that exact file and ignores the worktree's own
// .sidekick/sidekick.yaml. The empty-seed subtest documents the per-worktree
// discovery that the pin deliberately overrides — discovery is what used to let
// a session flip global↔project scope mid-flight.
func TestNewSessionPinsStartupConfigScope(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "sidekick.yaml")
	writeScopeConfig(t, globalPath, "GlobalOnly")

	// A worktree that ships its own project config; the pinned session must
	// never adopt it.
	worktree := t.TempDir()
	projectPath := filepath.Join(worktree, ".sidekick", "sidekick.yaml")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeScopeConfig(t, projectPath, "ProjectOnly")

	t.Run("pinned scope wins over the worktree config", func(t *testing.T) {
		m := newSessionRuntimeManager(context.Background(), "test", globalPath)
		defer m.StopAll()
		state, err := m.NewSession(daemon.SessionAnchor{Worktree: worktree})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		if got := m.ConfigPath(state); got != globalPath {
			t.Fatalf("config path = %q, want pinned %q", got, globalPath)
		}
		if _, ok := state.Verifier("GlobalOnly"); !ok {
			t.Fatal("session dropped the pinned global verifier")
		}
		if _, ok := state.Verifier("ProjectOnly"); ok {
			t.Fatal("session adopted the worktree's project verifier; scope changed mid-flight")
		}
	})

	t.Run("empty seed discovers the worktree config", func(t *testing.T) {
		m := newSessionRuntimeManager(context.Background(), "test", "")
		defer m.StopAll()
		state, err := m.NewSession(daemon.SessionAnchor{Worktree: worktree})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		if _, ok := state.Verifier("ProjectOnly"); !ok {
			t.Fatal("empty seed should fall back to the worktree's project config")
		}
	})
}

// writeScopeConfig writes a minimal one-verifier sidekick.yaml whose verifier
// name identifies which config a session loaded.
func writeScopeConfig(t *testing.T, path, verifierName string) {
	t.Helper()
	yaml := "goal_source: prompt\nverifiers:\n  - name: " + verifierName +
		"\n    type: command\n    direction: N\n    command: [\"sh\", \"-c\", \"echo {}\"]\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func waitForStartTest(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
