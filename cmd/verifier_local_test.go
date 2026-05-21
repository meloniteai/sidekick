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

// isolateGlobalConfig points SIDEKICK_GLOBAL_CONFIG at a path inside dir that
// won't exist, so config.Load can't fall back to the user's real
// ~/.sidekick/sidekick.yaml when no local sidekick.yaml is present.
func isolateGlobalConfig(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("SIDEKICK_GLOBAL_CONFIG", filepath.Join(dir, "no-global.yaml"))
}

// drive runs the cobra add command with --local against a scripted stdin in
// a temp dir. Returns the resulting sidekick.yaml (parsed) and the captured
// stdout, so tests can assert on both the YAML state and what the user saw.
func drive(t *testing.T, scriptedStdin string, args ...string) (*config.File, string) {
	t.Helper()
	dir := t.TempDir()
	isolateGlobalConfig(t, dir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(scriptedStdin))
	root.SetArgs(append([]string{"add", "--local"}, args...))

	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n--- output ---\n%s", err, out.String())
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".sidekick", "sidekick.yaml"))
	if err != nil {
		t.Fatalf("read sidekick.yaml: %v", err)
	}
	var f config.File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal sidekick.yaml: %v\nraw=%s", err, raw)
	}
	return &f, out.String()
}

func TestLocalAddWizardCommandVerifier(t *testing.T) {
	// name, direction, type (1=agent, 2=command), command, timeout, permissions?, confirm
	stdin := strings.Join([]string{
		"MyScorer",         // Name
		"SE",               // Direction
		"2",                // Type: command
		"./verifiers/x.sh", // Command
		"30s",              // Timeout
		"",                 // Configure permissions? -> default N
		"y",                // Confirm
		"",                 // trailing newline
	}, "\n")

	f, out := drive(t, stdin)

	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier, got %d (raw output:\n%s)", len(f.Verifiers), out)
	}
	v := f.Verifiers[0]
	if v.Name != "MyScorer" {
		t.Errorf("name = %q, want MyScorer", v.Name)
	}
	if v.Direction != "SE" {
		t.Errorf("direction = %q, want SE", v.Direction)
	}
	if v.Type != "command" {
		t.Errorf("type = %q, want command", v.Type)
	}
	if len(v.Command) != 1 || v.Command[0] != "./verifiers/x.sh" {
		t.Errorf("command = %v, want [./verifiers/x.sh]", v.Command)
	}
	if v.Timeout != "30s" {
		t.Errorf("timeout = %q, want 30s", v.Timeout)
	}
	if v.Source != nil {
		t.Errorf("local verifier should not have source block, got %+v", v.Source)
	}
	if !strings.Contains(out, "Note: script") {
		t.Errorf("expected missing-script note in output, got:\n%s", out)
	}
}

func TestLocalAddWizardAgentVerifierWithDefaults(t *testing.T) {
	// Empty answers everywhere → all defaults; type defaults to agent.
	stdin := strings.Join([]string{
		"",  // Name (default NewVerifier)
		"",  // Direction (default N — first unused)
		"",  // Type (default 1 = agent)
		"",  // Agent (default claude)
		"",  // Model (optional)
		"",  // Thinking (optional)
		"",  // Skill (default ./skills/newverifier/SKILL.md)
		"",  // Timeout (blank)
		"",  // Permissions? (default N)
		"y", // Confirm
		"",
	}, "\n")

	f, _ := drive(t, stdin)
	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier, got %d", len(f.Verifiers))
	}
	v := f.Verifiers[0]
	if v.Name != "NewVerifier" || v.Direction != "N" || v.Type != "agent" {
		t.Errorf("defaults mismatched: name=%q direction=%q type=%q", v.Name, v.Direction, v.Type)
	}
	if v.LLM.Agent != "claude" {
		t.Errorf("agent = %q, want claude", v.LLM.Agent)
	}
	if v.LLM.Skill != "./skills/newverifier/SKILL.md" {
		t.Errorf("skill = %q, want ./skills/newverifier/SKILL.md", v.LLM.Skill)
	}
}

