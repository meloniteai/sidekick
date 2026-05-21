// Package config loads `sidekick.yaml` from disk and converts entries into
// runtime verifier.Verifier instances.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/meloniteai/sidekick/internal/fetch"
	"github.com/meloniteai/sidekick/internal/verifier"
)

// File is the on-disk shape of `sidekick.yaml`.
type File struct {
	GoalSource string `yaml:"goal_source"` // "prompt" | "manual"; informational only for MVP
	// QuietPeriod sets a minimum gap between verifier batch runs across all
	// verifiers (LLM calls are expensive). Bursts of file edits inside the
	// window are coalesced; the next batch fires once the window elapses,
	// so we never miss a change. Empty → use the runtime default.
	QuietPeriod string `yaml:"quiet_period,omitempty"`
	// SessionIdleTimeout controls how long an inactive non-default worktree
	// session remains in memory. Empty means the daemon default; "0" disables
	// idle GC.
	SessionIdleTimeout string         `yaml:"session_idle_timeout,omitempty"`
	Verifiers          []VerifierSpec `yaml:"verifiers"`
}

// VerifierSpec mirrors verifier.Verifier with YAML tags.
type VerifierSpec struct {
	Name        string             `yaml:"name"`
	Type        string             `yaml:"type,omitempty"`
	Disabled    bool               `yaml:"disabled,omitempty"`
	Direction   string             `yaml:"direction"`
	Command     []string           `yaml:"command,omitempty"`
	Timeout     string             `yaml:"timeout,omitempty"` // duration string, e.g. "60s"
	LLM         AgentVerifierSpec  `yaml:"llm,omitempty"`     // yaml key kept as "llm" for backward compat
	Binary      BinaryVerifierSpec `yaml:"binary,omitempty"`
	Permissions *PermissionsSpec   `yaml:"permissions,omitempty"`
	Source      *SourceSpec        `yaml:"source,omitempty"`
}

// PermissionsSpec is the YAML shape of the advisory permission block.
// All fields are optional; missing values are treated conservatively
// (filesystem defaults to "read-only", env defaults to nil = "minimal").
//
// AllowedTools is enforced (not advisory) for the Claude agent: entries
// are appended to the hardcoded baseline list at spawn time, widening
// what the verifier subprocess can call without replacing the safe
// defaults. Ignored for command/binary verifiers and for non-Claude
// agents.
type PermissionsSpec struct {
	Network      bool     `yaml:"network,omitempty"`
	Filesystem   string   `yaml:"filesystem,omitempty"`
	Env          []string `yaml:"env,omitempty"`
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
}

// SourceSpec describes where a verifier's script or skill was fetched from.
// Populated automatically by `sidekick verifier add`; can also be authored by
// hand for self-documenting sidekick.yaml files. The sha256 is mandatory for
// remote sources — Sidekick refuses to load a remote script whose hash drifts.
type SourceSpec struct {
	URL    string `yaml:"url,omitempty"`
	Ref    string `yaml:"ref,omitempty"`
	SHA256 string `yaml:"sha256,omitempty"`
}

// AgentVerifierSpec configures a native agent-backed verifier.
type AgentVerifierSpec struct {
	Agent    string           `yaml:"agent,omitempty"`
	Model    string           `yaml:"model,omitempty"`
	Thinking string           `yaml:"thinking,omitempty"`
	Skill    string           `yaml:"skill,omitempty"`
	Custom   *CustomAgentSpec `yaml:"custom,omitempty"`
}

// CustomAgentSpec configures a non-built-in agent CLI. The command array
// is template-substituted with {{.Model}}, {{.Thinking}}, and {{.Skill}}
// before exec; stdin is fed the assembled prompt body.
//
// This is the v0.1 pluggability story: Claude and Codex are supported
// natively, anything else lands here. Future versions may consume more
// structured agent metadata (input format, response envelope shape) so
// the parser can extract usage telemetry from custom agents too.
type CustomAgentSpec struct {
	Command  []string `yaml:"command"`
	StdinFmt string   `yaml:"stdin,omitempty"` // "prompt" (default) | "none"
}

