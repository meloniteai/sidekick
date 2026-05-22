package verifier

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
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
		Command: []string{"sh", "-c", `cat >/dev/null; printf '{"distance": 0.0, "reason": "%s/%s"}\n' "$SESSION_BASE_REF" "$SIDEKICK_VERIFIER"`},
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
		// The output contract is the findings array, owned by code (not the skill).
		`"findings"`,
		`"distance": <number 0.0..1.0>`,
		`emit a single finding with "path": null`,
		// A top-level reason must always be requested so a clean pass still
		// surfaces feedback on the compass.
		`Always set the top-level "reason"`,
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
// allowed_tools declared in sidekick.yaml are appended to the hardcoded
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
			obj := findLastResultObject(strings.ReplaceAll(tc.in, `\n`, "\n"))
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

func TestFinalizeResultRollup(t *testing.T) {
	// Findings present: scalar is the max finding's distance + reason.
	r := finalizeResult(Result{Findings: []Finding{
		{Path: "a.go", Distance: 0.25, Reason: "minor"},
		{Path: "b.go", Distance: 0.75, Reason: "blocking"},
	}})
	if r.Distance != 0.75 || r.Reason != "blocking" {
		t.Fatalf("rollup got distance=%v reason=%q, want 0.75/blocking", r.Distance, r.Reason)
	}
	if len(r.Findings) != 2 {
		t.Fatalf("findings preserved = %d, want 2", len(r.Findings))
	}

	// Per-finding distance is clamped before roll-up.
	r = finalizeResult(Result{Findings: []Finding{{Path: "a.go", Distance: 9, Reason: "x"}}})
	if r.Distance != 1 || r.Findings[0].Distance != 1 {
		t.Fatalf("clamp got scalar=%v finding=%v, want 1/1", r.Distance, r.Findings[0].Distance)
	}

	// Legacy scalar with friction is wrapped as one tree-global finding.
	r = finalizeResult(Result{Distance: 0.5, Reason: "legacy"})
	if len(r.Findings) != 1 || r.Findings[0].Path != "" || r.Findings[0].Distance != 0.5 {
		t.Fatalf("legacy wrap got %+v, want one global finding at 0.5", r.Findings)
	}

	// A passing scalar (distance 0) yields no findings.
	r = finalizeResult(Result{Distance: 0, Reason: "passed"})
	if len(r.Findings) != 0 || r.Distance != 0 {
		t.Fatalf("pass got %d findings / distance %v, want 0/0", len(r.Findings), r.Distance)
	}

	// An empty findings set with a top-level reason keeps that reason — a clean
	// pass still surfaces feedback on the compass.
	r = finalizeResult(Result{Reason: "comments are concise"})
	if r.Distance != 0 || len(r.Findings) != 0 || r.Reason != "comments are concise" {
		t.Fatalf("pass-with-reason got %+v", r)
	}

	// A clean pass with no reason at all gets a non-blank default so the compass
	// never shows an empty cell.
	r = finalizeResult(Result{})
	if r.Reason == "" {
		t.Fatalf("empty result should receive a default reason, got blank")
	}
}

