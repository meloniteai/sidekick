package verifier

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifyEchoScript(t *testing.T) {
	v := Verifier{
		Name:      "Echo",
		Direction: "N",
		Command:   []string{"sh", "-c", `echo '{"distance": 0.42, "reason": "ok"}'`},
		Timeout:   5 * time.Second,
	}
	r, err := v.Verify(context.Background(), Session{Goal: "x"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Distance != 0.42 || r.Reason != "ok" {
		t.Fatalf("got %+v", r)
	}
}

func TestVerifyClampsDistance(t *testing.T) {
	v := Verifier{Name: "X", Command: []string{"sh", "-c", `echo '{"distance": 9.0, "reason": "high"}'`}}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 1.0 {
		t.Fatalf("expected clamp to 1.0, got %v", r.Distance)
	}
}

func TestVerifyTakesLastLine(t *testing.T) {
	v := Verifier{
		Name:    "Y",
		Command: []string{"sh", "-c", `echo "logging line"; echo '{"distance": 0.1, "reason": "tail"}'`},
	}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Reason != "tail" {
		t.Fatalf("expected reason=tail, got %q", r.Reason)
	}
}

func TestVerifyBadJSON(t *testing.T) {
	v := Verifier{Name: "Z", Command: []string{"sh", "-c", "echo not-json"}}
	_, err := v.Verify(context.Background(), Session{})
	if err == nil || !strings.Contains(err.Error(), "bad json") {
		t.Fatalf("expected bad json error, got %v", err)
	}
}

func TestVerifyTimeout(t *testing.T) {
	v := Verifier{Name: "Slow", Command: []string{"sh", "-c", "sleep 5"}, Timeout: 100 * time.Millisecond}
	_, err := v.Verify(context.Background(), Session{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestVerifyBinaryPassAndFail(t *testing.T) {
	pass := Verifier{
		Name: "Tests",
		Type: TypeBinary,
		Binary: BinaryConfig{
			Command:    []string{"sh", "-c", "cat >/dev/null; exit 0"},
			PassReason: "unit tests pass",
		},
	}
	r, err := pass.Verify(context.Background(), Session{SessionBaseRef: "abc123"})
	if err != nil {
		t.Fatalf("pass Verify: %v", err)
	}
	if r.Distance != 0 || r.Reason != "unit tests pass" {
		t.Fatalf("pass got %+v", r)
	}

	fail := Verifier{
		Name: "Tests",
		Type: TypeBinary,
		Binary: BinaryConfig{
			Command: []string{"sh", "-c", "echo nope >&2; exit 2"},
		},
	}
	r, err = fail.Verify(context.Background(), Session{SessionBaseRef: "abc123"})
	if err != nil {
		t.Fatalf("fail Verify: %v", err)
	}
	if r.Distance != 1 || r.Reason != "nope" {
		t.Fatalf("fail got %+v", r)
	}
}

func TestBuildAgentPromptStripsSkillFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skill := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skill, []byte(`---
name: architect
---
# architect

Rubric body.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	v := Verifier{Name: "Architect", Type: TypeAgent, Agent: AgentConfig{Skill: skill}}
	prompt, err := BuildAgentPrompt(v, Session{
		Goal:           "ship it",
		SessionBaseRef: "abc123",
		ChangedFiles:   []string{"a.go", "b.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# architect",
		"Rubric body.",
		"Verifier name: Architect",
		"Active goal: ship it",
		"Session base ref ($SESSION_BASE_REF): abc123",
		"Recently changed files",
		`{"distance": <number 0.0..1.0>, "reason": "<one short sentence>"}`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "name: architect") {
		t.Fatalf("prompt should strip frontmatter:\n%s", prompt)
	}
}

func TestAgentCommandClaudeAndCodex(t *testing.T) {
	claude, err := agentCommand(AgentConfig{Agent: "claude", Model: "haiku", Thinking: "low"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"claude", "-p", "--output-format", "json", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--model", "haiku", "--effort", "low"} {
		if !contains(claude, want) {
			t.Fatalf("claude args missing %q: %#v", want, claude)
		}
	}

	codex, err := agentCommand(AgentConfig{Agent: "codex", Model: "gpt-5.5", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := []string{"codex", "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--model", "gpt-5.5", "-c", `model_reasoning_effort="high"`, "--sandbox", "read-only", "-"}
	if !reflect.DeepEqual(codex, wantCodex) {
		t.Fatalf("codex args:\n got %#v\nwant %#v", codex, wantCodex)
	}
}

func TestParseAgentResult(t *testing.T) {
	claude := `{"result":"thinking...\n{\"distance\":0.2,\"reason\":\"ok\"}"}`
	r, err := parseAgentResult("Architect", "claude", claude)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0.2 || r.Reason != "ok" {
		t.Fatalf("claude parse got %+v", r)
	}
	codex := "done\n{\"distance\":9,\"reason\":\"clamped\"}\n"
	r, err = parseAgentResult("Architect", "codex", codex)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 1 || r.Reason != "clamped" {
		t.Fatalf("codex parse got %+v", r)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
