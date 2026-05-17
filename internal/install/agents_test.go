package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeHook_EmptyInput(t *testing.T) {
	out, changed, err := MergeHook(nil, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("MergeHook: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for empty input")
	}
	got := decode(t, out)
	hooks := got["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(post))
	}
	entry := post[0].(map[string]any)
	if entry["matcher"] != HudPostToolUseClaude.Matcher {
		t.Errorf("matcher = %q, want %q", entry["matcher"], HudPostToolUseClaude.Matcher)
	}
	hookList := entry["hooks"].([]any)
	if len(hookList) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hookList))
	}
	hookEntry := hookList[0].(map[string]any)
	if hookEntry["command"] != HudPostToolUseClaude.Command {
		t.Errorf("command = %q, want %q", hookEntry["command"], HudPostToolUseClaude.Command)
	}
}

func TestMergeHook_AlreadyPresent_NoOp(t *testing.T) {
	first, _, err := MergeHook(nil, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	out, changed, err := MergeHook(first, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false on second merge")
	}
	if !bytes.Equal(out, first) {
		t.Fatal("expected output bytes unchanged on no-op")
	}
}

func TestMergeHook_PreservesUnrelatedTopLevelKeys(t *testing.T) {
	existing := []byte(`{
  "permissions": {"allow": ["Read", "Edit"]},
  "model": "claude-sonnet-4-5",
  "env": {"FOO": "bar"}
}`)
	out, changed, err := MergeHook(existing, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("MergeHook: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := decode(t, out)
	if _, ok := got["permissions"]; !ok {
		t.Error("permissions key was dropped")
	}
	if got["model"] != "claude-sonnet-4-5" {
		t.Errorf("model = %v, want claude-sonnet-4-5", got["model"])
	}
	env, ok := got["env"].(map[string]any)
	if !ok || env["FOO"] != "bar" {
		t.Errorf("env.FOO = %v, want bar", env)
	}
}

func TestMergeHook_PreservesUnrelatedPostToolUseEntries(t *testing.T) {
	existing := []byte(`{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/audit.sh"}]}
    ]
  }
}`)
	out, _, err := MergeHook(existing, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("MergeHook: %v", err)
	}
	got := decode(t, out)
	post := got["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(post))
	}
	gotMatchers := []string{}
	for _, e := range post {
		gotMatchers = append(gotMatchers, e.(map[string]any)["matcher"].(string))
	}
	wantMatchers := []string{"Bash", HudPostToolUseClaude.Matcher}
	if !equalUnordered(gotMatchers, wantMatchers) {
		t.Errorf("matchers = %v, want %v (unordered)", gotMatchers, wantMatchers)
	}
}

func TestMergeHook_AppendsToExistingMatcher(t *testing.T) {
	// Same matcher already present but with a different command. Our merge
	// should append our command into the existing entry's hooks list rather
	// than create a parallel entry with the same matcher.
	existing := []byte(`{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Write|Edit|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": "/other/tool"}]}
    ]
  }
}`)
	out, changed, err := MergeHook(existing, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("MergeHook: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := decode(t, out)
	post := got["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(post))
	}
	hooks := post[0].(map[string]any)["hooks"].([]any)
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks under matcher, got %d", len(hooks))
	}
	commands := []string{}
	for _, h := range hooks {
		commands = append(commands, h.(map[string]any)["command"].(string))
	}
	if !equalUnordered(commands, []string{"/other/tool", HudPostToolUseClaude.Command}) {
		t.Errorf("commands = %v", commands)
	}
}

func TestMergeHook_InvalidJSON_Refuses(t *testing.T) {
	_, _, err := MergeHook([]byte(`{ not json`), HudPostToolUseClaude)
	if err == nil {
		t.Fatal("expected error on invalid JSON, got nil")
	}
}

func TestMergeHookFile_CreatesParentDir(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "deep", "nested", "settings.json")
	changed, err := MergeHookFile(path, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("MergeHookFile: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for fresh file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), HudPostToolUseClaude.Command) {
		t.Errorf("file does not contain command: %s", raw)
	}
}

func TestMergeHookFile_IdempotentSecondCall(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	if _, err := MergeHookFile(path, HudPostToolUseClaude); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first, _ := os.ReadFile(path)
	changed, err := MergeHookFile(path, HudPostToolUseClaude)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if changed {
		t.Error("expected changed=false on second call")
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Errorf("file mutated on second call:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestDetect_FindsConfigDir(t *testing.T) {
	// Isolate from the host's PATH so we observe HasDir signal alone.
	t.Setenv("PATH", "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	d := Detect(home, AgentClaude)
	if !d.Found || !d.HasDir || d.HasCLI {
		t.Errorf("expected Found+HasDir+!HasCLI, got %+v", d)
	}
	d2 := Detect(home, AgentCodex)
	if d2.Found {
		t.Errorf("expected codex not found in clean tempdir with empty PATH, got %+v", d2)
	}
}

func TestSkillDirs_WritesBothCanonicalAndCrossAgent(t *testing.T) {
	home := "/h"
	got := SkillDirs(home, "hud", AgentClaude)
	want := []string{"/h/.claude/skills/hud", "/h/.agents/skills/hud"}
	if !equalSlice(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	got = SkillDirs(home, "hud", AgentCodex)
	want = []string{"/h/.codex/skills/hud", "/h/.agents/skills/hud"}
	if !equalSlice(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWriteSkill_WritesEveryTargetDir(t *testing.T) {
	home := t.TempDir()
	body := []byte("# hud skill\n")
	written, err := WriteSkill(home, AgentClaude, "hud", body, nil)
	if err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("expected 2 files written, got %d (%v)", len(written), written)
	}
	for _, p := range written {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%s content mismatch", p)
		}
	}
}

// ---- helpers ----

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode produced JSON: %v\n%s", err, raw)
	}
	return m
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[string]int{}
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