// BinaryVerifierSpec configures a native exit-code verifier.
type BinaryVerifierSpec struct {
	Command    []string `yaml:"command"`
	PassReason string   `yaml:"pass_reason,omitempty"`
	FailReason string   `yaml:"fail_reason,omitempty"`
}

// validDirections accepts the 8 compass directions used by the layout.
var validDirections = map[string]bool{
	"N": true, "NE": true, "E": true, "SE": true,
	"S": true, "SW": true, "W": true, "NW": true,
}

// Load reads sidekick.yaml from `path`. If `path` is empty, it walks upward from
// cwd looking for `sidekick.yaml`. Returns (nil, os.ErrNotExist) if not found.
func Load(path string) (*File, string, error) {
	return LoadFrom(path, "")
}

// LoadFrom reads sidekick.yaml from `path`, or walks upward from startDir when
// path is empty. When no project sidekick.yaml is found we fall back to the
// global config at GlobalPath (typically $HOME/.sidekick/sidekick.yaml) so users
// can install verifiers once and see them in every repo. Project files
// shadow the global completely — no merging — so a project sidekick.yaml is a
// hard override.
func LoadFrom(path, startDir string) (*File, string, error) {
	if path == "" {
		var err error
		path, err = findUpwardsFrom("sidekick.yaml", startDir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, "", err
			}
			gp, gerr := GlobalPath()
			if gerr != nil {
				return nil, "", err
			}
			if _, statErr := os.Stat(gp); statErr != nil {
				return nil, "", err
			}
			path = gp
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, path, fmt.Errorf("parse %s: %w", path, err)
	}
	return &f, path, nil
}

