// Package config loads `hud.yaml` from disk and converts entries into
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

	"github.com/uriahlevy/hud/internal/verifier"
)

// File is the on-disk shape of `hud.yaml`.
type File struct {
	GoalSource string `yaml:"goal_source"` // "prompt" | "manual"; informational only for MVP
	// QuietPeriod sets a minimum gap between verifier batch runs across all
	// verifiers (LLM calls are expensive). Bursts of file edits inside the
	// window are coalesced; the next batch fires once the window elapses,
	// so we never miss a change. Empty → use the runtime default.
	QuietPeriod string         `yaml:"quiet_period,omitempty"`
	Verifiers   []VerifierSpec `yaml:"verifiers"`
}

// VerifierSpec mirrors verifier.Verifier with YAML tags.
type VerifierSpec struct {
	Name      string   `yaml:"name"`
	Direction string   `yaml:"direction"`
	Command   []string `yaml:"command"`
	Timeout   string   `yaml:"timeout,omitempty"` // duration string, e.g. "60s"
}

// validDirections accepts the 8 compass directions used by the layout.
var validDirections = map[string]bool{
	"N": true, "NE": true, "E": true, "SE": true,
	"S": true, "SW": true, "W": true, "NW": true,
}

// Load reads hud.yaml from `path`. If `path` is empty, it walks upward from
// cwd looking for `hud.yaml`. Returns (nil, os.ErrNotExist) if not found.
func Load(path string) (*File, string, error) {
	if path == "" {
		var err error
		path, err = findUpwards("hud.yaml")
		if err != nil {
			return nil, "", err
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

// Resolve converts the parsed file into runtime verifiers, resolving any
// relative command paths against `configDir`.
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
		if len(vs.Command) == 0 {
			return nil, fmt.Errorf("verifier %s: command is required", vs.Name)
		}
		cmd := append([]string(nil), vs.Command...)
		if strings.HasPrefix(cmd[0], "./") || strings.HasPrefix(cmd[0], "../") {
			cmd[0] = filepath.Join(configDir, cmd[0])
		}
		var timeout time.Duration
		if vs.Timeout != "" {
			t, err := time.ParseDuration(vs.Timeout)
			if err != nil {
				return nil, fmt.Errorf("verifier %s: bad timeout %q: %w", vs.Name, vs.Timeout, err)
			}
			timeout = t
		}
		out = append(out, verifier.Verifier{
			Name:      vs.Name,
			Direction: dir,
			Command:   cmd,
			Timeout:   timeout,
		})
	}
	return out, nil
}

// findUpwards searches for `name` starting at cwd and walking up to the
// filesystem root.
func findUpwards(name string) (string, error) {
	dir, err := os.Getwd()
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
