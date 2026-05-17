// Package install wires hud into the user's agent clients (Claude Code,
// Codex). It exposes pure functions for detection, JSON hook-merging, and
// skill installation so the cmd-side cobra command stays a thin shell.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Agent enumerates the agent clients we know how to wire up.
type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

// AllAgents returns the agents in a stable order for iteration / output.
func AllAgents() []Agent { return []Agent{AgentClaude, AgentCodex} }

// Detection reports which agent integration points exist on the machine.
// Found is true if either the agent's CLI is on PATH or its config dir
// exists — both are strong signals that the user has the agent installed.
type Detection struct {
	Agent    Agent
	Found    bool
	HomeDir  string // e.g. $HOME/.claude, $HOME/.codex
	CLIPath  string // resolved exec path, "" if CLI not found
	HasDir   bool
	HasCLI   bool
}

// Detect probes a single agent. It never errors — a missing $HOME just
// means everything is reported as not found.
func Detect(home string, agent Agent) Detection {
	d := Detection{Agent: agent}
	switch agent {
	case AgentClaude:
		d.HomeDir = filepath.Join(home, ".claude")
	case AgentCodex:
		d.HomeDir = filepath.Join(home, ".codex")
	}
	if info, err := os.Stat(d.HomeDir); err == nil && info.IsDir() {
		d.HasDir = true
	}
	if p, err := exec.LookPath(string(agent)); err == nil {
		d.HasCLI = true
		d.CLIPath = p
	}
	d.Found = d.HasDir || d.HasCLI
	return d
}

// DetectAll runs Detect for every known agent.
func DetectAll(home string) []Detection {
	all := AllAgents()
	out := make([]Detection, 0, len(all))
	for _, a := range all {
		out = append(out, Detect(home, a))
	}
	return out
}

// SkillDirs returns every on-disk location we should write the skill into
// for the given agent. We always write to the cross-agent open standard
// (~/.agents/skills/<name>/) plus the agent's native location, so a skill
// is discoverable however the agent prefers to scan.
//
//   Claude: ~/.claude/skills/<name>/
//   Codex:  ~/.codex/skills/<name>/   (best-effort; codex primarily reads
//                                      ~/.agents/skills, but writing here
//                                      too is cheap and idempotent.)
//   Both:   ~/.agents/skills/<name>/  (agentskills.io open standard)
func SkillDirs(home, name string, agent Agent) []string {
	cross := filepath.Join(home, ".agents", "skills", name)
	switch agent {
	case AgentClaude:
		return []string{
			filepath.Join(home, ".claude", "skills", name),
			cross,
		}
	case AgentCodex:
		return []string{
			filepath.Join(home, ".codex", "skills", name),
			cross,
		}
	}
	return nil
}

// WriteSkill writes a single SKILL.md (and any sibling files passed in
// `extra`) under every dir returned by SkillDirs for the agent. Existing
// files are overwritten — re-running hud install picks up the latest
// skill content shipped with the upgraded binary.
//
// `body` is the SKILL.md contents. `extra` is an optional map of relative
// filename → contents for additional files that ship with the skill (e.g.
// helper scripts, examples). Pass nil if there are none.
func WriteSkill(home string, agent Agent, name string, body []byte, extra map[string][]byte) ([]string, error) {
	var written []string
	for _, dir := range SkillDirs(home, name, agent) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return written, fmt.Errorf("mkdir %s: %w", dir, err)
		}
		skill := filepath.Join(dir, "SKILL.md")
		if err := writeFileAtomic(skill, body, 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", skill, err)
		}
		written = append(written, skill)
		for rel, content := range extra {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return written, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
			}
			if err := writeFileAtomic(path, content, 0o644); err != nil {
				return written, fmt.Errorf("write %s: %w", path, err)
			}
			written = append(written, path)
		}
	}
	return written, nil
}

// HookEntry mirrors a single Claude / Codex hook entry shape. Both agents
// share the same schema for the bit we care about: a matcher regex plus
// a list of hook commands to invoke on PostToolUse.
type HookEntry struct {
	Matcher string     `json:"matcher"`
	Hooks   []HookExec `json:"hooks"`
}

// HookExec is one command inside a HookEntry.
type HookExec struct {
	Type    string `json:"type"`              // always "command"
	Command string `json:"command"`           // e.g. "hud hook write"
	Timeout int    `json:"timeout,omitempty"` // seconds; omitted = agent default
}

// HookConfig is what we want present in the settings JSON. Other top-level
// keys are preserved verbatim during the merge.
type HookConfig struct {
	Event   string // "PostToolUse" — only event we currently emit
	Matcher string
	Command string
}

// HudPostToolUseClaude is the canonical Claude wiring.
var HudPostToolUseClaude = HookConfig{
	Event:   "PostToolUse",
	Matcher: "Write|Edit|MultiEdit|NotebookEdit",
	Command: "hud hook write",
}

