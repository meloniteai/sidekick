//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/meloniteai/sidekick/internal/config"
)

// catalogSkill is a known-stable SKILL.md in the public verifier catalog.
// The artefact is a markdown skill body — `sidekick verifier add` sniffs the
// content and classifies it as an agent verifier, downloads it, computes a
// sha256, and writes a pinned source entry into .sidekick/sidekick.yaml.
const catalogSkill = "https://raw.githubusercontent.com/meloniteai/sidekick-verifiers/main/agent/agents-md/SKILL.md"

// TestRemoteVerifierAddPinsArtefact exercises the remote-install path end
// to end: it fetches a real artefact from the catalog, classifies it,
// computes the sha256, and persists a verifier entry in .sidekick/sidekick.yaml that
// pins both URL and hash. We don't run the resulting verifier — the goal
// is to confirm the registration plumbing, not the agent execution path
// (T2 covers that).
func TestRemoteVerifierAddPinsArtefact(t *testing.T) {
	repo := ScratchRepo(t)

	out, err := RunSidekick(t, repo, nil,
		"verifier", "add", catalogSkill,
		"--name", "AgentsMD",
		"--direction", "NE",
		"--yes",
	)
	if err != nil {
		t.Fatalf("verifier add: %v\noutput:\n%s", err, out)
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
	if v.Name != "AgentsMD" {
		t.Errorf("name = %q, want AgentsMD", v.Name)
	}
	if v.Type != "agent" {
		t.Errorf("type = %q, want agent (SKILL.md should be classified as agent)", v.Type)
	}
	if v.Source == nil {
		t.Fatalf("source spec missing — remote add must record provenance")
	}
	if v.Source.URL != catalogSkill {
		t.Errorf("source.url = %q, want %q", v.Source.URL, catalogSkill)
	}
	if !looksLikeSHA256(v.Source.SHA256) {
		t.Errorf("source.sha256 = %q, want 64 lowercase hex chars", v.Source.SHA256)
	}
}

func looksLikeSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