func TestParseCommandResultFindings(t *testing.T) {
	// Structured findings drive the scalar via roll-up.
	r, err := parseCommandResult("cmd", `{"findings":[{"path":"x.go","distance":0.5,"reason":"bad"},{"path":"y.go","distance":0.25,"reason":"meh"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0.5 || r.Reason != "bad" || len(r.Findings) != 2 {
		t.Fatalf("findings parse got distance=%v reason=%q n=%d", r.Distance, r.Reason, len(r.Findings))
	}
	if r.Findings[0].Path != "x.go" {
		t.Fatalf("path not preserved: %+v", r.Findings[0])
	}

	// Robust path: findings object followed by trailing log lines still parses.
	r, err = parseCommandResult("cmd", "[info] starting\n{\"findings\":[{\"path\":\"z.go\",\"distance\":0.75,\"reason\":\"blk\"}]}\n[info] done")
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0.75 || len(r.Findings) != 1 {
		t.Fatalf("trailing-log findings parse got distance=%v n=%d", r.Distance, len(r.Findings))
	}

	// Legacy distance object still parses and wraps as one tree-global finding.
	r, err = parseCommandResult("cmd", `{"distance":0.3,"reason":"legacy"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0.3 || len(r.Findings) != 1 || r.Findings[0].Path != "" {
		t.Fatalf("legacy command parse got %+v", r)
	}

	// Empty findings array means pass: distance 0, no findings.
	r, err = parseCommandResult("cmd", `{"findings":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0 || len(r.Findings) != 0 || r.Status != ipc.StatusOK {
		t.Fatalf("empty findings got distance=%v n=%d status=%q", r.Distance, len(r.Findings), r.Status)
	}
}

func TestParseAgentResultFindings(t *testing.T) {
	// Codex-style: prose then a final findings line.
	r, err := parseAgentResult("X", "codex", "thinking\n{\"findings\":[{\"path\":\"p.go\",\"distance\":0.75,\"reason\":\"r\"}]}")
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0.75 || len(r.Findings) != 1 || r.Findings[0].Path != "p.go" {
		t.Fatalf("agent findings parse got %+v", r)
	}

	// A tree-global finding uses a null path, which normalizes to "".
	r, err = parseAgentResult("X", "codex", `{"findings":[{"path":null,"distance":1,"reason":"suite fails"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 1 || len(r.Findings) != 1 || r.Findings[0].Path != "" {
		t.Fatalf("global finding parse got %+v", r)
	}
}

func TestNormalizeFindingPath(t *testing.T) {
	wt := "/repo/wt"
	cases := []struct{ in, want string }{
		{"internal/a.go", "internal/a.go"},
		{"./internal/a.go", "internal/a.go"},
		{"/repo/wt/internal/a.go", "internal/a.go"}, // absolute under worktree -> relative
		{"/etc/passwd", ""},                         // escapes the worktree -> tree-global
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeFindingPath(wt, tc.in); got != tc.want {
			t.Errorf("normalizeFindingPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPrepareFindingsCap(t *testing.T) {
	findings := []Finding{
		{Path: "a.go", Distance: 0.1, Reason: "a"},
		{Path: "b.go", Distance: 0.9, Reason: "b"},
		{Path: "c.go", Distance: 0.5, Reason: "c"},
		{Path: "d.go", Distance: 0.3, Reason: "d"},
	}
	out := prepareFindings(findings, "", 2)
	if len(out) != 3 { // 2 kept + 1 "+K more" marker
		t.Fatalf("capped length = %d, want 3", len(out))
	}
	// Highest-distance findings are kept so the max roll-up is unaffected.
	if out[0].Distance != 0.9 || out[1].Distance != 0.5 {
		t.Fatalf("cap kept wrong findings: %+v", out[:2])
	}
	marker := out[2]
	if marker.Path != "" || !strings.Contains(marker.Reason, "+2 more") {
		t.Fatalf("overflow marker = %+v, want global '+2 more'", marker)
	}

	// A non-positive cap disables capping.
	if got := prepareFindings(findings, "", 0); len(got) != 4 {
		t.Fatalf("uncapped length = %d, want 4", len(got))
	}
}

func TestParseSARIF(t *testing.T) {
	const doc = `{
	  "version": "2.1.0",
	  "runs": [
	    {
	      "tool": {"driver": {"name": "golangci-lint"}},
	      "results": [
	        {"ruleId": "errcheck", "level": "error",
	         "message": {"text": "error return value not checked"},
	         "locations": [{"physicalLocation": {
	           "artifactLocation": {"uri": "internal/foo/bar.go"},
	           "region": {"startLine": 42}}}]},
	        {"ruleId": "gosimple", "level": "warning",
	         "message": {"text": "should use a simple channel send"},
	         "locations": [{"physicalLocation": {
	           "artifactLocation": {"uri": "internal/baz.go"},
	           "region": {"startLine": 7}}}]}
	      ]
	    }
	  ]
	}`
	findings := parseSARIF(doc)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Path != "internal/foo/bar.go" || findings[0].Line != 42 || findings[0].Distance != 1.0 {
		t.Fatalf("error finding wrong: %+v", findings[0])
	}
	if findings[1].Path != "internal/baz.go" || findings[1].Distance != 0.5 {
		t.Fatalf("warning finding wrong: %+v", findings[1])
	}
	if got := parseSARIF("not json"); got != nil {
		t.Fatalf("unparseable SARIF should yield nil, got %+v", got)
	}
}

func TestExtractRegexFindings(t *testing.T) {
	pattern := `^(?P<file>[^:]+):(?P<line>\d+):\s*(?P<reason>.*)$`
	text := "internal/a.go:12: unused variable\ninternal/b.go:7: shadowed err\nnot a match line"
	findings := extractRegexFindings(pattern, text)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if findings[0].Path != "internal/a.go" || findings[0].Line != 12 || findings[0].Reason != "unused variable" {
		t.Fatalf("finding[0] wrong: %+v", findings[0])
	}
	if findings[0].Distance != 1.0 {
		t.Fatalf("regex finding distance = %v, want 1.0", findings[0].Distance)
	}
	if got := extractRegexFindings(`(?P<bad`, text); got != nil {
		t.Fatalf("bad regex should yield nil, got %+v", got)
	}
}

func TestVerifyBinarySARIF(t *testing.T) {
	sarif := `{"runs":[{"results":[` +
		`{"level":"warning","message":{"text":"w"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3}}}]},` +
		`{"level":"error","message":{"text":"e"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"b.go"},"region":{"startLine":9}}}]}` +
		`]}]}`
	// Exit non-zero to prove findings, not exit code, drive the score.
	v := Verifier{Name: "lint", Type: TypeBinary, Binary: BinaryConfig{
		Command: []string{"sh", "-c", "printf '%s' '" + sarif + "'; exit 1"},
		Format:  "sarif",
	}}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(r.Findings))
	}
	if r.Distance != 1.0 {
		t.Fatalf("rolled-up distance = %v, want 1.0 (max = error)", r.Distance)
	}

	// Empty results + exit 0 = pass.
	pass := Verifier{Name: "lint", Type: TypeBinary, Binary: BinaryConfig{
		Command:    []string{"sh", "-c", `printf '%s' '{"runs":[{"results":[]}]}'; exit 0`},
		Format:     "sarif",
		PassReason: "clean",
	}}
	r, err = pass.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Distance != 0 || len(r.Findings) != 0 || r.Reason != "clean" {
		t.Fatalf("sarif pass got %+v", r)
	}
}

func TestVerifyBinarySARIFOutputFile(t *testing.T) {
	dir := t.TempDir()
	sarif := `{"runs":[{"results":[{"level":"note","message":{"text":"n"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"x.go"},"region":{"startLine":1}}}]}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "report.sarif"), []byte(sarif), 0o600); err != nil {
		t.Fatal(err)
	}
	v := Verifier{Name: "lint", Type: TypeBinary, Binary: BinaryConfig{
		Command:    []string{"sh", "-c", "exit 1"},
		Format:     "sarif",
		OutputFile: "report.sarif", // relative to the session worktree
	}}
	r, err := v.Verify(context.Background(), Session{SessionWorktree: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Path != "x.go" || r.Distance != 0.25 {
		t.Fatalf("output-file SARIF got %+v", r)
	}
}

func TestVerifyBinaryFailRegex(t *testing.T) {
	v := Verifier{Name: "lint", Type: TypeBinary, Binary: BinaryConfig{
		Command:   []string{"sh", "-c", `printf 'internal/a.go:12: unused variable\ninternal/b.go:7: shadowed err\nnoise\n'; exit 1`},
		FailRegex: `^(?P<file>[^:]+):(?P<line>\d+):\s*(?P<reason>.*)$`,
	}}
	r, err := v.Verify(context.Background(), Session{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 {
		t.Fatalf("regex findings = %d, want 2", len(r.Findings))
	}
	if r.Distance != 1.0 || r.Findings[0].Path != "internal/a.go" {
		t.Fatalf("regex verify got %+v", r)
	}
}
