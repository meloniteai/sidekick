package sidekick

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meloniteai/sidekick/internal/verifier"
)

func landingFixture() []verifier.Verifier {
	return []verifier.Verifier{
		{Name: "Architect", Direction: "N"},
		{Name: "Test", Direction: "E"},
		{Name: "Security", Direction: "S"},
	}
}

// TestLandingRenderShape pins the visual contract: Sidekick wordmark, version
// pill, working dir, socket path, the section title, every verifier with its
// direction, and the footer help row. Each of these is something a user
// needs to *see* on start — silently dropping one regresses discoverability.
func TestLandingRenderShape(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/home/u/.sidekick/sockets/abc.sock", "/home/u/repos/sidekick")
	l.width, l.height = 120, 40

	out := l.View()
	for _, want := range []string{
		"██",        // wordmark
		"v0.1",      // version pill
		"Socket",    // socket label
		"Verifiers", // section title
		"Architect", "Test", "Security",
		"N", "E", "S",
		"↑/↓ choose", "space check-in", "enter start", "esc abort",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("landing view missing %q in:\n%s", want, out)
		}
	}
}

// TestLandingDefaultsAllEnabled: hitting enter on a fresh fixture (no
// pre-disabled verifiers) returns every input verifier with Disabled=false.
// The contract is "yaml is the source of truth"; an all-enabled yaml lands
// on an all-enabled session.
func TestLandingDefaultsAllEnabled(t *testing.T) {
	l := NewLanding(landingFixture(), "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := next.(Landing)
	if !final.Confirmed() || final.Aborted() {
		t.Fatalf("enter on defaults should confirm; confirmed=%v aborted=%v", final.Confirmed(), final.Aborted())
	}
	got := final.Verifiers()
	if len(got) != 3 {
		t.Fatalf("verifiers slice size = %d, want 3 (all rows always returned)", len(got))
	}
	for _, v := range got {
		if v.Disabled {
			t.Fatalf("default Disabled = true for %s, want false", v.Name)
		}
	}
	if final.EnabledCount() != 3 {
		t.Fatalf("EnabledCount = %d, want 3", final.EnabledCount())
	}
}

// TestLandingSeedsFromDisabledFlag: NewLanding mirrors each verifier's
// Disabled flag onto the picker so re-launching with a previously disabled
// row finds it pre-toggled off. This is the yaml→landing leg of the mirror.
func TestLandingSeedsFromDisabledFlag(t *testing.T) {
	vs := landingFixture()
	vs[1].Disabled = true // Test
	l := NewLanding(vs, "0.1", "/sock", "/cwd")
	l.width, l.height = 120, 40

	if l.EnabledCount() != 2 {
		t.Fatalf("EnabledCount = %d, want 2 (Test seeded off)", l.EnabledCount())
	}
	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := next.(Landing)
	got := final.Verifiers()
	if len(got) != 3 {
		t.Fatalf("verifiers slice = %d, want 3 (disabled rows kept)", len(got))
	}
	if got[0].Disabled || !got[1].Disabled || got[2].Disabled {
		t.Fatalf("disabled flags = [%v %v %v], want [false true false]", got[0].Disabled, got[1].Disabled, got[2].Disabled)
	}
}

// TestLandingToggleMirrorsDisabled: space-toggling a row flips the Disabled
// flag on the returned verifier rather than dropping the row. Disabled rows
// stay in the slice so the runner/Sidekick can re-enable them without a restart.
func TestLandingToggleMirrorsDisabled(t *testing.T) {
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
	got := final.Verifiers()
	if len(got) != 3 {
		t.Fatalf("verifiers slice = %d, want 3 (input rows preserved)", len(got))
	}
	if got[0].Name != "Architect" || got[1].Name != "Test" || got[2].Name != "Security" {
		t.Fatalf("order changed: %+v", got)
	}
	if got[0].Disabled || !got[1].Disabled || got[2].Disabled {
		t.Fatalf("disabled flags after toggle = [%v %v %v], want [false true false]", got[0].Disabled, got[1].Disabled, got[2].Disabled)
	}
	if final.EnabledCount() != 2 {
		t.Fatalf("EnabledCount = %d, want 2", final.EnabledCount())
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

func TestLandingConfigChoiceSwitchesVerifierSet(t *testing.T) {
	project := []verifier.Verifier{{Name: "Project", Direction: "N"}}
	global := []verifier.Verifier{{Name: "GlobalA", Direction: "E"}, {Name: "GlobalB", Direction: "S"}}
	l := NewLanding(project, "0.1", "/sock", "/cwd").WithConfigChoices([]LandingConfigChoice{
		{Label: "project", Path: "/repo/.sidekick/sidekick.yaml", Verifiers: project},
		{Label: "global", Path: "/home/u/.sidekick/sidekick.yaml", Verifiers: global},
	})
	l.width, l.height = 120, 40

	if !strings.Contains(l.View(), "Config") || !strings.Contains(l.View(), "project") || !strings.Contains(l.View(), "global") {
		t.Fatalf("config choices not rendered:\n%s", l.View())
	}
	if strings.Contains(l.View(), "Verifiers") {
		t.Fatalf("config phase should not render verifier picker:\n%s", l.View())
	}
	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	switched := next.(Landing)
	if switched.ConfigPath() != "/home/u/.sidekick/sidekick.yaml" {
		t.Fatalf("ConfigPath = %q, want global", switched.ConfigPath())
	}
	verifierModel, _ := switched.Update(tea.KeyMsg{Type: tea.KeyEnter})
	verifierPhase := verifierModel.(Landing)
	if verifierPhase.Confirmed() {
		t.Fatalf("first enter should advance from config phase, not start")
	}
	if !strings.Contains(verifierPhase.View(), "Verifiers") || !strings.Contains(verifierPhase.View(), "b back") {
		t.Fatalf("verifier phase missing local key labels:\n%s", verifierPhase.View())
	}
	finalModel, _ := verifierPhase.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := finalModel.(Landing)
	if !final.Confirmed() {
		t.Fatalf("second enter should confirm verifier selection")
	}
	got := final.Verifiers()
	if len(got) != 2 || got[0].Name != "GlobalA" || got[1].Name != "GlobalB" {
		t.Fatalf("verifiers after global switch = %+v", got)
	}
}

func TestLandingConfigPhaseUsesArrowsNotTab(t *testing.T) {
	project := []verifier.Verifier{{Name: "Project", Direction: "N"}}
	global := []verifier.Verifier{{Name: "Global", Direction: "E"}}
	l := NewLanding(project, "0.1", "/sock", "/cwd").WithConfigChoices([]LandingConfigChoice{
		{Label: "project", Path: "/repo/.sidekick/sidekick.yaml", Verifiers: project},
		{Label: "global", Path: "/home/u/.sidekick/sidekick.yaml", Verifiers: global},
	})

	next, _ := l.Update(tea.KeyMsg{Type: tea.KeyTab})
	tabbed := next.(Landing)
	if tabbed.ConfigPath() != "/repo/.sidekick/sidekick.yaml" {
		t.Fatalf("tab should not switch config scope; got %q", tabbed.ConfigPath())
	}
	next, _ = tabbed.Update(tea.KeyMsg{Type: tea.KeyDown})
	arrowed := next.(Landing)
	if arrowed.ConfigPath() != "/home/u/.sidekick/sidekick.yaml" {
		t.Fatalf("down should switch config scope; got %q", arrowed.ConfigPath())
	}
	if !strings.Contains(arrowed.View(), "enter continue") || !strings.Contains(arrowed.View(), "g global") {
		t.Fatalf("config phase missing local key labels:\n%s", arrowed.View())
	}
}
