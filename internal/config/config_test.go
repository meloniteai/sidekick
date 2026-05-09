package config

import (
	"os"
	"path/filepath"
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
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := f.Resolve(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("want 3, got %d", len(vs))
	}
	if vs[0].Type != verifier.TypeCommand || !filepath.IsAbs(vs[0].Command[0]) {
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
