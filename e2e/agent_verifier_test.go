//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

// TestAgentVerifierRealCLIs exercises agent-type verifiers against the
// real claude and codex CLIs (haiku-tier / cheapest models). The test is
// gated by SIDEKICK_E2E_REAL_AGENT=1 — it spends API credit and depends
// on external services, so we don't run it on every PR. CI wires it as a
// separate `e2e-agents` job that only runs when the API-key secret is set.
//
// The fixture skill (testdata/minimal-skill.md) instructs the agent to
// emit a fixed JSON line. We assert that the agent ran to completion and
// produced a parseable result — we deliberately don't pin reason text,
// because real models drift even under "say exactly X" prompts.
func TestAgentVerifierRealCLIs(t *testing.T) {
	if os.Getenv("SIDEKICK_E2E_REAL_AGENT") != "1" {
		t.Skip("set SIDEKICK_E2E_REAL_AGENT=1 to run; requires claude/codex CLIs and API keys")
	}

	// Use a stable absolute path so the verifier can find the skill no
	// matter which cwd the daemon ends up resolving.
	_, file, _, _ := runtime.Caller(0)
	skillPath := filepath.Join(filepath.Dir(file), "testdata", "minimal-skill.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("skill fixture: %v", err)
	}

	cases := []struct {
		name   string
		agent  string
		model  string
		envKey string
		cli    string
	}{
		{name: "claude", agent: "claude", model: "haiku", envKey: "ANTHROPIC_API_KEY", cli: "claude"},
		{name: "codex", agent: "codex", model: "gpt-5-mini", envKey: "OPENAI_API_KEY", cli: "codex"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.cli); err != nil {
				t.Skipf("%s CLI not on PATH: %v", tc.cli, err)
			}
			if os.Getenv(tc.envKey) == "" {
				t.Skipf("%s not set; skipping", tc.envKey)
			}

			repo := ScratchRepo(t)
			yaml := fmt.Sprintf(`goal_source: prompt
quiet_period: 50ms
verifiers:
  - name: MinSkill
    type: agent
    direction: N
    timeout: 120s
    llm:
      agent: %s
      model: %s
      skill: %s
`, tc.agent, tc.model, skillPath)
			WriteSidekickYAML(t, repo, yaml)

			d := StartDaemon(t, repo)
			d.Hook(t, "foo.go")

			v := d.WaitForVerifier(t, "MinSkill", func(v ipc.VerifierStatus) bool {
				return !v.Running && !v.ComputedAt.IsZero()
			}, 120*time.Second)

			// Tolerant assert: the agent ran and produced *some* parseable
			// result. We do not pin distance/reason — model variance makes
			// exact assertions flaky even with a "say exactly X" skill.
			if v.Status == ipc.StatusError {
				t.Fatalf("verifier error: %s", v.Reason)
			}
			if v.Reason == "" {
				t.Fatalf("empty reason; status=%q", v.Status)
			}
		})
	}
}
