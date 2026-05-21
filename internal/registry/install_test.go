package registry

import (
	"os"
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

// stubFetch returns a fetcher that always yields body, ignoring the URL/sha.
// Project installs materialise the artefact into the repo, so the tests must
// supply the bytes rather than hitting the network.
func stubFetch(body []byte) func(string, string) ([]byte, error) {
	return func(string, string) ([]byte, error) { return body, nil }
}

// TestInstall_ProjectMaterialisesSkill exercises a project-scoped install:
// no sidekick.yaml exists yet, so Install creates it (with parent dirs),
// writes the SKILL.md into the project's .sidekick/ dir, and points the spec
// at that local path with NO source block — the verifier is a fully-editable
// fork, not a cache-pinned remote.
func TestInstall_ProjectMaterialisesSkill(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "subdir", ".sidekick", "sidekick.yaml")
	body := []byte("# needs tests\nrubric body\n")

	res, err := Install(InstallOptions{
		Scope:       ScopeProject,
		ProjectPath: target,
		Manifest:    sampleAgentManifest(),
		Fetch:       stubFetch(body),
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
	if vs.Source != nil {
		t.Fatalf("project install must not pin a source block: %+v", vs.Source)
	}
	if vs.LLM.Skill != "./skills/needs-tests/SKILL.md" {
		t.Fatalf("llm.skill = %q, want ./skills/needs-tests/SKILL.md", vs.LLM.Skill)
	}
	if vs.LLM.Agent != "claude" || vs.LLM.Model != "claude-sonnet-4-6" {
		t.Fatalf("agent llm fields not persisted: %+v", vs.LLM)
	}
	if vs.Permissions == nil || len(vs.Permissions.AllowedTools) != 1 {
		t.Fatalf("allowed_tools not persisted: %+v", vs.Permissions)
	}

	skill := filepath.Join(dir, "subdir", ".sidekick", "skills", "needs-tests", "SKILL.md")
	got, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("materialised skill not on disk: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("materialised skill = %q, want %q", got, body)
	}

	// The forked verifier must validate as a normal local agent verifier.
	if _, err := f.Resolve(filepath.Join(dir, "subdir", ".sidekick")); err != nil {
		t.Fatalf("Resolve of materialised verifier: %v", err)
	}
}

// TestInstall_ProjectMaterialisesScript covers the command path: the artefact
// lands under .sidekick/verifiers/<slug>.sh, executable, and the spec's command
// points at it.
func TestInstall_ProjectMaterialisesScript(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".sidekick", "sidekick.yaml")
	body := []byte("#!/bin/sh\necho '{\"distance\":0,\"reason\":\"ok\"}'\n")

	m := sampleAgentManifest()
	m.Name = "smoke"
	m.Slug = "smoke"
	m.Type = "command"
	m.Agent = ManifestAgent{}

	if _, err := Install(InstallOptions{
		Scope:       ScopeProject,
		ProjectPath: target,
		Manifest:    m,
		Fetch:       stubFetch(body),
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	f, _, err := config.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	vs := f.Verifiers[0]
	if vs.Source != nil {
		t.Fatalf("project install must not pin a source block: %+v", vs.Source)
	}
	want := "./verifiers/smoke.sh"
	if len(vs.Command) != 1 || vs.Command[0] != want {
		t.Fatalf("command = %#v, want [%q]", vs.Command, want)
	}
	script := filepath.Join(dir, ".sidekick", "verifiers", "smoke.sh")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("materialised script not on disk: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("materialised script %s mode %o is not executable", script, info.Mode().Perm())
	}
}

func TestInstall_ProjectDefaultPathIsSidekickDir(t *testing.T) {
	dir := t.TempDir()
	body := []byte("# rubric\n")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	res, err := Install(InstallOptions{
		Scope:    ScopeProject,
		Manifest: sampleAgentManifest(),
		Fetch:    stubFetch(body),
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := filepath.Join(dir, ".sidekick", "sidekick.yaml")
	if same, err := sameFilesystemPathForTest(res.Path, want); err != nil {
		t.Fatal(err)
	} else if !same {
		t.Fatalf("Path = %q, want %q", res.Path, want)
	}
	if _, err := os.Stat(filepath.Join(dir, ".sidekick", "skills", "needs-tests", "SKILL.md")); err != nil {
		t.Fatalf("materialised skill not under .sidekick: %v", err)
	}
}

// TestInstall_RenamesOnConflict installs the same manifest twice into
// the same sidekick.yaml — the second install must land on a unique name
// rather than overwriting the first entry.
func TestInstall_RenamesOnConflict(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".sidekick", "sidekick.yaml")
	m := sampleAgentManifest()
	fetch := stubFetch([]byte("# rubric\n"))

	if _, err := Install(InstallOptions{Scope: ScopeProject, ProjectPath: target, Manifest: m, Fetch: fetch}); err != nil {
		t.Fatal(err)
	}
	res, err := Install(InstallOptions{Scope: ScopeProject, ProjectPath: target, Manifest: m, Fetch: fetch})
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
	// The de-duplicated name must drive the on-disk slug so the second
	// install does not clobber the first verifier's skill.
	if f.Verifiers[1].LLM.Skill != "./skills/needs-tests-2/SKILL.md" {
		t.Fatalf("second skill path = %q, want needs-tests-2 dir", f.Verifiers[1].LLM.Skill)
	}
	for _, slug := range []string{"needs-tests", "needs-tests-2"} {
		if _, err := os.Stat(filepath.Join(dir, ".sidekick", "skills", slug, "SKILL.md")); err != nil {
			t.Fatalf("missing skill for %s: %v", slug, err)
		}
	}
}

// TestInstall_Global routes the write to config.GlobalPath() when
// ScopeGlobal is selected, using $SIDEKICK_GLOBAL_CONFIG as the override
// path so the user's real home is never touched. Unlike a project install,
// a global install stays pinned to the shared cache: it keeps a source block,
// never materialises a local skill, and never downloads at install time.
func TestInstall_Global(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", target)

	res, err := Install(InstallOptions{
		Scope:    ScopeGlobal,
		Manifest: sampleAgentManifest(),
		// No Fetch: a global install must not call it. The fake manifest URL
		// would fail to resolve if it did.
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Path != target {
		t.Fatalf("Path = %q, want %q", res.Path, target)
	}

	f, _, err := config.Load(target)
	if err != nil {
		t.Fatal(err)
	}
	vs := f.Verifiers[0]
	if vs.Source == nil || vs.Source.SHA256 == "" {
		t.Fatalf("global install must keep a source pin: %+v", vs.Source)
	}
	if vs.LLM.Skill != "" {
		t.Fatalf("global install must not materialise a local skill, got %q", vs.LLM.Skill)
	}
	if _, err := os.Stat(filepath.Join(dir, ".sidekick")); !os.IsNotExist(err) {
		t.Fatalf("global install must not create a project .sidekick dir, stat err=%v", err)
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

func TestCopyVerifier_GlobalRemoteToProjectMaterialises(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "home", ".sidekick", "sidekick.yaml")
	project := filepath.Join(dir, "repo", ".sidekick", "sidekick.yaml")
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(global, &config.File{Verifiers: []config.VerifierSpec{{
		Name:      "Remote",
		Type:      "agent",
		Direction: "N",
		LLM:       config.AgentVerifierSpec{Agent: "claude", Model: "sonnet"},
		Source: &config.SourceSpec{
			URL:    "https://raw.example.com/remote/SKILL.md",
			SHA256: strings.Repeat("a", 64),
		},
	}}}); err != nil {
		t.Fatal(err)
	}

	res, err := CopyVerifier(CopyVerifierOptions{
		SourcePath:  global,
		Target:      ScopeProject,
		ProjectPath: project,
		Name:        "Remote",
		Fetch:       stubFetch([]byte("# remote\n")),
	})
	if err != nil {
		t.Fatalf("CopyVerifier: %v", err)
	}
	if res.Path != project || res.FinalName != "Remote" {
		t.Fatalf("result = %+v, want project/Remote", res)
	}
	f, _, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	vs := f.Verifiers[0]
	if vs.Source != nil {
		t.Fatalf("project copy should be local, got source %+v", vs.Source)
	}
	if vs.LLM.Skill != "./skills/remote/SKILL.md" {
		t.Fatalf("skill = %q", vs.LLM.Skill)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "repo", ".sidekick", "skills", "remote", "SKILL.md")); err != nil {
		t.Fatal(err)
	} else if string(got) != "# remote\n" {
		t.Fatalf("copied body = %q", got)
	}
}

func TestCopyVerifier_ProjectLocalToGlobalCopiesSkill(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "repo", ".sidekick", "sidekick.yaml")
	global := filepath.Join(dir, "home", ".sidekick", "sidekick.yaml")
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", global)
	skill := filepath.Join(dir, "repo", ".sidekick", "skills", "local", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("# local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(project, &config.File{Verifiers: []config.VerifierSpec{{
		Name:      "Local",
		Type:      "agent",
		Direction: "S",
		LLM:       config.AgentVerifierSpec{Agent: "claude", Skill: "./skills/local/SKILL.md"},
	}}}); err != nil {
		t.Fatal(err)
	}

	res, err := CopyVerifier(CopyVerifierOptions{SourcePath: project, Target: ScopeGlobal, Name: "Local"})
	if err != nil {
		t.Fatalf("CopyVerifier: %v", err)
	}
	if res.Path != global {
		t.Fatalf("Path = %q, want %q", res.Path, global)
	}
	f, _, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	if f.Verifiers[0].LLM.Skill != "./skills/local/SKILL.md" {
		t.Fatalf("global skill = %q", f.Verifiers[0].LLM.Skill)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "home", ".sidekick", "skills", "local", "SKILL.md")); err != nil {
		t.Fatal(err)
	} else if string(got) != "# local\n" {
		t.Fatalf("copied body = %q", got)
	}
}

func sameFilesystemPathForTest(a, b string) (bool, error) {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(aa); err == nil {
		aa = resolved
	}
	if resolved, err := filepath.EvalSymlinks(bb); err == nil {
		bb = resolved
	}
	return filepath.Clean(aa) == filepath.Clean(bb), nil
}