func TestLocalAddWizardBinaryWithPermissions(t *testing.T) {
	stdin := strings.Join([]string{
		"Smoke",         // Name
		"S",             // Direction
		"binary",        // Type by label
		"go test ./...", // Command
		"checks passed", // pass_reason
		"checks failed", // fail_reason
		"",              // Timeout
		"y",             // Configure permissions?
		"n",             // Network?
		"read-only",     // Filesystem
		"PATH:GOPATH",   // Env
		"y",             // Confirm
		"",
	}, "\n")

	f, _ := drive(t, stdin)
	if len(f.Verifiers) != 1 {
		t.Fatalf("want 1 verifier, got %d", len(f.Verifiers))
	}
	v := f.Verifiers[0]
	if v.Type != "binary" {
		t.Fatalf("type = %q, want binary", v.Type)
	}
	wantCmd := []string{"go", "test", "./..."}
	if len(v.Binary.Command) != len(wantCmd) {
		t.Fatalf("binary.command = %v, want %v", v.Binary.Command, wantCmd)
	}
	for i, want := range wantCmd {
		if v.Binary.Command[i] != want {
			t.Errorf("binary.command[%d] = %q, want %q", i, v.Binary.Command[i], want)
		}
	}
	if v.Binary.PassReason != "checks passed" || v.Binary.FailReason != "checks failed" {
		t.Errorf("reasons: pass=%q fail=%q", v.Binary.PassReason, v.Binary.FailReason)
	}
	if v.Permissions == nil {
		t.Fatal("permissions block missing")
	}
	if v.Permissions.Network {
		t.Error("network should be false")
	}
	if v.Permissions.Filesystem != "read-only" {
		t.Errorf("fs = %q, want read-only", v.Permissions.Filesystem)
	}
	if got := strings.Join(v.Permissions.Env, ","); got != "PATH,GOPATH" {
		t.Errorf("env = %v, want [PATH GOPATH]", v.Permissions.Env)
	}
}

func TestLocalAddWizardRejectsDuplicateName(t *testing.T) {
	// Pre-populate sidekick.yaml with an existing verifier.
	dir := t.TempDir()
	isolateGlobalConfig(t, dir)
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	preExisting := []byte(`goal_source: prompt
verifiers:
  - name: Existing
    type: command
    direction: N
    command:
      - ./verifiers/existing.sh
`)
	configPath := filepath.Join(dir, ".sidekick", "sidekick.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir seed dir: %v", err)
	}
	if err := os.WriteFile(configPath, preExisting, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// First attempt with duplicate name should re-prompt; second answer succeeds.
	stdin := strings.Join([]string{
		"Existing", // duplicate → re-prompt
		"Fresh",    // good
		"E",        // Direction
		"2",        // command
		"./ok.sh",  // command
		"",         // timeout
		"",         // permissions? -> N
		"y",        // confirm
		"",
	}, "\n")

	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs([]string{"add", "--local"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "already exists") {
		t.Errorf("expected duplicate-name error in output, got:\n%s", out.String())
	}

	raw, _ := os.ReadFile(configPath)
	var f config.File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(f.Verifiers) != 2 {
		t.Fatalf("want 2 verifiers (Existing + Fresh), got %d", len(f.Verifiers))
	}
	if f.Verifiers[1].Name != "Fresh" {
		t.Errorf("appended verifier name = %q, want Fresh", f.Verifiers[1].Name)
	}
}

func TestLocalAddWizardAbortOnConfirmNo(t *testing.T) {
	stdin := strings.Join([]string{
		"Whatever",
		"NE",
		"2",
		"./x.sh",
		"",
		"",
		"n",
		"",
	}, "\n")

	dir := t.TempDir()
	isolateGlobalConfig(t, dir)
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs([]string{"add", "--local"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted error, got err=%v output=%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".sidekick", "sidekick.yaml")); !os.IsNotExist(err) {
		t.Errorf("sidekick.yaml should not have been written on abort: err=%v", err)
	}
}

func TestLocalAddRejectsMissingURLWithoutLocal(t *testing.T) {
	dir := t.TempDir()
	isolateGlobalConfig(t, dir)
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"add"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--local") {
		t.Fatalf("expected guidance toward --local, got err=%v output=%s", err, out.String())
	}
}

func TestLocalAddRejectsURLWithLocalFlag(t *testing.T) {
	dir := t.TempDir()
	isolateGlobalConfig(t, dir)
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	root := newVerifierCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"add", "--local", "https://example.com/x.sh"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no <url>") {
		t.Fatalf("expected rejection, got err=%v output=%s", err, out.String())
	}
}
