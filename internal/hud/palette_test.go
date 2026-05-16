package hud

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/daemon"
)

// TestPaletteRenderShape locks in the visual contract: title row with slash
// banner, a filter prompt, all four migrated commands with their shortcuts,
// and the footer help text. If any of these go missing the user loses
// discoverability of the migration.
func TestPaletteRenderShape(t *testing.T) {
	p := NewPalette()
	p.SetSize(120, 40)

	out := p.View()
	for _, want := range []string{
		"Commands",
		"////",
		"Type to filter",
		"New Verifier",
		"Edit Verifier",
		"Switch Session",
		"Toggle Git Changes",
		"Toggle Event Log",
		"ctrl+n", "ctrl+e", "ctrl+w", "ctrl+g", "ctrl+l",
		"↑/↓ choose", "enter confirm", "esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("palette view missing %q in:\n%s", want, out)
		}
	}
}

// TestPaletteNavigationAndEnter verifies down-arrow advances the cursor and
// enter returns the corresponding action. Covers the wiring contract the
// Model relies on in dispatchPaletteAction.
func TestPaletteNavigationAndEnter(t *testing.T) {
	p := NewPalette()
	p.SetSize(120, 40)

	// First item is New Verifier — confirming on the initial cursor should
	// return paletteActionNewVerifier.
	confirmed, _, done := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !done || confirmed.Chosen() != paletteActionNewVerifier {
		t.Fatalf("first enter: got done=%v action=%d, want done=true action=%d", done, confirmed.Chosen(), paletteActionNewVerifier)
	}

	// Now from a fresh palette: down → down → enter should pick Switch
	// Session (index 2), proving the cursor advances and clamps correctly.
	p = NewPalette()
	p.SetSize(120, 40)
	step1, _, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	step2, _, _ := step1.Update(tea.KeyMsg{Type: tea.KeyDown})
	final, _, done := step2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !done || final.Chosen() != paletteActionSwitchSession {
		t.Fatalf("down,down,enter: got done=%v action=%d, want action=%d", done, final.Chosen(), paletteActionSwitchSession)
	}
}

// TestPaletteFilter narrows the visible list as the user types and resets the
// cursor so enter always selects a still-visible match.
func TestPaletteFilter(t *testing.T) {
	p := NewPalette()
	p.SetSize(120, 40)

	// Move cursor off the first item so we can detect the post-filter reset.
	next, _, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _, _ = next.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Type "git" — only "Toggle Git Changes" matches.
	for _, r := range "git" {
		next, _, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	items := next.visibleItems()
	if len(items) != 1 || items[0].action != paletteActionToggleGitPanel {
		t.Fatalf("filter \"git\" should leave only the git toggle item; got %d items: %+v", len(items), items)
	}

	// Enter should now pick the git toggle even though we had previously
	// moved the cursor past it.
	final, _, done := next.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !done || final.Chosen() != paletteActionToggleGitPanel {
		t.Fatalf("post-filter enter: got done=%v action=%d, want action=%d", done, final.Chosen(), paletteActionToggleGitPanel)
	}
}

// TestPaletteEscCancels: dismissing with esc reports done=true with action
// None so the Model can close without running any side effect.
func TestPaletteEscCancels(t *testing.T) {
	p := NewPalette()
	p.SetSize(120, 40)
	final, _, done := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !done {
		t.Fatalf("esc should close the palette")
	}
	if final.Chosen() != paletteActionNone {
		t.Fatalf("esc should yield paletteActionNone; got %d", final.Chosen())
	}
}

// TestModelCtrlPOpensPalette: the user-facing entry point. Pressing ctrl+p on
// the main screen has to install a palette; subsequent keys must route to it
// rather than the main key handler.
func TestModelCtrlPOpensPalette(t *testing.T) {
	m := Model{width: 120, height: 40}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("update did not return a Model")
	}
	if nm.palette == nil {
		t.Fatalf("ctrl+p should open the palette; palette is still nil")
	}
}

// openPalette is a tiny test helper that drives Update with ctrl+p and
// returns the resulting Model with a palette installed.
func openPalette(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("ctrl+p: Update did not return a Model")
	}
	if out.palette == nil {
		t.Fatalf("ctrl+p: palette was not opened")
	}
	return out
}

