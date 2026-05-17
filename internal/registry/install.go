package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uriahlevy/hud/internal/config"
)

// Scope picks which hud.yaml the installer writes into. ScopeProject
// uses the currently-loaded project file (or ./hud.yaml in cwd if none
// exists yet); ScopeGlobal uses config.GlobalPath().
type Scope int

const (
	ScopeProject Scope = iota
	ScopeGlobal
)

// InstallOptions describes a single install. ProjectPath is the path of
// the project's currently-loaded hud.yaml (if any); used by ScopeProject
// to decide where to write. Direction overrides Manifest.Direction so
// the user can pick a slot from the UI before committing.
type InstallOptions struct {
	Scope       Scope
	Manifest    Manifest
	ProjectPath string
	Direction   string
}

// InstallResult describes the outcome of an Install. FinalName differs
// from Manifest.Name only when the name collided in the target file and
// we appended a "-2"/"-3"/… suffix to keep the install idempotent.
type InstallResult struct {
	Path      string
	FinalName string
}

// Install appends a source-pinned VerifierSpec for the manifest to the
// hud.yaml selected by opts.Scope. The sha256 in Manifest.SHA256 is the
// only integrity check; subsequent `hud start` invocations re-verify
// fetched bytes against it (see fetch.Resolve).
//
// On name collision the new entry is renamed with a numeric suffix
// rather than overwriting — installing the same verifier twice is a
// no-op-ish bump, not a destructive operation.
func Install(opts InstallOptions) (InstallResult, error) {
	if opts.Manifest.SHA256 == "" {
		return InstallResult{}, errors.New("manifest is missing sha256")
	}
	if opts.Manifest.RawURL == "" {
		return InstallResult{}, errors.New("manifest is missing source URL")
	}

	path, err := resolveTargetPath(opts)
	if err != nil {
		return InstallResult{}, err
	}
	if err := ensureFile(path); err != nil {
		return InstallResult{}, err
	}

	f, _, err := config.Load(path)
	if err != nil {
		return InstallResult{}, fmt.Errorf("load %s: %w", path, err)
	}
	if f == nil {
		f = &config.File{}
	}

	dir := opts.Direction
	if dir == "" {
		dir = opts.Manifest.Direction
	}
	if dir == "" {
		dir = "NE"
	}

	spec := config.VerifierSpec{
		Name:      uniqueName(f, opts.Manifest.Name),
		Type:      opts.Manifest.Type,
		Direction: strings.ToUpper(dir),
		Timeout:   opts.Manifest.DefaultTimeout,
		Source: &config.SourceSpec{
			URL:    opts.Manifest.RawURL,
			SHA256: opts.Manifest.SHA256,
		},
	}
	if opts.Manifest.Type == "agent" {
		spec.LLM = config.AgentVerifierSpec{
			Agent:    opts.Manifest.Agent.Agent,
			Model:    opts.Manifest.Agent.Model,
			Thinking: opts.Manifest.Agent.Thinking,
		}
	}
	if hasPermissions(opts.Manifest.Permissions) {
		spec.Permissions = &config.PermissionsSpec{
			Network:      opts.Manifest.Permissions.Network,
			Filesystem:   opts.Manifest.Permissions.Filesystem,
			Env:          append([]string(nil), opts.Manifest.Permissions.Env...),
			AllowedTools: append([]string(nil), opts.Manifest.Permissions.AllowedTools...),
		}
	}

	f.Verifiers = append(f.Verifiers, spec)
	if err := config.Save(path, f); err != nil {
		return InstallResult{}, fmt.Errorf("save %s: %w", path, err)
	}
	return InstallResult{Path: path, FinalName: spec.Name}, nil
}

func resolveTargetPath(opts InstallOptions) (string, error) {
	switch opts.Scope {
	case ScopeGlobal:
		return config.GlobalPath()
	case ScopeProject:
		if opts.ProjectPath != "" {
			return opts.ProjectPath, nil
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, "hud.yaml"), nil
	default:
		return "", fmt.Errorf("unknown scope %d", opts.Scope)
	}
}

// ensureFile creates an empty hud.yaml (and any missing parent dirs) at
// path if it doesn't exist. Permission bits intentionally mirror what
// config.Save uses so the freshly-created file isn't broader than the
// loader expects (0o600).
func ensureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		// Race: another writer created it between Stat and OpenFile.
		// Treat that as success.
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	return f.Close()
}

func uniqueName(f *config.File, want string) string {
	if !nameExists(f, want) {
		return want
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", want, i)
		if !nameExists(f, candidate) {
			return candidate
		}
	}
}

func nameExists(f *config.File, name string) bool {
	for _, v := range f.Verifiers {
		if strings.EqualFold(v.Name, name) {
			return true
		}
	}
	return false
}

func hasPermissions(p ManifestPermSet) bool {
	return p.Network || p.Filesystem != "" || len(p.Env) > 0 || len(p.AllowedTools) > 0
}
