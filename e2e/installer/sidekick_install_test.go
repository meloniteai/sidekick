//go:build e2e

package installer

import (
	"strings"
	"testing"
)

// TestSidekickInstallWiresAgents runs install.sh end-to-end with the
// branch's install.sh (binary lands), then `sidekick install --yes
// --claude --codex` to force-wire both agents (the container has no
// CLIs/config dirs, so detection alone would no-op). Asserts the
// resulting on-disk state matches what sidekick promises to write:
//
//   - ~/.claude/settings.json contains a PostToolUse matcher whose command
//     is `sidekick hook write`.
//   - ~/.codex/hooks.json contains the same.
//   - SKILL.md exists under ~/.claude/skills/sidekick/ and the cross-agent
//     ~/.agents/skills/sidekick/ location.
//
// Settings file paths come from internal/install/agents.go (HookFilePath,
// SkillDirs) — kept in sync with the source rather than hard-coded here.
func TestSidekickInstallWiresAgents(t *testing.T) {
	// We use `jq` for JSON assertions inside the container — the docker
	// image installs it explicitly so we have a portable parser.
	post := `sidekick install --yes --claude --codex

echo "---- claude settings ----"
cat "$HOME/.claude/settings.json"
echo
echo "---- codex hooks ----"
cat "$HOME/.codex/hooks.json"
echo
echo "---- skill listings ----"
ls "$HOME/.claude/skills/sidekick/SKILL.md"
ls "$HOME/.codex/skills/sidekick/SKILL.md"
ls "$HOME/.agents/skills/sidekick/SKILL.md"
echo
echo "---- jq assertions ----"
jq -e '.hooks.PostToolUse[0].hooks[0].command | test("sidekick hook write")' "$HOME/.claude/settings.json"
jq -e '.hooks.PostToolUse[0].hooks[0].command | test("sidekick hook write")' "$HOME/.codex/hooks.json"
echo "ALL_GOOD"`

	out, err := RunInstallScript(t, "SIDEKICK_SKIP_AGENTS=1", post)
	if err != nil {
		t.Fatalf("sidekick install run failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, "ALL_GOOD") {
		t.Fatalf("expected ALL_GOOD sentinel in output:\n%s", out)
	}
	for _, want := range []string{
		"sidekick hook write",
		".claude/skills/sidekick/SKILL.md",
		".codex/skills/sidekick/SKILL.md",
		".agents/skills/sidekick/SKILL.md",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}
