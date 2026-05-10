package hud

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

func writeEditorFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "architect", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("# Old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "hud.yaml")
	if err := os.WriteFile(cfg, []byte(`verifiers:
  - name: Architect
    type: agent
    direction: N
    timeout: 90s
    llm:
      agent: claude
      model: haiku
      thinking: low
      skill: ./skills/architect/SKILL.md
`), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, skill
}

func TestEditWizardSavesMetadataAndSkill(t *testing.T) {
	cfg, skill := writeEditorFixture(t)
	w := NewEditWizard(cfg)

	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next
	if done || w.phase != editMetadata {
		t.Fatalf("enter should open metadata step, phase=%v done=%v", w.phase, done)
	}

	w.text = newTextBuffer(`name: Architect
type: agent
direction: NE
timeout: 45s
llm:
    agent: codex
    model: gpt-5.5
    thinking: medium
    skill: ./skills/architect/SKILL.md`)
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editSkill {
		t.Fatalf("ctrl+s should save metadata and continue to skill, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}

	f, _, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Verifiers[0]
	if got.Direction != "NE" || got.Timeout != "45s" || got.LLM.Agent != "codex" || got.LLM.Model != "gpt-5.5" {
		t.Fatalf("metadata not saved: %+v", got)
	}

	w.text = newTextBuffer("# New\nupdated rubric")
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if !done || w.errMsg != "" {
		t.Fatalf("ctrl+s should save skill and finish, done=%v err=%q", done, w.errMsg)
	}
	raw, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# New\nupdated rubric\n" {
		t.Fatalf("skill not saved: %q", raw)
	}
}

func TestEditWizardSkipDoesNotSaveCurrentStep(t *testing.T) {
	cfg, skill := writeEditorFixture(t)
	w := NewEditWizard(cfg)
	next, _, _ := w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next

	w.text = newTextBuffer(`name: Architect
type: agent
direction: S
timeout: 1s
llm:
    agent: codex
    skill: ./skills/architect/SKILL.md`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	w = next
	if done || w.phase != editSkill {
		t.Fatalf("ctrl+n should skip metadata and continue, phase=%v done=%v", w.phase, done)
	}
	f, _, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if f.Verifiers[0].Direction != "N" || f.Verifiers[0].Timeout != "90s" {
		t.Fatalf("metadata skip wrote changes: %+v", f.Verifiers[0])
	}

	w.text = newTextBuffer("# Unsaved")
	_, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !done {
		t.Fatal("ctrl+n should finish from skill step")
	}
	raw, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# Old\n" {
		t.Fatalf("skill skip wrote changes: %q", raw)
	}
}

func TestModelEditKeyOpensWizard(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	m := New(daemon.NewState()).WithConfigEditor(cfg)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	got := next.(Model)
	if got.editor == nil {
		t.Fatal("e should open verifier edit wizard")
	}
	if got.editor.configPath != cfg {
		t.Fatalf("wizard configPath = %q, want %q", got.editor.configPath, cfg)
	}
}

func TestModelEditKeyOpensSelectedVerifier(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	state := daemon.NewState()
	state.UpsertVerifier(ipc.VerifierStatus{Name: "Architect", Direction: "N", Distance: 0.4})
	m := New(state).WithConfigEditor(cfg)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	got := next.(Model)
	if got.editor == nil {
		t.Fatal("e should open verifier edit wizard")
	}
	if got.editor.phase != editMetadata || got.editor.selected != 0 {
		t.Fatalf("wizard should start on selected verifier metadata, phase=%v selected=%d", got.editor.phase, got.editor.selected)
	}
}

func TestModelConfigSavedCallback(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	called := 0
	m := New(daemon.NewState()).
		WithConfigEditor(cfg).
		WithConfigSaved(func() error {
			called++
			return nil
		})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	m.editor.text = newTextBuffer(`name: Architect
type: agent
direction: NE
timeout: 45s
llm:
    agent: claude
    skill: ./skills/architect/SKILL.md`)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(Model)
	if called != 1 {
		t.Fatalf("config saved callback called %d times, want 1", called)
	}
	if m.editor == nil || m.editor.saved {
		t.Fatalf("wizard should continue with saved flag consumed, editor=%v", m.editor)
	}
}

func TestEditWizardViewShowsSelectionAndStepHelp(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewEditWizard(cfg)
	w.width = 100
	w.height = 30

	out := w.View()
	for _, want := range []string{"HUD verifier editor", "Architect", "enter edit", "esc abort"} {
		if !strings.Contains(out, want) {
			t.Fatalf("selection view missing %q in:\n%s", want, out)
		}
	}

	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next
	if done {
		t.Fatal("enter should not close wizard")
	}
	out = w.View()
	for _, want := range []string{"Step 1/2", "hud.yaml", "ctrl+s save", "ctrl+n skip", `direction: "N"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("metadata view missing %q in:\n%s", want, out)
		}
	}
}

func TestTextBufferEditingKeys(t *testing.T) {
	b := newTextBuffer("ab")
	b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if got := b.String(); got != "cab" {
		t.Fatalf("insert at cursor = %q, want cab", got)
	}

	b.Update(tea.KeyMsg{Type: tea.KeyRight})
	b.Update(tea.KeyMsg{Type: tea.KeyRight})
	b.Update(tea.KeyMsg{Type: tea.KeyRight})
	b.Update(tea.KeyMsg{Type: tea.KeyEnter})
	b.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if got := b.String(); got != "cab\nd" {
		t.Fatalf("split/insert = %q, want cab\\nd", got)
	}

	b.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := b.String(); got != "cab\n" {
		t.Fatalf("backspace = %q, want cab newline", got)
	}

	b.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := b.String(); got != "cab" {
		t.Fatalf("backspace at line start should join lines, got %q", got)
	}

	b.Update(tea.KeyMsg{Type: tea.KeyLeft})
	b.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if got := b.String(); got != "ca" {
		t.Fatalf("delete = %q, want ca", got)
	}
}
