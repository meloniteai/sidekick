package hud

import (
	"os/exec"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
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

func TestRefreshOrbsSnapsThenSpringsToNewTarget(t *testing.T) {
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Tests", Direction: "E", Distance: 0.2})
	m := New(state)

	// First tick must snap to the target — reconnecting to a running daemon
	// shouldn't paint a glide-from-center.
	next, _ := m.Update(tickMsg{})
	m = next.(Model)
	got := m.orbs["Tests"]
	if !got.armed {
		t.Fatal("first observation should arm the spring")
	}
	if got.x != 0.2 || got.y != 0 {
		t.Fatalf("first observation should snap to target; got (%v, %v) want (0.2, 0)", got.x, got.y)
	}

	// Now push the target outward; the spring should approach it over a few
	// ticks without overshooting past 1.0.
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Tests", Direction: "E", Distance: 0.9})
	prev := got.x
	for i := 0; i < 6; i++ {
		next, _ = m.Update(tickMsg{})
		m = next.(Model)
		cur := m.orbs["Tests"].x
		if cur < prev {
			t.Fatalf("spring should move outward on tick %d: prev=%v cur=%v", i, prev, cur)
		}
		prev = cur
	}
	if prev <= 0.5 {
		t.Fatalf("spring should have meaningfully approached the new target after 6 ticks; got %v", prev)
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
