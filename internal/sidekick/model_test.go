package sidekick

import (
	"os/exec"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/ipc"
)

func TestModelCtrlWOpensSessionSwitcher(t *testing.T) {
	reg := testRegistryWithTwoSessions(t)
	m := NewRegistry(reg)
	m.width, m.height = 120, 40

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	got := next.(Model)
	if got.switcher == nil {
		t.Fatal("ctrl+w should open session switcher")
	}
}

func TestModelSessionSwitcherChangesDisplayedSession(t *testing.T) {
	reg := testRegistryWithTwoSessions(t)
	m := NewRegistry(reg)
	m.width, m.height = 120, 40
	before := reg.DisplayedSession().SessionWorktree()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	m = next.(Model)
	m = sessionSwitcherKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sessionSwitcherKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	after := reg.DisplayedSession().SessionWorktree()
	if after == before {
		t.Fatalf("displayed session did not change: %q", after)
	}
	if m.switcher != nil {
		t.Fatal("switcher should close after selection")
	}
}

func testRegistryWithTwoSessions(t *testing.T) *daemon.Registry {
	t.Helper()
	trunk := t.TempDir()
	testGit(t, trunk, "init", "-q", "-b", "main")
	testGit(t, trunk, "commit", "--allow-empty", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	testGit(t, trunk, "worktree", "add", "-q", wt)

	defaultState := daemon.NewState()
	defaultState.SetSessionWorktree(trunk)
	defaultState.SetGoal("trunk")
	reg := daemon.NewRegistry(defaultState, func(anchor daemon.SessionAnchor) (*daemon.State, error) {
		s := daemon.NewState()
		s.SetSessionWorktree(anchor.Worktree)
		s.SetSessionBaseRef(anchor.BaseRef)
		s.SetGoal("worktree")
		return s, nil
	})
	if _, err := reg.SessionForCWD(wt); err != nil {
		t.Fatal(err)
	}
	return reg
}

func sessionSwitcherKey(t *testing.T, m Model, msg tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update did not return Model")
	}
	return out
}

func testGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func TestModelToggleGitPanelKey(t *testing.T) {
	state := daemon.NewState()
	m := New(state)
	if m.gitPanel != nil {
		t.Fatal("git panel should start hidden")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if next.(Model).gitPanel == nil {
		t.Fatal("g should open git panel modal")
	}
	after, _ := next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if after.(Model).gitPanel != nil {
		t.Fatal("second g should dismiss git panel modal")
	}
}

func TestModelIgnoresNumericVerifierToggleKeys(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	m := New(state).WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	got := next.(Model).snapshot.Verifiers
	if len(got) != 2 {
		t.Fatalf("got %d verifiers, want 2", len(got))
	}
	if got[0].Disabled {
		t.Fatal("first verifier should remain enabled")
	}
	if got[1].Disabled {
		t.Fatal("numeric keys should not toggle verifiers")
	}
	if next.(Model).footerNotice != "" {
		t.Fatalf("numeric keys should not set a footer notice, got %q", next.(Model).footerNotice)
	}
}

func TestModelIgnoresNonSpaceVerifierToggleAliases(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	m := New(state).WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got := next.(Model).snapshot.Verifiers
	if got[0].Disabled {
		t.Fatal("x should not toggle verifiers; space is the only toggle shortcut")
	}
}

func TestModelFooterBrowserActionsUseSelectedVerifier(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	triggered := ""
	m := New(state).
		WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) }).
		WithTriggerVerifier(func(name string) { triggered = name })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.selectedVerifier != 1 {
		t.Fatalf("selectedVerifier = %d, want 1", m.selectedVerifier)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = next.(Model)
	if triggered != "Test" {
		t.Fatalf("triggered = %q, want Test", triggered)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	got := m.snapshot.Verifiers
	if !got[1].Disabled {
		t.Fatal("space should toggle selected verifier off")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.status == nil || m.status.verifier != "Test" {
		t.Fatalf("enter should open selected status wizard, got %#v", m.status)
	}
}

func TestModelStatusDeleteRequiresConfirmation(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	deleted := ""
	m := New(state).WithDeleteVerifier(func(name string) (string, error) {
		deleted = name
		state.ReplaceVerifiers([]ipc.VerifierStatus{{Name: "Test", Direction: "E", Distance: 1.0}})
		return "removed " + name, nil
	})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = next.(Model)
	if m.status == nil || !m.status.confirmDelete {
		t.Fatalf("d should arm delete confirmation, status=%#v", m.status)
	}
	if m.status.deleteYes {
		t.Fatal("delete confirmation should start on No")
	}
	if deleted != "" {
		t.Fatalf("delete callback should not run before confirmation, got %q", deleted)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.status == nil || m.status.confirmDelete {
		t.Fatalf("enter on selected No should cancel delete confirmation, status=%#v", m.status)
	}
	if deleted != "" {
		t.Fatalf("delete callback should not run on selected No, got %q", deleted)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(Model)
	if m.status == nil || !m.status.deleteYes {
		t.Fatalf("right should select Yes, status=%#v", m.status)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if deleted != "Architect" {
		t.Fatalf("delete callback got %q, want Architect", deleted)
	}
	if m.status != nil {
		t.Fatal("status wizard should close after confirmed delete")
	}
	if len(m.snapshot.Verifiers) != 1 || m.snapshot.Verifiers[0].Name != "Test" {
		t.Fatalf("snapshot after delete = %+v", m.snapshot.Verifiers)
	}
	if m.footerNotice != "removed Architect" {
		t.Fatalf("footer notice = %q, want removed Architect", m.footerNotice)
	}
}

func TestRefreshNeedleMovesClockwiseTowardRunningVerifier(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Tests", Direction: "E", Running: true})
	m := New(state)

	next, _ := m.Update(tickMsg{})
	m = next.(Model)
	if m.needle.target != 2 {
		t.Fatalf("needle target = %d, want E index 2", m.needle.target)
	}
	if m.needle.direction != 1 {
		t.Fatalf("needle direction = %d, want one clockwise step to NE index 1", m.needle.direction)
	}
}

func TestRefreshNeedleMovesCounterClockwiseTowardRunningVerifier(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Deploy", Direction: "W", Running: true})
	m := New(state)

	next, _ := m.Update(tickMsg{})
	m = next.(Model)
	if m.needle.target != 6 {
		t.Fatalf("needle target = %d, want W index 6", m.needle.target)
	}
	if m.needle.direction != 7 {
		t.Fatalf("needle direction = %d, want one counter-clockwise step to NW index 7", m.needle.direction)
	}
}

func TestModelFooterNoticeExpires(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Test", Direction: "E", Distance: 0.5})
	m := New(state).WithToggleVerifier(func(name string) { state.ToggleVerifierDisabled(name) })

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(Model)
	if m.footerNotice == "" {
		t.Fatal("toggle should set a transient footer notice")
	}
	for i := 0; i < footerNoticeTicks; i++ {
		next, _ = m.Update(tickMsg{})
		m = next.(Model)
	}
	if m.footerNotice != "" || m.footerNoticeUntil != 0 {
		t.Fatalf("footer notice should expire, notice=%q until=%d", m.footerNotice, m.footerNoticeUntil)
	}
}
