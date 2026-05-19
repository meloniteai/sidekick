//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

// TestHookFiresCommandVerifier walks the full hook -> daemon -> runner ->
// verifier-output -> status pipe with a deterministic command verifier
// that prints a fixed JSON Result. Cheap (no agent CLI, no LLM), but
// exercises the same code path agent verifiers use.
func TestHookFiresCommandVerifier(t *testing.T) {
	repo := ScratchRepo(t)
	WriteSidekickYAML(t, repo, `goal_source: prompt
quiet_period: 50ms
verifiers:
  - name: Echo
    type: command
    direction: N
    timeout: 5s
    command: ["sh", "-c", "printf '{\"distance\":0.1,\"reason\":\"ok\"}\n'"]
`)

	d := StartDaemon(t, repo)

	d.Hook(t, "foo.go")

	v := d.WaitForVerifier(t, "Echo", func(v ipc.VerifierStatus) bool {
		return !v.Running && !v.ComputedAt.IsZero() && v.Reason == "ok"
	}, 5*time.Second)

	if v.Distance != 0.1 {
		t.Fatalf("distance = %v, want 0.1", v.Distance)
	}
	if v.Status != ipc.StatusOK {
		t.Fatalf("status = %q, want %q", v.Status, ipc.StatusOK)
	}
}
