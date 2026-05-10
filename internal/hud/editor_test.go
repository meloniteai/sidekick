package hud

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/config"
	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/verifier"
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

func TestCreateWizardAddsAgentVerifierAndSkill(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewCreateWizard(cfg)
	if !w.create || w.phase != editCreateBasics {
		t.Fatalf("new wizard should start in create basics, create=%v phase=%v", w.create, w.phase)
	}

	w.text = newTextBuffer(`name: Quality
direction: E
timeout: 2m
disabled: false`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateType {
		t.Fatalf("basics should continue to type, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}

	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateConfig || w.createKind != "agent" {
		t.Fatalf("type should continue to agent config, phase=%v kind=%q done=%v", w.phase, w.createKind, done)
	}

	w.text = newTextBuffer(`llm:
    agent: codex
    model: gpt-5.5
    thinking: medium
    skill: ./skills/quality/SKILL.md`)
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateSkill {
		t.Fatalf("agent config should continue to skill, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}

	w.text = newTextBuffer("# Quality\nscore the session")
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if !done || w.errMsg != "" || !w.saved {
		t.Fatalf("skill save should create verifier, done=%v saved=%v err=%q", done, w.saved, w.errMsg)
	}

	f, _, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Verifiers) != 2 {
		t.Fatalf("verifier count = %d, want 2", len(f.Verifiers))
	}
	got := f.Verifiers[1]
	if got.Name != "Quality" || got.Type != "agent" || got.Direction != "E" || got.LLM.Agent != "codex" {
		t.Fatalf("created verifier mismatch: %+v", got)
	}
	skill := filepath.Join(filepath.Dir(cfg), "skills", "quality", "SKILL.md")
	raw, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# Quality\nscore the session\n" {
		t.Fatalf("skill content = %q", raw)
	}
}

func TestCreateWizardAddsCommandVerifier(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewCreateWizard(cfg)
	w.text = newTextBuffer(`name: Smoke
direction: S
timeout: 30s`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateType {
		t.Fatalf("basics should continue to type, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyDown})
	w = next
	if done {
		t.Fatal("down should not close type picker")
	}
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next
	if done || w.phase != editCreateConfig || w.createKind != "command" {
		t.Fatalf("type should continue to command config, phase=%v kind=%q done=%v", w.phase, w.createKind, done)
	}

	w.text = newTextBuffer(`command:
    - ./verifiers/smoke.sh
    - --quick`)
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if !done || w.errMsg != "" || !w.saved {
		t.Fatalf("command config save should finish, done=%v saved=%v err=%q", done, w.saved, w.errMsg)
	}

	f, _, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Verifiers[1]
	if got.Name != "Smoke" || got.Type != "command" || got.Direction != "S" {
		t.Fatalf("created verifier mismatch: %+v", got)
	}
	if strings.Join(got.Command, " ") != "./verifiers/smoke.sh --quick" {
		t.Fatalf("command = %#v", got.Command)
	}
}

func TestCreateWizardAddsBinaryVerifier(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewCreateWizard(cfg)
	w.text = newTextBuffer(`name: Checks
direction: W`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateType {
		t.Fatalf("basics should continue to type, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}
	for i := 0; i < 2; i++ {
		next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyDown})
		w = next
		if done {
			t.Fatal("down should not close type picker")
		}
	}
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next
	if done || w.phase != editCreateConfig || w.createKind != "binary" {
		t.Fatalf("type should continue to binary config, phase=%v kind=%q done=%v", w.phase, w.createKind, done)
	}

	w.text = newTextBuffer(`binary:
    command:
      - go
      - test
      - ./...
    pass_reason: ok
    fail_reason: failed`)
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if !done || w.errMsg != "" || !w.saved {
		t.Fatalf("binary config save should finish, done=%v saved=%v err=%q", done, w.saved, w.errMsg)
	}

	f, _, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Verifiers[1]
	if got.Name != "Checks" || got.Type != "binary" || strings.Join(got.Binary.Command, " ") != "go test ./..." {
		t.Fatalf("created verifier mismatch: %+v", got)
	}
}

func TestCreateWizardRejectsDuplicateName(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewCreateWizard(cfg)
	w.text = newTextBuffer(`name: Architect
direction: E`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateBasics || !strings.Contains(w.errMsg, "already exists") {
		t.Fatalf("duplicate name should stay on basics, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}
}

func TestCreateWizardDoesNotWriteAgentSkillOutsideConfigDir(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	w := NewCreateWizard(cfg)
	w.text = newTextBuffer(`name: External
direction: W`)
	next, _, done := w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateType {
		t.Fatalf("basics should continue to type, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateConfig {
		t.Fatalf("type should continue to config, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}
	w.text = newTextBuffer(`llm:
    agent: claude
    skill: ../outside/SKILL.md`)
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || w.phase != editCreateSkill || !strings.Contains(w.errMsg, "inside") {
		t.Fatalf("unsafe skill path should stay on skill step with error, phase=%v done=%v err=%q", w.phase, done, w.errMsg)
	}

	w.text = newTextBuffer("# Should not be written")
	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	w = next
	if done || !strings.Contains(w.errMsg, "inside") {
		t.Fatalf("ctrl+s should refuse outside write, done=%v err=%q", done, w.errMsg)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg), "..", "outside", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("outside skill file should not exist, stat err=%v", err)
	}

	next, _, done = w.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	w = next
	if !done || w.errMsg != "" || !w.saved {
		t.Fatalf("ctrl+n should save config only, done=%v saved=%v err=%q", done, w.saved, w.errMsg)
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

func TestModelNewKeyOpensCreateWizard(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	m := New(daemon.NewState()).WithConfigEditor(cfg)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got := next.(Model)
	if got.editor == nil {
		t.Fatal("n should open verifier create wizard")
	}
	if !got.editor.create || got.editor.phase != editCreateBasics {
		t.Fatalf("wizard should start creating, create=%v phase=%v", got.editor.create, got.editor.phase)
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

func TestModelCreateWizardReloadsActiveVerifierTracking(t *testing.T) {
	cfg, _ := writeEditorFixture(t)
	// The wizard saves a Smoke verifier pointing at ./verifiers/smoke.sh; the
	// onConfigSaved callback then calls Resolve, which validates that local
	// scripts referenced by config exist on disk. Stage the script so the
	// post-save reload mirrors a real user setting up files before saving.
	smokeScript := filepath.Join(filepath.Dir(cfg), "verifiers", "smoke.sh")
	if err := os.MkdirAll(filepath.Dir(smokeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(smokeScript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, path, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := f.Resolve(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	state := daemon.NewState()
	runner := verifier.NewRunner(context.Background(), state, initial)
	defer runner.Stop()

	m := New(state).
		WithConfigEditor(cfg).
		WithConfigSaved(func() error {
			next, loadedPath, err := config.Load(cfg)
			if err != nil {
				return err
			}
			vs, err := next.Resolve(filepath.Dir(loadedPath))
			if err != nil {
				return err
			}
			runner.ReplaceVerifiers(vs)
			return nil
		})

	nextModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = nextModel.(Model)
	m.editor.text = newTextBuffer(`name: Smoke
direction: S
timeout: 30s`)
	nextModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nextModel.(Model)
	nextModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nextModel.(Model)
	nextModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nextModel.(Model)
	m.editor.text = newTextBuffer(`command:
    - ./verifiers/smoke.sh`)
	nextModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nextModel.(Model)

	if m.editor != nil {
		t.Fatal("create wizard should close after saving command verifier")
	}
	if len(m.snapshot.Verifiers) != 2 {
		t.Fatalf("snapshot verifier count = %d, want 2", len(m.snapshot.Verifiers))
	}
	got := m.snapshot.Verifiers[1]
	if got.Name != "Smoke" || got.Direction != "S" || got.Config.Type != "command" {
		t.Fatalf("new verifier not tracked in active snapshot: %+v", got)
	}
	if got.Distance != 1.0 || got.Reason != "awaiting first run" {
		t.Fatalf("new verifier should have initial runtime status, got distance=%v reason=%q", got.Distance, got.Reason)
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