// HudPostToolUseCodex is the canonical Codex wiring (adds apply_patch).
var HudPostToolUseCodex = HookConfig{
	Event:   "PostToolUse",
	Matcher: "apply_patch|Write|Edit|MultiEdit|NotebookEdit",
	Command: "hud hook write",
}

// MergeHook deep-merges a single hook config into the given settings JSON.
//
// Idempotent: if an entry with the same Event + Matcher + Command already
// exists, MergeHook returns the input bytes unchanged. Every other top-level
// key, every other event under "hooks", and every other entry under the
// target event are preserved verbatim.
//
// Pass `existing == nil` to start from an empty {} document.
//
// Returns (newBytes, changed, err). changed=false means the file was
// already in the desired state — callers can skip the write.
func MergeHook(existing []byte, cfg HookConfig) ([]byte, bool, error) {
	var root map[string]any
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, false, fmt.Errorf("parse existing settings: %w", err)
		}
	}
	if root == nil {
		root = map[string]any{}
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	rawEvent, _ := hooks[cfg.Event].([]any)
	// Build the entry we want present.
	desired := map[string]any{
		"matcher": cfg.Matcher,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": cfg.Command,
			},
		},
	}

	// Look for an existing entry with our matcher.
	for _, raw := range rawEvent {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["matcher"] != cfg.Matcher {
			continue
		}
		// Same matcher exists — check if our command is already in its hooks list.
		execs, _ := entry["hooks"].([]any)
		for _, e := range execs {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if em["type"] == "command" && em["command"] == cfg.Command {
				return existing, false, nil
			}
		}
		// Matcher exists but missing our command — append it.
		entry["hooks"] = append(execs, desired["hooks"].([]any)[0])
		return marshalSettings(root), true, nil
	}

	// No matching entry — append a new one.
	rawEvent = append(rawEvent, desired)
	hooks[cfg.Event] = rawEvent
	root["hooks"] = hooks
	return marshalSettings(root), true, nil
}

func marshalSettings(root map[string]any) []byte {
	// 2-space indent matches the existing examples files. Newline at EOF
	// to keep diffs against hand-edited files clean.
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		// json.Marshal on a generic map[string]any only fails for
		// unsupported types, which we never put in. Treat as a bug.
		panic(fmt.Sprintf("install: marshal settings: %v", err))
	}
	return append(out, '\n')
}

// MergeHookFile reads an existing file at path (treating ENOENT as empty),
// merges cfg, and atomically writes the result back. Returns (changed, err).
// The parent directory is created if needed.
func MergeHookFile(path string, cfg HookConfig) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	merged, changed, err := MergeHook(existing, cfg)
	if err != nil {
		return false, fmt.Errorf("merge %s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, merged, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// RegisterMCP shells out to the agent's own CLI to register the hud MCP
// server (user-scope). Returns (registered, ranCommand, err) where
// registered=true means the CLI exited 0; ranCommand is the argv we
// actually invoked (useful for --print / dry-run output). If the CLI is
// not on PATH, RegisterMCP returns (false, nil, exec.ErrNotFound) so the
// caller can surface a clear "claude not on PATH" warning without
// stopping the install.
//
// Commands run:
//   Claude: claude mcp add hud --scope user -- hud mcp
//   Codex:  codex  mcp add hud -- hud mcp
func RegisterMCP(agent Agent, stdout, stderr io.Writer) (registered bool, argv []string, err error) {
	switch agent {
	case AgentClaude:
		argv = []string{"claude", "mcp", "add", "hud", "--scope", "user", "--", "hud", "mcp"}
	case AgentCodex:
		argv = []string{"codex", "mcp", "add", "hud", "--", "hud", "mcp"}
	default:
		return false, nil, fmt.Errorf("unknown agent %q", agent)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return false, argv, err
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout = stdout
	c.Stderr = stderr
	if err := c.Run(); err != nil {
		return false, argv, err
	}
	return true, argv, nil
}

// HookFilePath returns where MergeHookFile should write for the given
// agent (user-scope).
func HookFilePath(home string, agent Agent) string {
	switch agent {
	case AgentClaude:
		return filepath.Join(home, ".claude", "settings.json")
	case AgentCodex:
		return filepath.Join(home, ".codex", "hooks.json")
	}
	return ""
}

// CanonicalHookConfig returns the hud PostToolUse wiring for the agent.
func CanonicalHookConfig(agent Agent) HookConfig {
	switch agent {
	case AgentClaude:
		return HudPostToolUseClaude
	case AgentCodex:
		return HudPostToolUseCodex
	}
	return HookConfig{}
}

// writeFileAtomic mirrors the pattern in internal/config/config.go:
// write to a sibling temp file, fsync via Close, rename into place.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
