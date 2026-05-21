package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/meloniteai/sidekick/internal/config"
	"github.com/meloniteai/sidekick/internal/fetch"
)

// Scope picks which sidekick.yaml the installer writes into. ScopeProject
// uses the currently-loaded project file (or ./sidekick.yaml in cwd if none
// exists yet); ScopeGlobal uses config.GlobalPath().
type Scope int

const (
	ScopeProject Scope = iota
	ScopeGlobal
)

// InstallOptions describes a single install. ProjectPath is the path of
// the project's currently-loaded sidekick.yaml (if any); used by ScopeProject
// to decide where to write. Direction overrides Manifest.Direction so
// the user can pick a slot from the UI before committing.
type InstallOptions struct {
	Scope       Scope
	Manifest    Manifest
	ProjectPath string
	Direction   string

	// Fetch retrieves and sha256-verifies the artefact bytes for a
	// ScopeProject install, which materialises the skill/script into the
	// project's .sidekick/ dir so it can be edited in-session (ScopeGlobal
	// installs never call this — they stay pinned to the shared cache).
	// Defaults to verifiedDownload when nil; tests inject a stub to avoid
	// real network I/O.
	Fetch func(rawURL, sha256 string) ([]byte, error)
}

// InstallResult describes the outcome of an Install. FinalName differs
// from Manifest.Name only when the name collided in the target file and
// we appended a "-2"/"-3"/… suffix to keep the install idempotent.
type InstallResult struct {
	Path      string
	FinalName string
}

// Install adds a VerifierSpec for the manifest to the sidekick.yaml chosen by
// opts.Scope. ScopeGlobal writes a source-pinned spec and leaves the artefact
// in the shared cache (immutable, shared across repos). ScopeProject downloads
// the artefact into the project's .sidekick/ dir and points the spec at that
// local path with no source block, so it can be edited in place. Name
// collisions get a numeric suffix rather than overwriting.
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

	// Project installs fork the artefact into the repo so it can be edited;
	// global installs stay pinned to the shared cache (see Install doc).
	if opts.Scope == ScopeProject {
		if err := materialiseIntoProject(filepath.Dir(path), &spec, opts); err != nil {
			return InstallResult{}, err
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
		return filepath.Join(cwd, "sidekick.yaml"), nil
	default:
		return "", fmt.Errorf("unknown scope %d", opts.Scope)
	}
}

// ensureFile creates an empty sidekick.yaml (and any missing parent dirs) at
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

// materialiseIntoProject downloads the artefact into projectDir/.sidekick/ and
// rewrites spec to reference that local path with no source block, turning the
// install into an editable, project-owned copy.
func materialiseIntoProject(projectDir string, spec *config.VerifierSpec, opts InstallOptions) error {
	fetchFn := opts.Fetch
	if fetchFn == nil {
		fetchFn = verifiedDownload
	}
	body, err := fetchFn(opts.Manifest.RawURL, opts.Manifest.SHA256)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", opts.Manifest.RawURL, err)
	}

	kind := strings.ToLower(opts.Manifest.Type)
	rel, mode := projectArtefactPath(kind, artefactSlug(spec.Name, opts.Manifest.Slug))
	abs := filepath.Join(projectDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(abs), err)
	}
	if err := config.WriteFileAtomic(abs, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", abs, err)
	}

	// "./"-prefixed so config.ResolveLocalPath anchors it to the config dir.
	local := "./" + filepath.ToSlash(rel)
	switch kind {
	case "agent", "llm":
		spec.LLM.Skill = local
	case "binary":
		spec.Binary.Command = []string{local}
	default: // command
		spec.Command = []string{local}
	}
	spec.Source = nil
	return nil
}

// projectArtefactPath returns the .sidekick-relative destination and mode for a
// materialised artefact. Scripts get +x because the runner exec(2)s them.
func projectArtefactPath(kind, slug string) (string, os.FileMode) {
	switch kind {
	case "agent", "llm":
		return filepath.Join(".sidekick", "skills", slug, "SKILL.md"), 0o644
	default:
		return filepath.Join(".sidekick", "verifiers", slug+".sh"), 0o755
	}
}

// artefactSlug derives the on-disk slug from the de-duplicated verifier name so
// renamed installs ("foo"/"foo-2") don't clobber each other's files.
func artefactSlug(finalName, catalogSlug string) string {
	if s := slugify(finalName); s != "" {
		return s
	}
	if s := slugify(catalogSlug); s != "" {
		return s
	}
	return "verifier"
}

func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	dashed := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dashed = false
			continue
		}
		if b.Len() > 0 && !dashed {
			b.WriteByte('-')
			dashed = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// verifiedDownload is the default artefact fetcher: an HTTPS GET whose bytes
// must hash to the manifest pin. It skips the shared cache since the bytes are
// copied into the project instead.
func verifiedDownload(rawURL, sha string) ([]byte, error) {
	body, err := fetch.Download(rawURL)
	if err != nil {
		return nil, err
	}
	if got := fetch.Hash(body); !strings.EqualFold(got, sha) {
		return nil, fmt.Errorf("sha256 mismatch: got %s, expected %s", got, sha)
	}
	return body, nil
}