// TestPaletteDispatchTogglesGitPanel: select "Toggle Git Changes" through the
// palette and verify showGitPanel actually flipped on the host Model. This is
// the end-to-end contract — the palette UI is meaningless if the dispatcher
// doesn't fire the matching side effect.
func TestPaletteDispatchTogglesGitPanel(t *testing.T) {
	m := New(daemon.NewState())
	m.width, m.height = 120, 40
	if m.showGitPanel {
		t.Fatalf("precondition: showGitPanel should start false")
	}
	m = openPalette(t, m)

	// "Toggle Git Changes" is the fourth item (index 3).
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.palette != nil {
		t.Fatalf("palette should close after enter; still open")
	}
	if !m.showGitPanel {
		t.Fatalf("dispatch did not flip showGitPanel; got false, want true")
	}
}

func TestPaletteDispatchOpensSessionSwitcher(t *testing.T) {
	m := NewRegistry(testRegistryWithTwoSessions(t))
	m.width, m.height = 120, 40
	m = openPalette(t, m)

	// "Switch Session" is the third item (index 2).
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.palette != nil {
		t.Fatalf("palette should close after enter; still open")
	}
	if m.switcher == nil {
		t.Fatalf("dispatch did not open session switcher")
	}
}

func TestPaletteDispatchSwitchSessionChangesDisplayed(t *testing.T) {
	reg := testRegistryWithTwoSessions(t)
	m := NewRegistry(reg)
	m.width, m.height = 120, 40
	before := reg.DisplayedSession().SessionWorktree()
	m = openPalette(t, m)

	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = sessionSwitcherKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = sessionSwitcherKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if after := reg.DisplayedSession().SessionWorktree(); after == before {
		t.Fatalf("displayed session did not change: %q", after)
	}
}

// TestPaletteDispatchTogglesEventLog mirrors the git-panel test for the event
// log toggle. Together they prove the dispatcher reaches all four toggle/open
// branches through the same path.
func TestPaletteDispatchTogglesEventLog(t *testing.T) {
	m := New(daemon.NewState())
	m.width, m.height = 120, 40
	if m.showEventLog {
		t.Fatalf("precondition: showEventLog should start false")
	}
	m = openPalette(t, m)

	// "Toggle Event Log" is the fifth item (index 4).
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.palette != nil {
		t.Fatalf("palette should close after enter; still open")
	}
	if !m.showEventLog {
		t.Fatalf("dispatch did not flip showEventLog; got false, want true")
	}
}

// TestPaletteDispatchOpensCreateWizard: confirming "New Verifier" (index 0)
// has to install a fresh create wizard on the Model. We rely on the existing
// editor fixture so the wizard can read its hud.yaml and reach the
// editCreateBasics phase, matching what TestModelNewKeyOpensCreateWizard
// asserts for the bare-`n` shortcut.
func TestPaletteDispatchOpensCreateWizard(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	m := New(daemon.NewState()).WithConfigEditor(cfg)
	m.width, m.height = 120, 40
	m = openPalette(t, m)

	// First item is "New Verifier" — enter on cursor=0 dispatches it.
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor == nil {
		t.Fatalf("dispatch did not open a create wizard; editor is nil")
	}
	if !m.editor.create || m.editor.phase != editCreateBasics {
		t.Fatalf("create wizard should be on basics phase; create=%v phase=%v", m.editor.create, m.editor.phase)
	}
}

// TestPaletteDispatchOpensEditWizard: confirming "Edit Verifier" (index 1)
// installs an EditWizard pointing at hud.yaml.
func TestPaletteDispatchOpensEditWizard(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	m := New(daemon.NewState()).WithConfigEditor(cfg)
	m.width, m.height = 120, 40
	m = openPalette(t, m)

	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m = paletteKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.editor == nil {
		t.Fatalf("dispatch did not open an edit wizard; editor is nil")
	}
	if m.editor.create {
		t.Fatalf("edit dispatch should NOT start a create wizard; create=true")
	}
	if m.editor.configPath != cfg {
		t.Fatalf("edit wizard configPath = %q, want %q", m.editor.configPath, cfg)
	}
}

// paletteKey is a tiny test helper that forwards a key to the Model while
// the palette is open and asserts the return type. Keeps the dispatch tests
// readable by hiding the type-assertion boilerplate.
func paletteKey(t *testing.T, m Model, key tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(key)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return out
}
