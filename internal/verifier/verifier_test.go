package verifier

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/ipc"
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

func TestVerifyCommandReceivesVerifierEnv(t *testing.T) {
	v := Verifier{
		Name:    "Env",
		Command: []string{"sh", "-c", `cat >/dev/null; printf '{"distance": 0.0, "reason": "%s/%s"}\n' "$SESSION_BASE_REF" "$HUD_VERIFIER"`},
	}
	r, err := v.Verify(context.Background(), Session{SessionBaseRef: "abc123"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Reason != "abc123/1" {
		t.Fatalf("command verifier env not passed, got %+v", r)
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
		Goal:            "ship it",
		SessionBaseRef:  "abc123",
		SessionWorktree: "/repo/worktree-x",
		ChangedFiles:    []string{"a.go", "b.go"},
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
		"Session worktree ($SESSION_WORKTREE): /repo/worktree-x",
		"Recently changed files",
		// Protocol preamble lifted from skill bodies — these must come
		// from the runtime, not the skill, so community skills can stay
		// lens-only.
		"## How to evaluate",
		"git -C $SESSION_WORKTREE diff $SESSION_BASE_REF --stat",
		"git -C $SESSION_WORKTREE diff $SESSION_BASE_REF",
		"git -C $SESSION_WORKTREE status --porcelain",
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
	// The runtime prompt mandates "git -C $SESSION_WORKTREE …" form, so the
	// allowlist must accept it explicitly — claude's prefix matcher won't
	// unify "git -C path diff" with "git diff:*".
	var allowed string
	for i, a := range claude {
		if a == "--allowedTools" && i+1 < len(claude) {
			allowed = claude[i+1]
			break
		}
	}
	if !strings.Contains(allowed, "Bash(git -C:*)") {
		t.Fatalf("allowlist missing Bash(git -C:*) entry: %s", allowed)
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

// TestAgentCommandClaudeAllowedToolsUnion asserts the per-verifier
// allowed_tools declared in hud.yaml are appended to the hardcoded
// baseline rather than replacing it, and that duplicates are deduped so
// the command line stays clean.
func TestAgentCommandClaudeAllowedToolsUnion(t *testing.T) {
	args, err := agentCommand(AgentConfig{
		Agent:        "claude",
		AllowedTools: []string{"Bash(go test:*)", "Bash(go build:*)", "Read"}, // "Read" is already in baseline
	})
	if err != nil {
		t.Fatal(err)
	}
	var allowed string
	for i, a := range args {
		if a == "--allowedTools" && i+1 < len(args) {
			allowed = args[i+1]
			break
		}
	}
	if allowed == "" {
		t.Fatalf("no --allowedTools value in args: %#v", args)
	}
	// Baseline entries must survive.
	for _, want := range []string{"Bash(git -C:*)", "Read", "Grep", "Glob"} {
		if !strings.Contains(allowed, want) {
			t.Fatalf("baseline entry %q missing: %s", want, allowed)
		}
	}
	// Per-verifier extras must appear.
	for _, want := range []string{"Bash(go test:*)", "Bash(go build:*)"} {
		if !strings.Contains(allowed, want) {
			t.Fatalf("extra entry %q missing: %s", want, allowed)
		}
	}
	// "Read" must appear exactly once (dedup).
	if got := strings.Count(allowed, "Read"); got != 1 {
		t.Fatalf("expected Read to appear exactly once, got %d in: %s", got, allowed)
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
	if r.Status != ipc.StatusOK {
		t.Fatalf("expected status=ok, got %q", r.Status)
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

// TestParseAgentResultUnparseable verifies that when the agent's output
// contains no extractable distance object, we return Status=unknown
// instead of fabricating distance=0.5. Preserving the prior distance is
// the runner's job; the parser just signals the unparseable state.
func TestParseAgentResultUnparseable(t *testing.T) {
	output := "I cannot help with that.\n"
	r, err := parseAgentResult("Architect", "claude", output)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != ipc.StatusUnknown {
		t.Fatalf("expected status=unknown, got %q (full %+v)", r.Status, r)
	}
}

// TestParserBraceAware exercises the regression class that drove the
// regex rewrite: distance objects whose reason field contains braces or
// quoted braces. Old regex `\{[^{}]*"distance"[^{}]*\}` would skip these
// and silently bucket the run as "could not parse" → 0.5. The new
// brace+string-aware scanner must accept them.
func TestParserBraceAware(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want float64
	}{
		{
			name: "nested-object-in-reason-via-string",
			in:   `prelude\n{"distance": 0.3, "reason": "failed at {x:1}"}`,
			want: 0.3,
		},
		{
			name: "trailing-log-after-json",
			in:   `{"distance": 0.4, "reason": "ok"}\n[debug] cleanup`,
			want: 0.4,
		},
		{
			name: "multiple-objects-take-last",
			in:   `{"distance": 0.9, "reason": "stale"}\n{"distance": 0.1, "reason": "fresh"}`,
			want: 0.1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := findLastDistanceObject(strings.ReplaceAll(tc.in, `\n`, "\n"))
			if obj == "" {
				t.Fatalf("scanner returned empty for %q", tc.in)
			}
			r, err := parseAgentResult("X", "codex", strings.ReplaceAll(tc.in, `\n`, "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if r.Distance != tc.want {
				t.Fatalf("distance got %v want %v (extracted %q)", r.Distance, tc.want, obj)
			}
		})
	}
}

// TestVerifyAgentUsageHarvest checks that token+cost telemetry from the
// Claude --output-format=json envelope is propagated into Result.Usage.
func TestVerifyAgentUsageHarvest(t *testing.T) {
	stdout := `{"result":"{\"distance\":0.2,\"reason\":\"ok\"}","total_cost_usd":0.0042,"duration_ms":1234,"model":"claude-haiku-4-5","usage":{"input_tokens":150,"output_tokens":42,"cache_read_input_tokens":1000}}`
	r, err := parseAgentResult("Architect", "claude", stdout)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage == nil {
		t.Fatal("expected usage populated, got nil")
	}
	if r.Usage.CostUSD != 0.0042 || r.Usage.InputTokens != 150 || r.Usage.OutputTokens != 42 || r.Usage.CacheReads != 1000 {
		t.Fatalf("usage mismatched: %+v", *r.Usage)
	}
	if r.Usage.Model != "claude-haiku-4-5" {
		t.Fatalf("model got %q", r.Usage.Model)
	}
}

// TestRenderCustomCommandTemplating exercises agent: custom argv
// rendering with template substitution and verifies that args without
// templates pass through unmodified.
func TestRenderCustomCommandTemplating(t *testing.T) {
	c := AgentConfig{
		Agent:    "custom",
		Model:    "gemini-1.5-flash",
		Thinking: "low",
		Skill:    "/tmp/skill.md",
		Custom: CustomAgent{
			Command: []string{"my-llm", "--model={{.Model}}", "--effort={{.Thinking}}", "-"},
		},
	}
	got, err := agentCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"my-llm", "--model=gemini-1.5-flash", "--effort=low", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
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
