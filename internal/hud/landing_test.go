package hud

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/verifier"
)

func landingFixture() []verifier.Verifier {
	return []verifier.Verifier{
		{Name: "Architect", Direction: "N"},
		{Name: "Test", Direction: "E"},
		{Name: "Security", Direction: "S"},
	}
}

// TestLandingRenderShape pins the visual contract: HUD wordmark, version
// pill, working dir, socket path, the section title, every verifier with its
// direction, and the footer help row. Each of these is something a user
// needs to *see* on start — silently dropping one regresses discoverability.
func TestLandingRenderShape(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/home/u/.hud/sockets/abc.sock", "/home/u/repos/hud")
	l.width, l.height = 120, 40

	out := l.View()
	for _, want := range []string{
		"██",          // wordmark
		"v0.1",        // version pill
		"Socket",      // socket label
		"Verifiers",   // section title
		"Architect", "Test", "Security",
		"N", "E", "S",
		"↑/↓ navigate", "space toggle", "enter start", "esc abort",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("landing view missing %q in:\n%s", want, out)
		}
	}
}

// TestLandingDefaultsAllEnabled: hitting enter immediately should return the
// full input set, matching the behaviour of the previous huh picker which
// pre-selected everything.
func TestLandingDefaultsAllEnabled(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := next.(Landing)
	if !final.Confirmed() || final.Aborted() {
		t.Fatalf("enter on defaults should confirm; confirmed=%v aborted=%v", final.Confirmed(), final.Aborted())
	}
	if got := len(final.Selection()); got != 3 {
		t.Fatalf("default selection size = %d, want 3", got)
	}
}

// TestLandingToggleDeselects: space-toggling a row drops it from the
// selection, and the remaining verifiers come back in input order — same
// invariant the old filterPickerSelection helper used to guarantee.
func TestLandingToggleDeselects(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	// Move to "Test" (index 1) and toggle off.
	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyDown})
	next, _ = next.(Landing).Update(tea.KeyMsg{Type: tea.KeySpace})
	next, _ = next.(Landing).Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := next.(Landing)

	if !final.Confirmed() {
		t.Fatalf("enter should confirm after partial deselect")
	}
	sel := final.Selection()
	if len(sel) != 2 || sel[0].Name != "Architect" || sel[1].Name != "Security" {
		names := make([]string, len(sel))
		for i, v := range sel {
			names[i] = v.Name
		}
		t.Fatalf("selection = %v, want [Architect Security] (input order preserved)", names)
	}
}

// TestLandingMinSelectedBlocksEnter: deselecting everything must block enter
// and surface an error — keeps the contract that the daemon never starts
// with zero verifiers (mirrors the old huh Validate).
func TestLandingMinSelectedBlocksEnter(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	// Toggle off all three rows.
	model := tea.Model(l)
	for range 3 {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
		model = next
		next, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = next
	}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := next.(Landing)

	if final.Confirmed() {
		t.Fatalf("enter on empty selection should not confirm")
	}
	if final.err == "" {
		t.Fatalf("empty selection should surface an error message")
	}
	if !strings.Contains(final.View(), final.err) {
		t.Fatalf("error %q should appear in the view", final.err)
	}
}

// TestLandingEscAborts: esc reports aborted=true with confirmed=false so the
// caller can map it to the "aborted: no verifiers selected" shutdown.
func TestLandingEscAborts(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyEsc})
	final := next.(Landing)

	if !final.Aborted() || final.Confirmed() {
		t.Fatalf("esc should abort; aborted=%v confirmed=%v", final.Aborted(), final.Confirmed())
	}
}
