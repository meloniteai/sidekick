package sidekick

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/registry"
)

func TestRemoteBrowserGlobalFallbackProjectTarget(t *testing.T) {
	dir := t.TempDir()
	worktree := filepath.Join(dir, "repo")
	global := filepath.Join(dir, "home", ".sidekick", "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", global)

	b := NewRemoteBrowser(global, worktree)
	if b.scope != registry.ScopeGlobal {
		t.Fatalf("scope = %v, want global for a global loaded config", b.scope)
	}
	wantProject := filepath.Join(worktree, ".sidekick", "sidekick.yaml")
	if b.projectPath != wantProject {
		t.Fatalf("projectPath = %q, want %q", b.projectPath, wantProject)
	}

	b.mode = browserModeDetail
	next, _, _ := b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if next.scope != registry.ScopeProject {
		t.Fatalf("p key scope = %v, want project", next.scope)
	}
	if next.projectPath != wantProject {
		t.Fatalf("p key projectPath = %q, want %q", next.projectPath, wantProject)
	}
	if !strings.Contains(next.installMsg, "switch this session from global config") {
		t.Fatalf("project scope warning missing, got %q", next.installMsg)
	}

	next, _, _ = next.Update(browserInstalledMsg{finalName: "needs-tests", path: wantProject, project: true})
	if !strings.Contains(next.installMsg, "switched this session from global config to project config") {
		t.Fatalf("global-to-project install alert missing, got %q", next.installMsg)
	}
}

func TestRemoteBrowserProjectConfigDefaultsProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", filepath.Join(dir, "home", ".sidekick", "sidekick.yaml"))
	project := filepath.Join(dir, "repo", ".sidekick", "sidekick.yaml")

	b := NewRemoteBrowser(project, filepath.Dir(project))
	if b.scope != registry.ScopeProject {
		t.Fatalf("scope = %v, want project for a project loaded config", b.scope)
	}
	if b.projectPath != project {
		t.Fatalf("projectPath = %q, want %q", b.projectPath, project)
	}
}

func TestModelAdoptsProjectInstallPath(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "repo", ".sidekick", "sidekick.yaml")
	state := daemon.NewState()
	state.SetSessionWorktree(filepath.Dir(project))

	var adopted string
	m := New(state).
		WithConfigInstalled(func(path string) error {
			adopted = path
			return nil
		})
	m.browser = &RemoteBrowser{}

	next, _ := m.Update(browserInstalledMsg{path: project, project: true})
	got := next.(Model)
	if adopted != project {
		t.Fatalf("adopted path = %q, want %q", adopted, project)
	}
	if got.browser == nil {
		t.Fatal("browser should remain open after install result")
	}
}

func TestModelAdoptsGlobalInstallWhenGlobalConfigIsActive(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "home", ".sidekick", "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", global)

	var adopted string
	m := New(daemon.NewState()).
		WithConfigEditor(global).
		WithConfigInstalled(func(path string) error {
			adopted = path
			return nil
		})
	m.browser = &RemoteBrowser{}

	next, _ := m.Update(browserInstalledMsg{path: global})
	got := next.(Model)
	if adopted != global {
		t.Fatalf("adopted path = %q, want %q", adopted, global)
	}
	if got.browser == nil {
		t.Fatal("browser should remain open after install result")
	}
}

func TestModelDoesNotAdoptGlobalInstallOverProjectConfig(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "home", ".sidekick", "sidekick.yaml")
	project := filepath.Join(dir, "repo", ".sidekick", "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", global)

	adopted := false
	reloaded := false
	m := New(daemon.NewState()).
		WithConfigEditor(project).
		WithConfigInstalled(func(path string) error {
			adopted = true
			return nil
		}).
		WithConfigSaved(func() error {
			reloaded = true
			return nil
		})
	m.browser = &RemoteBrowser{}

	m.Update(browserInstalledMsg{path: global})
	if adopted {
		t.Fatal("global install should not replace an active project config")
	}
	if !reloaded {
		t.Fatal("global install should still run the normal reload callback")
	}
}

func TestModelCopyVerifierKeepsCurrentScope(t *testing.T) {
	project := filepath.Join(t.TempDir(), "repo", ".sidekick", "sidekick.yaml")
	called := false
	m := New(daemon.NewState()).
		WithConfigEditor(project).
		WithConfigInstalled(func(path string) error {
			t.Fatalf("copy must not adopt copied-to config path %q", path)
			return nil
		}).
		WithCopyVerifier(func(name string, target registry.Scope) (string, error) {
			called = true
			if name != "Architect" || target != registry.ScopeGlobal {
				t.Fatalf("copy args = %q/%v, want Architect/global", name, target)
			}
			return "copied", nil
		})
	m.status = &StatusWizard{verifier: "Architect"}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	got := next.(Model)
	if !called {
		t.Fatal("copy callback was not called")
	}
	if got.status == nil || got.status.notice != "copied" {
		t.Fatalf("copy notice = %#v", got.status)
	}
	if got.currentConfigPath() != project {
		t.Fatalf("config path changed to %q, want %q", got.currentConfigPath(), project)
	}
}
