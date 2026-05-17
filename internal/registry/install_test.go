package registry

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/meloniteai/sidekick/internal/config"
)

func sampleAgentManifest() Manifest {
	return Manifest{
		Name:      "needs-tests",
		Type:      "agent",
		Direction: "NE",
		Slug:      "needs-tests",
		Artefact:  "SKILL.md",
		RawURL:    "https://raw.example.com/acme/verifiers/main/agent/needs-tests/SKILL.md",
		SHA256:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Agent: ManifestAgent{
			Agent: "claude",
			Model: "claude-sonnet-4-6",
		},
		Permissions: ManifestPermSet{
			Filesystem:   "read-only",
			AllowedTools: []string{"Bash(go test:*)"},
		},
	}
}

// TestInstall_ProjectCreatesFile exercises the case where no sidekick.yaml
// exists at the target path yet: Install should create it (with parent
// dirs) and write the verifier into a fresh File.
func TestInstall_ProjectCreatesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "subdir", "sidekick.yaml")

	res, err := Install(InstallOptions{
		Scope:       ScopeProject,
		ProjectPath: target,
		Manifest:    sampleAgentManifest(),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Path != target {
		t.Fatalf("Path = %q, want %q", res.Path, target)
	}
	if res.FinalName != "needs-tests" {
		t.Fatalf("FinalName = %q, want needs-tests", res.FinalName)
	}

	f, _, err := config.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier, got %d", len(f.Verifiers))
	}
	vs := f.Verifiers[0]
	if vs.Source == nil || vs.Source.SHA256 == "" {
		t.Fatalf("source/sha not persisted: %+v", vs)
	}
	if vs.LLM.Agent != "claude" || vs.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("agent llm fields not persisted: %+v", vs.LLM)
	}
	if vs.Permissions == nil || len(vs.Permissions.AllowedTools) != 1 {
		t.Fatalf("allowed_tools not persisted: %+v", vs.Permissions)
	}
}

// TestInstall_RenamesOnConflict installs the same manifest twice into
// the same sidekick.yaml — the second install must land on a unique name
// rather than overwriting the first entry.
func TestInstall_RenamesOnConflict(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sidekick.yaml")
	m := sampleAgentManifest()

	if _, err := Install(InstallOptions{Scope: ScopeProject, ProjectPath: target, Manifest: m}); err != nil {
		t.Fatal(err)
	}
	res, err := Install(InstallOptions{Scope: ScopeProject, ProjectPath: target, Manifest: m})
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalName != "needs-tests-2" {
		t.Fatalf("FinalName = %q, want needs-tests-2", res.FinalName)
	}
	f, _, err := config.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Verifiers) != 2 {
		t.Fatalf("want 2 verifiers, got %d", len(f.Verifiers))
	}
	names := []string{f.Verifiers[0].Name, f.Verifiers[1].Name}
	if names[0] != "needs-tests" || names[1] != "needs-tests-2" {
		t.Fatalf("verifier names = %v", names)
	}
}

// TestInstall_Global routes the write to config.GlobalPath() when
// ScopeGlobal is selected, using $SIDEKICK_GLOBAL_CONFIG as the override
// path so the user's real home is never touched.
func TestInstall_Global(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", target)

	res, err := Install(InstallOptions{
		Scope:    ScopeGlobal,
		Manifest: sampleAgentManifest(),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Path != target {
		t.Fatalf("Path = %q, want %q", res.Path, target)
	}
}

// TestInstall_RejectsManifestWithoutSHA defends against silent installs
// of unpinned remote scripts. If a manifest is malformed in the catalog,
// the installer must refuse rather than write an unpinned source: block.
func TestInstall_RejectsManifestWithoutSHA(t *testing.T) {
	m := sampleAgentManifest()
	m.SHA256 = ""
	_, err := Install(InstallOptions{
		Scope:       ScopeProject,
		ProjectPath: filepath.Join(t.TempDir(), "sidekick.yaml"),
		Manifest:    m,
	})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("expected sha256-required error, got %v", err)
	}
}
