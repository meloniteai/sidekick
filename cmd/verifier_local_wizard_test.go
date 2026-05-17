package cmd

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/meloniteai/sidekick/internal/config"
)

// drive feeds a stream of key presses through the wizard model and returns
// the resulting model. Each rune produces a KeyRunes msg, and "\n" maps to
// KeyEnter — enough vocabulary to walk the wizard end-to-end in tests.
func driveWizard(t *testing.T, m *localWizardModel, keys string) *localWizardModel {
	t.Helper()
	var cur tea.Model = m
	for _, r := range keys {
		var msg tea.Msg
		switch r {
		case '\n':
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case '\t':
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case '↓':
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case '↑':
			msg = tea.KeyMsg{Type: tea.KeyUp}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		}
		next, _ := cur.Update(msg)
		cur = next
	}
	final, ok := cur.(*localWizardModel)
	if !ok {
		t.Fatalf("driveWizard: model is %T, want *localWizardModel", cur)
	}
	return final
}

// TestLocalWizardChromeMatchesPalette pins down the visual contract: the
// modal carries the palette's title-row slash banner, the "↑/↓ choose"
// help affordance lifted from the palette footer, and renders the
// step counter so the user knows where they are in the flow.
func TestLocalWizardChromeMatchesPalette(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "", "", "", "", false)
	// Window-size to force a sane layout.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	out := m.View()
	for _, want := range []string{
		"New Verifier",
		"////", // palette-style slash banner
		"1/",   // step counter prefix (visible-step total varies w/ flags)
		"Name",
		"shift+tab back",
		"esc cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("first-step view missing %q in:\n%s", want, out)
		}
	}
}

// TestLocalWizardWalksAgentBranch confirms that picking type=agent (the
// default) routes through the agent-only steps and reaches the confirm
// step with the YAML preview embedded in the description.
func TestLocalWizardWalksAgentBranch(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "MyAgent", "NE", "agent", "", false)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Walk: Name(enter) → Direction(enter) → Type(enter) → AgentName(enter)
	// → Model(enter blank) → Thinking(enter blank) → Skill(enter, default)
	// → Timeout(enter blank) → Configure perms?(no, enter) → Confirm shown.
	m = driveWizard(t, m, "\n\n\n\n\n\n\n\n↓\n")

	step := m.currentStep()
	if step.id != "confirm" {
		t.Fatalf("expected to reach confirm step; got %q", step.id)
	}
	out := m.View()
	for _, want := range []string{
		"Confirm",
		"name: MyAgent",
		"type: agent",
		"Add", "Cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirm view missing %q in:\n%s", want, out)
		}
	}
}

// TestLocalWizardTypeChoiceSwitchesBranch flips type from the default
// agent to binary and verifies the subsequent steps are the binary-only
// ones (Binary command, Pass reason, Fail reason).
func TestLocalWizardTypeChoiceSwitchesBranch(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "", "", "", "", false)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Name(enter) → Direction(enter) → Type: ↓↓ to land on binary, then enter.
	m = driveWizard(t, m, "\n\n↓↓\n")

	step := m.currentStep()
	if step.id != "binary-cmd" {
		t.Fatalf("expected binary-cmd step after picking binary; got %q", step.id)
	}
	if m.kind != "binary" {
		t.Fatalf("kind = %q, want binary", m.kind)
	}
}

// TestLocalWizardEscAborts: hitting esc at any step sets aborted=true and
// emits a Quit command, mirroring the palette's "esc cancel" contract.
func TestLocalWizardEscAborts(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "", "", "", "", false)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	final := next.(*localWizardModel)
	if !final.aborted {
		t.Fatal("esc should set aborted=true")
	}
	if cmd == nil {
		t.Fatal("esc should produce a Quit command")
	}
}

// TestLocalWizardSkipsConfirmWhenYesFlag: --yes hides the confirm step so
// the wizard completes as soon as the last input step finishes. Verifies
// the show() predicate wiring rather than the post-save path (the caller
// handles save).
func TestLocalWizardSkipsConfirmWhenYesFlag(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "Auto", "E", "command", "", true)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, s := range m.steps {
		if s.id == "confirm" && s.show(m) {
			t.Fatalf("confirm step should be hidden when yesFlag=true")
		}
	}
}

// TestLocalWizardWrapsLongSummary: the type step has summaries that don't
// fit a single line at typical terminal widths. The renderer must wrap
// onto an indented continuation line rather than truncating, otherwise
// users can't read the option descriptions.
func TestLocalWizardWrapsLongSummary(t *testing.T) {
	f := &config.File{}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "", "", "", "", false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	// Advance to the type step.
	m = driveWizard(t, m, "\n\n")

	out := m.View()
	// The "command" option's summary is "read session JSON on stdin,
	// print distance/reason JSON" — long enough to wrap at innerW≈68.
	// After wrap the trailing word "JSON" sits on its own indented line.
	if !strings.Contains(out, "print distance/reason") {
		t.Fatalf("first wrap line missing: \n%s", out)
	}
	if !strings.Contains(out, "JSON") {
		t.Fatalf("wrapped JSON tail missing: \n%s", out)
	}
	// And nothing should have a literal "…" or truncation marker — we
	// reserve those for paths the user can't see.
	if strings.Contains(out, "distance/reason JSON                ") {
		// trailing-space match would mean we didn't wrap, only padded.
		t.Fatalf("expected wrap, found single-line padded summary: \n%s", out)
	}
}

// TestLocalWizardWidensModalCap raises the question: does the wizard's
// own inner-width helper actually allow a roomier modal than the in-Sidekick
// palette's tight 72-cell cap? Tests the helper directly so a future
// width tweak doesn't silently shrink the wizard back below 80 cells —
// the point at which the type-step summaries start wrapping awkwardly.
func TestLocalWizardWidensModalCap(t *testing.T) {
	if w := localWizardInnerWidth(160); w < 80 {
		t.Fatalf("modal should grow past 80 cells on a wide terminal; got %d", w)
	}
}

// TestLocalWizardNameDuplicateRejects: typing an existing name should
// surface the duplicate-name validation error and keep the user on the
// Name step. Mirrors what the text wizard does.
func TestLocalWizardNameDuplicateRejects(t *testing.T) {
	f := &config.File{Verifiers: []config.VerifierSpec{{Name: "Existing", Direction: "N", Type: "command", Command: []string{"./x.sh"}}}}
	m := newLocalWizardModel(f, "/tmp/sidekick.yaml", "Existing", "", "", "", false)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Enter on the Name step with the pre-filled duplicate value.
	m = driveWizard(t, m, "\n")
	if m.currentStep().id != "name" {
		t.Fatalf("expected to stay on Name step after duplicate; got %q", m.currentStep().id)
	}
	if !strings.Contains(m.errMsg, "already exists") {
		t.Fatalf("expected duplicate-name error, got %q", m.errMsg)
	}
}
