package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/verifier"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "hud.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// touchLocalArtifact creates an empty file under dir so that Resolve's
// local-script existence check passes. Tests that exercise resolution
// shape (path joining, type coercion) without intending to test the
// "missing-script" error path should call this with the same paths
// that appear in the YAML.
func touchLocalArtifact(t *testing.T, dir, rel string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveValid(t *testing.T) {
	p := writeTemp(t, `
goal_source: prompt
verifiers:
  - name: Architect
    direction: n
    command: ["./bin/architect"]
    timeout: 30s
  - name: Test
    direction: E
    command: ["echo", "hi"]
`)
	touchLocalArtifact(t, filepath.Dir(p), "bin/architect")
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := f.Resolve(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2, got %d", len(vs))
	}
	// direction should be uppercased
	if vs[0].Direction != "N" {
		t.Errorf("direction not uppercased: %q", vs[0].Direction)
	}
	// relative ./bin/architect should be joined to configDir
	if !filepath.IsAbs(vs[0].Command[0]) {
		t.Errorf("relative command not resolved: %q", vs[0].Command[0])
	}
	if vs[0].Timeout.Seconds() != 30 {
		t.Errorf("timeout not parsed: %v", vs[0].Timeout)
	}
	if vs[0].Type != verifier.TypeCommand {
		t.Errorf("default type = %q, want command", vs[0].Type)
	}
}

func TestResolveTypedVerifiers(t *testing.T) {
	p := writeTemp(t, `
verifiers:
  - name: Explicit Command
    type: command
    disabled: true
    direction: N
    command: ["./bin/check"]
  - name: Architect
    type: llm
    direction: E
    timeout: 90s
    llm:
      agent: codex
      model: gpt-5.5
      thinking: high
      skill: ./skills/architect/SKILL.md
  - name: Unit Tests
    type: binary
    direction: S
    binary:
      command: ["./scripts/test.sh"]
      pass_reason: tests pass
      fail_reason: tests failed
`)
	dir := filepath.Dir(p)
	touchLocalArtifact(t, dir, "bin/check")
	touchLocalArtifact(t, dir, "skills/architect/SKILL.md")
	touchLocalArtifact(t, dir, "scripts/test.sh")
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := f.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("want 3, got %d", len(vs))
	}
	if vs[0].Type != verifier.TypeCommand || !vs[0].Disabled || !filepath.IsAbs(vs[0].Command[0]) {
		t.Fatalf("command verifier not resolved: %+v", vs[0])
	}
	if vs[1].Type != verifier.TypeAgent ||
		vs[1].Agent.Agent != "codex" ||
		vs[1].Agent.Model != "gpt-5.5" ||
		vs[1].Agent.Thinking != "high" ||
		!filepath.IsAbs(vs[1].Agent.Skill) {
		t.Fatalf("agent verifier not resolved: %+v", vs[1])
	}
	if vs[2].Type != verifier.TypeBinary ||
		!filepath.IsAbs(vs[2].Binary.Command[0]) ||
		vs[2].Binary.PassReason != "tests pass" ||
		vs[2].Binary.FailReason != "tests failed" {
		t.Fatalf("binary verifier not resolved: %+v", vs[2])
	}
}

