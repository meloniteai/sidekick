package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/meloniteai/sidekick/internal/config"
)

const removeTestSeed = `goal_source: prompt
verifiers:
  - name: Architect
    type: agent
    direction: N
    llm:
      agent: claude
      skill: ./skills/architect/SKILL.md
  - name: Smoke
    type: command
    direction: S
    command:
      - ./verifiers/smoke.sh
`

func writeSeed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sidekick.yaml"), []byte(removeTestSeed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return dir
}

func runRemove(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"remove"}, args...))
	err := root.Execute()
	return out.String(), err
}

func readVerifiers(t *testing.T, dir string) *config.File {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "sidekick.yaml"))
	if err != nil {
		t.Fatalf("read sidekick.yaml: %v", err)
	}
	var f config.File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &f
}

func TestRemoveDeletesNamedVerifier(t *testing.T) {
	dir := writeSeed(t)
	out, err := runRemove(t, "y\n", "Smoke")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	f := readVerifiers(t, dir)
	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier left, got %d", len(f.Verifiers))
	}
	if f.Verifiers[0].Name != "Architect" {
		t.Errorf("remaining = %q, want Architect", f.Verifiers[0].Name)
	}
	if !strings.Contains(out, "Removed \"Smoke\"") {
		t.Errorf("expected confirmation in output, got:\n%s", out)
	}
}

func TestRemoveCaseInsensitive(t *testing.T) {
	dir := writeSeed(t)
	if _, err := runRemove(t, "", "--yes", "smoke"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	f := readVerifiers(t, dir)
	if len(f.Verifiers) != 1 || f.Verifiers[0].Name != "Architect" {
		t.Errorf("verifiers after remove: %+v", f.Verifiers)
	}
}

func TestRemoveYesSkipsPrompt(t *testing.T) {
	dir := writeSeed(t)
	out, err := runRemove(t, "", "--yes", "Architect")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if strings.Contains(out, "Delete verifier") {
		t.Errorf("--yes should skip the prompt, got:\n%s", out)
	}
	f := readVerifiers(t, dir)
	if len(f.Verifiers) != 1 || f.Verifiers[0].Name != "Smoke" {
		t.Errorf("verifiers after remove: %+v", f.Verifiers)
	}
}

func TestRemoveAbortOnNo(t *testing.T) {
	dir := writeSeed(t)
	out, err := runRemove(t, "n\n", "Smoke")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want aborted error, got err=%v out=%s", err, out)
	}
	f := readVerifiers(t, dir)
	if len(f.Verifiers) != 2 {
		t.Errorf("verifiers should be untouched, got %d", len(f.Verifiers))
	}
}

func TestRemoveUnknownName(t *testing.T) {
	writeSeed(t)
	_, err := runRemove(t, "", "--yes", "DoesNotExist")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestRemoveAliasRm(t *testing.T) {
	dir := writeSeed(t)
	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"rm", "--yes", "Architect"})
	if err := root.Execute(); err != nil {
		t.Fatalf("rm alias: %v\n%s", err, out.String())
	}
	f := readVerifiers(t, dir)
	if len(f.Verifiers) != 1 || f.Verifiers[0].Name != "Smoke" {
		t.Errorf("verifiers after rm: %+v", f.Verifiers)
	}
}