// GlobalPath returns the location of the global sidekick.yaml — the one Sidekick
// reads when no project-level file is found by walking upward from cwd.
// Defaults to $HOME/.sidekick/sidekick.yaml; tests can override with $SIDEKICK_GLOBAL_CONFIG.
func GlobalPath() (string, error) {
	if p := os.Getenv("SIDEKICK_GLOBAL_CONFIG"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sidekick", "sidekick.yaml"), nil
}

// Save writes f back to path as sidekick.yaml.
func Save(path string, f *File) error {
	raw, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := WriteFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// SetVerifierDisabled persists a verifier's disabled flag in sidekick.yaml.
func SetVerifierDisabled(path, name string, disabled bool) error {
	f, path, err := Load(path)
	if err != nil {
		return err
	}
	for i := range f.Verifiers {
		if f.Verifiers[i].Name == name {
			f.Verifiers[i].Disabled = disabled
			return Save(path, f)
		}
	}
	return fmt.Errorf("verifier %q not found in %s", name, path)
}

// WriteFileAtomic writes data to path via a temp file + rename so readers never
// observe a torn write. Shared by config saves and the verifier installer/editor.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
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

// ResolveQuietPeriod parses the optional root-level quiet_period field. A
// missing or empty value returns 0, signalling "use the runtime default".
// Negative values are rejected so a typo (e.g. "-2s") fails loudly rather
// than collapsing to 0.
func (f *File) ResolveQuietPeriod() (time.Duration, error) {
	if f.QuietPeriod == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(f.QuietPeriod)
	if err != nil {
		return 0, fmt.Errorf("bad quiet_period %q: %w", f.QuietPeriod, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("quiet_period must be non-negative, got %s", d)
	}
	return d, nil
}

// ResolveSessionIdleTimeout parses the optional root-level
// session_idle_timeout field. Empty returns 0 so callers can keep their
// runtime default; the literal duration 0 disables idle GC.
func (f *File) ResolveSessionIdleTimeout() (time.Duration, bool, error) {
	if f.SessionIdleTimeout == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(f.SessionIdleTimeout)
	if err != nil {
		return 0, true, fmt.Errorf("bad session_idle_timeout %q: %w", f.SessionIdleTimeout, err)
	}
	if d < 0 {
		return 0, true, fmt.Errorf("session_idle_timeout must be non-negative, got %s", d)
	}
	return d, true, nil
}

// ValidateStructural runs Resolve's field-level checks (required fields,
// duplicate names, valid direction/timeout/type, agent/command/binary
// minima) without touching the filesystem, trust store, or fetch cache.
// Used by the in-TUI create wizard, where a user may legitimately reference
// a skill or script they are about to create — the existence check still
// fires at `sidekick start` load time via Resolve.
func (f *File) ValidateStructural() error {
	if len(f.Verifiers) == 0 {
		return errors.New("no verifiers configured")
	}
	seen := map[string]bool{}
	for i, vs := range f.Verifiers {
		if vs.Name == "" {
			return fmt.Errorf("verifier #%d: missing name", i+1)
		}
		if seen[vs.Name] {
			return fmt.Errorf("duplicate verifier name %q", vs.Name)
		}
		seen[vs.Name] = true
		if !validDirections[strings.ToUpper(vs.Direction)] {
			return fmt.Errorf("verifier %s: invalid direction %q (want one of N/NE/E/SE/S/SW/W/NW)", vs.Name, vs.Direction)
		}
		if vs.Timeout != "" {
			if _, err := time.ParseDuration(vs.Timeout); err != nil {
				return fmt.Errorf("verifier %s: bad timeout %q: %w", vs.Name, vs.Timeout, err)
			}
		}
		hasRemote := vs.Source != nil && vs.Source.URL != ""
		if hasRemote && vs.Source.SHA256 == "" {
			return fmt.Errorf("verifier %s: remote source requires sha256 pin (got url=%q without sha256)", vs.Name, vs.Source.URL)
		}
		kind := strings.ToLower(vs.Type)
		if kind == "llm" {
			kind = verifier.TypeAgent
		}
		if kind == "" {
			kind = verifier.TypeCommand
		}
		switch kind {
		case verifier.TypeCommand:
			if len(vs.Command) == 0 && !hasRemote {
				return fmt.Errorf("verifier %s: command is required", vs.Name)
			}
		case verifier.TypeAgent:
			if !hasRemote && strings.TrimSpace(vs.LLM.Skill) == "" {
				return fmt.Errorf("verifier %s: agent.skill is required", vs.Name)
			}
			if strings.EqualFold(vs.LLM.Agent, "custom") {
				if vs.LLM.Custom == nil || len(vs.LLM.Custom.Command) == 0 {
					return fmt.Errorf("verifier %s: llm.custom.command is required when agent: custom", vs.Name)
				}
			}
		case verifier.TypeBinary:
			if len(vs.Binary.Command) == 0 && !hasRemote {
				return fmt.Errorf("verifier %s: binary.command is required", vs.Name)
			}
		default:
			return fmt.Errorf("verifier %s: unknown type %q", vs.Name, vs.Type)
		}
	}
	return nil
}

// Resolve converts the parsed file into runtime verifiers, resolving any
// relative local paths against `configDir`. Remote verifiers must carry a
// sha256 pin in their source block; the pin is the only integrity check
// (see fetch.Resolve, which refuses to return drifted bytes).
func (f *File) Resolve(configDir string) ([]verifier.Verifier, error) {
	if len(f.Verifiers) == 0 {
		return nil, errors.New("no verifiers configured")
	}
	out := make([]verifier.Verifier, 0, len(f.Verifiers))
	seen := map[string]bool{}
	for i, vs := range f.Verifiers {
		if vs.Name == "" {
			return nil, fmt.Errorf("verifier #%d: missing name", i+1)
		}
		if seen[vs.Name] {
			return nil, fmt.Errorf("duplicate verifier name %q", vs.Name)
		}
		seen[vs.Name] = true
		dir := strings.ToUpper(vs.Direction)
		if !validDirections[dir] {
			return nil, fmt.Errorf("verifier %s: invalid direction %q (want one of N/NE/E/SE/S/SW/W/NW)", vs.Name, vs.Direction)
		}
		kind := strings.ToLower(vs.Type)
		if kind == "llm" {
			kind = verifier.TypeAgent // "llm" accepted as alias for backward compat
		}
		if kind == "" {
			kind = verifier.TypeCommand
		}
		var timeout time.Duration
		if vs.Timeout != "" {
			t, err := time.ParseDuration(vs.Timeout)
			if err != nil {
				return nil, fmt.Errorf("verifier %s: bad timeout %q: %w", vs.Name, vs.Timeout, err)
			}
			timeout = t
		}
		v := verifier.Verifier{
			Name:      vs.Name,
			Direction: dir,
			Type:      kind,
			Disabled:  vs.Disabled,
			Timeout:   timeout,
		}
		// remoteArtefact is the cached local path of a fetched skill or
		// script when the verifier has source.url set. Empty for purely
		// local verifiers. Used below to override skill / command paths
		// before further validation.
		var remoteArtefact string
		if vs.Source != nil && vs.Source.URL != "" {
			if vs.Source.SHA256 == "" {
				return nil, fmt.Errorf("verifier %s: remote source requires sha256 pin (got url=%q without sha256)", vs.Name, vs.Source.URL)
			}
			ext := remoteExt(kind, vs)
			path, err := fetch.Resolve(fetch.Pin{
				URL:    vs.Source.URL,
				SHA256: vs.Source.SHA256,
				Ext:    ext,
			})
			if err != nil {
				return nil, fmt.Errorf("verifier %s: %w", vs.Name, err)
			}
			remoteArtefact = path
		}

		switch kind {
		case verifier.TypeCommand:
			if len(vs.Command) == 0 && remoteArtefact == "" {
				return nil, fmt.Errorf("verifier %s: command is required", vs.Name)
			}
			cmd := append([]string(nil), vs.Command...)
			if remoteArtefact != "" {
				if len(cmd) == 0 {
					cmd = []string{remoteArtefact}
				} else {
					cmd[0] = remoteArtefact
				}
				v.Command = cmd
				// Skip looksLikeLocalScript existence check — fetch.Resolve
				// already established the file is on disk.
			} else {
				rawCmd := cmd[0]
				v.Command = resolveCommand(configDir, cmd)
				if err := checkLocalScript(vs.Name, rawCmd, v.Command[0]); err != nil {
					return nil, err
				}
			}
		case verifier.TypeAgent:
			agent := vs.LLM.Agent
			if agent == "" {
				agent = "claude"
			}
			var skill string
			if remoteArtefact != "" {
				skill = remoteArtefact
			} else {
				if vs.LLM.Skill == "" {
					return nil, fmt.Errorf("verifier %s: agent.skill is required", vs.Name)
				}
				skill = resolveLocalPath(configDir, vs.LLM.Skill)
				if err := checkSkillFile(vs.Name, skill); err != nil {
					return nil, err
				}
			}
			ac := verifier.AgentConfig{
				Agent:    strings.ToLower(agent),
				Model:    vs.LLM.Model,
				Thinking: vs.LLM.Thinking,
				Skill:    skill,
			}
			if vs.Permissions != nil && len(vs.Permissions.AllowedTools) > 0 {
				ac.AllowedTools = append([]string(nil), vs.Permissions.AllowedTools...)
			}
			if strings.EqualFold(agent, "custom") {
				if vs.LLM.Custom == nil || len(vs.LLM.Custom.Command) == 0 {
					return nil, fmt.Errorf("verifier %s: llm.custom.command is required when agent: custom", vs.Name)
				}
				ac.Custom = verifier.CustomAgent{
					Command:  append([]string(nil), vs.LLM.Custom.Command...),
					StdinFmt: vs.LLM.Custom.StdinFmt,
				}
			}
			v.Agent = ac
		case verifier.TypeBinary:
			if len(vs.Binary.Command) == 0 && remoteArtefact == "" {
				return nil, fmt.Errorf("verifier %s: binary.command is required", vs.Name)
			}
			cmd := append([]string(nil), vs.Binary.Command...)
			if remoteArtefact != "" {
				if len(cmd) == 0 {
					cmd = []string{remoteArtefact}
				} else {
					cmd[0] = remoteArtefact
				}
				v.Binary = verifier.BinaryConfig{
					Command:    cmd,
					PassReason: vs.Binary.PassReason,
					FailReason: vs.Binary.FailReason,
				}
			} else {
				rawCmd := cmd[0]
				resolved := resolveCommand(configDir, cmd)
				if err := checkLocalScript(vs.Name, rawCmd, resolved[0]); err != nil {
					return nil, err
				}
				v.Binary = verifier.BinaryConfig{
					Command:    resolved,
					PassReason: vs.Binary.PassReason,
					FailReason: vs.Binary.FailReason,
				}
			}
		default:
			return nil, fmt.Errorf("verifier %s: invalid type %q (want command, agent, or binary)", vs.Name, vs.Type)
		}
		if vs.Permissions != nil {
			fs := strings.ToLower(strings.TrimSpace(vs.Permissions.Filesystem))
			switch fs {
			case "", "read-only", "read-write", "none":
			default:
				return nil, fmt.Errorf("verifier %s: permissions.filesystem %q (want one of read-only, read-write, none)", vs.Name, vs.Permissions.Filesystem)
			}
			v.Permissions = verifier.Permissions{
				Network:      vs.Permissions.Network,
				Filesystem:   fs,
				Env:          append([]string(nil), vs.Permissions.Env...),
				AllowedTools: append([]string(nil), vs.Permissions.AllowedTools...),
			}
		}
		if remoteArtefact != "" {
			v.Source = "remote"
			v.SourceURL = vs.Source.URL
			v.SHA256 = vs.Source.SHA256
		} else {
			v.Source = "local"
		}
		out = append(out, v)
	}
	return out, nil
}

// remoteExt picks an extension hint for the cache filename based on the
// verifier kind. The extension is purely cosmetic — fetch is content-
// addressed by sha256 — but it makes `~/.sidekick/cache/` browsable.
func remoteExt(kind string, vs VerifierSpec) string {
	switch kind {
	case verifier.TypeAgent:
		return ".md"
	case verifier.TypeBinary, verifier.TypeCommand:
		return ".sh"
	}
	return ""
}

// checkSkillFile verifies the configured skill path exists and is readable.
// Catching the missing-skill case at config load — instead of 30 seconds in
// when the first edit lands — is one of the cheapest UX wins for new users.
func checkSkillFile(verifierName, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verifier %s: agent.skill %q not readable: %w", verifierName, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("verifier %s: agent.skill %q is a directory, not a file", verifierName, path)
	}
	return nil
}

// checkLocalScript verifies that a script the user clearly intended to be
// in-tree (path written as ./foo, ../foo, ~/foo, or /abs/foo) actually
// exists. Bare names like "go", "bash", "echo" are PATH lookups at runtime
// and intentionally not validated here — they're the standard way users
// shell out to system tools, and validating them would reject any host
// that uses a different executable name.
//
// raw is the original string from sidekick.yaml; resolved is the cwd-relative
// or home-relative form after resolveLocalPath.
func checkLocalScript(verifierName, raw, resolved string) error {
	if !looksLikeLocalScript(raw) {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("verifier %s: command %q not found: %w", verifierName, resolved, err)
	}
	if info.IsDir() {
		return fmt.Errorf("verifier %s: command %q is a directory, not an executable", verifierName, resolved)
	}
	return nil
}

func looksLikeLocalScript(p string) bool {
	switch {
	case strings.HasPrefix(p, "./"), strings.HasPrefix(p, "../"), strings.HasPrefix(p, "~/"), strings.HasPrefix(p, "/"):
		return true
	}
	return false
}

func resolveCommand(configDir string, in []string) []string {
	cmd := append([]string(nil), in...)
	if len(cmd) > 0 {
		cmd[0] = resolveLocalPath(configDir, cmd[0])
	}
	return cmd
}

// ResolveLocalPath resolves the local path forms accepted by sidekick.yaml against
// configDir. Non-local command names are returned unchanged.
func ResolveLocalPath(configDir, p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return filepath.Join(configDir, p)
	}
	return p
}

func resolveLocalPath(configDir, p string) string {
	return ResolveLocalPath(configDir, p)
}

// findUpwards searches for `name` starting at cwd and walking up to the
// filesystem root.
func findUpwards(name string) (string, error) {
	return findUpwardsFrom(name, "")
}

func findUpwardsFrom(name, startDir string) (string, error) {
	dir, err := os.Getwd()
	if startDir != "" {
		dir = startDir
	} else if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
