//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/meloniteai/sidekick/internal/config"
)

// TestLocalVerifierWizardCreatesEntry shells out to `sidekick verifier add
// --local` with a scripted stdin, exercising the non-TTY text wizard. The
// wizard's TTY (palette) and text paths share the same persistence logic
// (loadOrInit → append → config.Save), so the cheaper text path is
// sufficient to gate that "wizard finished → .sidekick/sidekick.yaml gained an entry".
// We don't run the verifier; the goal is to validate file persistence.
func TestLocalVerifierWizardCreatesEntry(t *testing.T) {
	repo := ScratchRepo(t)

	// Walk: Name → Direction → Type (2 = command) → Command → Timeout →
	// Configure permissions? (blank = no) → Confirm (blank = yes is shown
	// as Y/n, but the wizard expects an explicit "y"). Trailing newline
	// keeps the reader from blocking after the last prompt.
	stdin := strings.Join([]string{
		"WizardE2E",
		"NE",
		"2",
		"./verifiers/wizard-e2e.sh",
		"30s",
		"",
		"y",
		"",
	}, "\n")

	out, err := RunSidekick(t, repo, strings.NewReader(stdin),
		"verifier", "add", "--local",
	)
	if err != nil {
		t.Fatalf("verifier add --local: %v\noutput:\n%s", err, out)
	}

	raw, err := os.ReadFile(ProjectSidekickYAML(repo))
	if err != nil {
		t.Fatalf("read sidekick.yaml: %v\nadd output:\n%s", err, out)
	}
	var f config.File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal sidekick.yaml: %v\nraw=%s", err, raw)
	}
	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier, got %d\nyaml=%s", len(f.Verifiers), raw)
	}
	v := f.Verifiers[0]
	if v.Name != "WizardE2E" {
		t.Errorf("name = %q, want WizardE2E", v.Name)
	}
	if v.Direction != "NE" {
		t.Errorf("direction = %q, want NE", v.Direction)
	}
	if v.Type != "command" {
		t.Errorf("type = %q, want command", v.Type)
	}
	if len(v.Command) != 1 || v.Command[0] != "./verifiers/wizard-e2e.sh" {
		t.Errorf("command = %v, want [./verifiers/wizard-e2e.sh]", v.Command)
	}
	// Wizard-created verifiers have no Source spec — that's reserved for
	// remote pins. (T3 covers the remote-add path.)
	if v.Source != nil {
		t.Errorf("source = %+v, want nil for local wizard verifier", v.Source)
	}
}