func TestSetVerifierDisabled(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - name: A
    direction: N
    command: ["x"]
  - name: B
    disabled: true
    direction: S
    command: ["y"]
`)
	if err := SetVerifierDisabled(p, "A", true); err != nil {
		t.Fatal(err)
	}
	if err := SetVerifierDisabled(p, "B", false); err != nil {
		t.Fatal(err)
	}
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Verifiers[0].Disabled {
		t.Fatalf("A disabled flag not saved: %+v", f.Verifiers[0])
	}
	if f.Verifiers[1].Disabled {
		t.Fatalf("B disabled flag not cleared: %+v", f.Verifiers[1])
	}
}

func TestResolveTypedVerifierValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bad_type", `verifiers: [{name: A, type: nope, direction: N, command: ["x"]}]`},
		{"missing_agent_skill", `verifiers: [{name: A, type: agent, direction: N, llm: {agent: claude}}]`},
		{"missing_binary_command", `verifiers: [{name: A, type: binary, direction: N, binary: {}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, tc.body)
			f, _, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Resolve(filepath.Dir(p)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestResolveDuplicateName(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - {name: A, direction: N, command: ["x"]}
  - {name: A, direction: S, command: ["y"]}`)
	f, _, _ := Load(p)
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestResolveBadDirection(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - {name: A, direction: NORTH, command: ["x"]}`)
	f, _, _ := Load(p)
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected bad direction error")
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Load(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("expected missing file error")
	}
}

// TestRejectRemoteWithoutSHA enforces the trust model: any source: URL
// without a sha256 pin must be rejected at config load. Drift caught
// late is drift caught wrong.
func TestRejectRemoteWithoutSHA(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - name: Bad
    type: command
    direction: N
    source:
      url: https://example.com/mine.sh
`)
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected sha256-required error")
	}
}

// TestUntrustedRemoteRejected ensures a remote verifier whose sha256 is
// pinned but absent from trust.json blocks daemon startup with a
// human-readable instruction. We isolate trust via $HUD_TRUST_FILE so
// the test does not depend on the user's real ~/.hud directory.
func TestUntrustedRemoteRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HUD_TRUST_FILE", filepath.Join(dir, "trust.json"))

	p := writeTemp(t, `verifiers:
  - name: Remote
    type: command
    direction: NE
    source:
      url: https://example.com/x.sh
      sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  - name: Local
    type: command
    direction: N
    command: ["echo", "hi"]
`)
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Resolve(filepath.Dir(p))
	if err == nil {
		t.Fatal("expected untrusted-remote error")
	}
	if !strings.Contains(err.Error(), "Remote") || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("error should name verifier and trust command; got: %v", err)
	}
}

// TestPermissionsValidated rejects an unknown filesystem mode at config
// load. Typos that pass through become "no opinion declared" silently,
// which defeats the whole point of advisory permissions.
func TestPermissionsValidated(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - name: A
    type: command
    direction: N
    command: ["echo"]
    permissions:
      filesystem: nope
`)
	f, _, _ := Load(p)
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected permissions validation error")
	}
}

// TestSkillFileExistenceChecked verifies that an agent verifier whose
// skill path doesn't exist fails at Resolve, not 30 seconds in when
// the first edit lands.
func TestSkillFileExistenceChecked(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - name: Architect
    type: agent
    direction: N
    llm:
      agent: claude
      skill: ./skills/missing/SKILL.md
`)
	f, _, _ := Load(p)
	_, err := f.Resolve(filepath.Dir(p))
	if err == nil {
		t.Fatal("expected missing-skill error")
	}
	if !strings.Contains(err.Error(), "skill") {
		t.Fatalf("error should mention skill: %v", err)
	}
}

// TestCustomAgentRequiresCommand asserts the new agent: custom path
// validates llm.custom.command at config load.
func TestCustomAgentRequiresCommand(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - name: Custom
    type: agent
    direction: N
    llm:
      agent: custom
      skill: ./skills/x.md
`)
	dir := filepath.Dir(p)
	touchLocalArtifact(t, dir, "skills/x.md")
	f, _, _ := Load(p)
	if _, err := f.Resolve(dir); err == nil {
		t.Fatal("expected custom-command-required error")
	}
}

func TestResolveQuietPeriod(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string // duration; "" means 0
		wantErr bool
	}{
		{"unset", `verifiers: [{name: A, direction: N, command: ["x"]}]`, "", false},
		{"explicit", "quiet_period: 30s\nverifiers: [{name: A, direction: N, command: [\"x\"]}]", "30s", false},
		{"bad_format", "quiet_period: notaduration\nverifiers: [{name: A, direction: N, command: [\"x\"]}]", "", true},
		{"negative", "quiet_period: -5s\nverifiers: [{name: A, direction: N, command: [\"x\"]}]", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, tc.body)
			f, _, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got, err := f.ResolveQuietPeriod()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got duration %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == "" {
				if got != 0 {
					t.Fatalf("expected zero duration, got %s", got)
				}
				return
			}
			want, _ := time.ParseDuration(tc.want)
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}
