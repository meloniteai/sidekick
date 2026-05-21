package registry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/meloniteai/sidekick/internal/config"
	"github.com/meloniteai/sidekick/internal/fetch"
)

// Scope picks which sidekick.yaml the installer writes into. ScopeProject
// uses the currently-loaded project file (or ./.sidekick/sidekick.yaml in cwd
// if none exists yet); ScopeGlobal uses config.GlobalPath().
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

// CopyVerifierOptions describes copying one existing verifier entry between
// project and global sidekick.yaml scopes.
type CopyVerifierOptions struct {
	SourcePath  string
	Target      Scope
	ProjectPath string
	Name        string

	// Fetch retrieves remote artefact bytes when copying a source-pinned
	// verifier into project scope. Nil uses verifiedDownload.
	Fetch func(rawURL, sha256 string) ([]byte, error)
}

// CopyVerifierResult describes the copied verifier entry.
type CopyVerifierResult struct {
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

// CopyVerifier copies an existing verifier from SourcePath into the target
// scope. Remote verifiers copied to project scope are materialised into an
// editable project-owned file, matching Install(ScopeProject). Local
// skill/script artefacts are copied beside the target config and the YAML path
// is rewritten relative to that config.
func CopyVerifier(opts CopyVerifierOptions) (CopyVerifierResult, error) {
	if strings.TrimSpace(opts.SourcePath) == "" {
		return CopyVerifierResult{}, errors.New("source config path is required")
	}
	if strings.TrimSpace(opts.Name) == "" {
		return CopyVerifierResult{}, errors.New("verifier name is required")
	}
	targetPath, err := resolveTargetPath(InstallOptions{Scope: opts.Target, ProjectPath: opts.ProjectPath})
	if err != nil {
		return CopyVerifierResult{}, err
	}
	if sameFilesystemPath(opts.SourcePath, targetPath) {
		return CopyVerifierResult{}, fmt.Errorf("source and target are both %s", targetPath)
	}
	sourceFile, sourcePath, err := config.Load(opts.SourcePath)
	if err != nil {
		return CopyVerifierResult{}, fmt.Errorf("load %s: %w", opts.SourcePath, err)
	}
	sourceSpec, ok := verifierSpecByName(sourceFile, opts.Name)
	if !ok {
		return CopyVerifierResult{}, fmt.Errorf("verifier %q not found in %s", opts.Name, sourcePath)
	}
	if err := ensureFile(targetPath); err != nil {
		return CopyVerifierResult{}, err
	}
	targetFile, _, err := config.Load(targetPath)
	if err != nil {
		return CopyVerifierResult{}, fmt.Errorf("load %s: %w", targetPath, err)
	}
	if targetFile == nil {
		targetFile = &config.File{}
	}

	spec := cloneVerifierSpec(sourceSpec)
	spec.Name = uniqueName(targetFile, spec.Name)
	targetDir := filepath.Dir(targetPath)
	sourceDir := filepath.Dir(sourcePath)
	if opts.Target == ScopeProject && spec.Source != nil && spec.Source.URL != "" {
		m := manifestFromSpec(spec)
		if m.SHA256 == "" {
			return CopyVerifierResult{}, fmt.Errorf("verifier %q remote source is missing sha256", sourceSpec.Name)
		}
		if err := materialiseIntoProject(targetDir, &spec, InstallOptions{Manifest: m, Fetch: opts.Fetch}); err != nil {
			return CopyVerifierResult{}, err
		}
	} else if spec.Source == nil || spec.Source.URL == "" {
		if err := copyLocalArtefact(sourceDir, targetDir, &spec); err != nil {
			return CopyVerifierResult{}, err
		}
	}

	targetFile.Verifiers = append(targetFile.Verifiers, spec)
	if err := config.Save(targetPath, targetFile); err != nil {
		return CopyVerifierResult{}, fmt.Errorf("save %s: %w", targetPath, err)
	}
	return CopyVerifierResult{Path: targetPath, FinalName: spec.Name}, nil
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
		return filepath.Join(cwd, ".sidekick", "sidekick.yaml"), nil
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

func verifierSpecByName(f *config.File, name string) (config.VerifierSpec, bool) {
	for _, v := range f.Verifiers {
		if strings.EqualFold(v.Name, name) {
			return v, true
		}
	}
	return config.VerifierSpec{}, false
}

func cloneVerifierSpec(in config.VerifierSpec) config.VerifierSpec {
	out := in
	out.Command = append([]string(nil), in.Command...)
	if in.LLM.Custom != nil {
		custom := *in.LLM.Custom
		custom.Command = append([]string(nil), in.LLM.Custom.Command...)
		out.LLM.Custom = &custom
	}
	out.Binary.Command = append([]string(nil), in.Binary.Command...)
	if in.Permissions != nil {
		p := *in.Permissions
		p.Env = append([]string(nil), in.Permissions.Env...)
		p.AllowedTools = append([]string(nil), in.Permissions.AllowedTools...)
		out.Permissions = &p
	}
	if in.Source != nil {
		s := *in.Source
		out.Source = &s
	}
	return out
}

func manifestFromSpec(spec config.VerifierSpec) Manifest {
	kind := strings.ToLower(spec.Type)
	if kind == "" {
		kind = "command"
	}
	m := Manifest{
		Name:           spec.Name,
		Type:           kind,
		Direction:      spec.Direction,
		Slug:           slugify(spec.Name),
		DefaultTimeout: spec.Timeout,
	}
	if spec.Source != nil {
		m.RawURL = spec.Source.URL
		m.SHA256 = spec.Source.SHA256
	}
	m.Agent = ManifestAgent{
		Agent:    spec.LLM.Agent,
		Model:    spec.LLM.Model,
		Thinking: spec.LLM.Thinking,
	}
	if spec.Permissions != nil {
		m.Permissions = ManifestPermSet{
			Network:      spec.Permissions.Network,
			Filesystem:   spec.Permissions.Filesystem,
			Env:          append([]string(nil), spec.Permissions.Env...),
			AllowedTools: append([]string(nil), spec.Permissions.AllowedTools...),
		}
	}
	return m
}

func copyLocalArtefact(sourceDir, targetDir string, spec *config.VerifierSpec) error {
	kind := strings.ToLower(spec.Type)
	if kind == "" {
		kind = "command"
	}
	switch kind {
	case "agent", "llm":
		if spec.LLM.Skill == "" {
			return nil
		}
		rel, err := copyOneLocalArtefact(sourceDir, targetDir, spec.LLM.Skill, kind, artefactSlug(spec.Name, ""))
		if err != nil {
			return err
		}
		spec.LLM.Skill = rel
	case "binary":
		if len(spec.Binary.Command) == 0 || !looksLikeLocalPath(spec.Binary.Command[0]) {
			return nil
		}
		rel, err := copyOneLocalArtefact(sourceDir, targetDir, spec.Binary.Command[0], kind, artefactSlug(spec.Name, ""))
		if err != nil {
			return err
		}
		spec.Binary.Command[0] = rel
	default:
		if len(spec.Command) == 0 || !looksLikeLocalPath(spec.Command[0]) {
			return nil
		}
		rel, err := copyOneLocalArtefact(sourceDir, targetDir, spec.Command[0], kind, artefactSlug(spec.Name, ""))
		if err != nil {
			return err
		}
		spec.Command[0] = rel
	}
	return nil
}

func copyOneLocalArtefact(sourceDir, targetDir, raw, kind, slug string) (string, error) {
	src := config.ResolveLocalPath(sourceDir, raw)
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("copy local artefact %s: %w", src, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("copy local artefact %s: is a directory", src)
	}
	rel, defaultMode := projectArtefactPath(targetDir, kind, slug)
	dst := filepath.Join(targetDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", src, err)
	}
	defer in.Close()
	body, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", src, err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = defaultMode
	}
	if defaultMode&0o100 != 0 {
		mode |= 0o700
	}
	if err := config.WriteFileAtomic(dst, body, mode); err != nil {
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	return "./" + filepath.ToSlash(rel), nil
}

func looksLikeLocalPath(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "/")
}

func sameFilesystemPath(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		aa = filepath.Clean(a)
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		bb = filepath.Clean(b)
	}
	if resolved, err := filepath.EvalSymlinks(aa); err == nil {
		aa = resolved
	}
	if resolved, err := filepath.EvalSymlinks(bb); err == nil {
		bb = resolved
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

func hasPermissions(p ManifestPermSet) bool {
	return p.Network || p.Filesystem != "" || len(p.Env) > 0 || len(p.AllowedTools) > 0
}

// materialiseIntoProject downloads the artefact beside the project config and
// rewrites spec to reference that local path with no source block, turning the
// install into an editable, project-owned copy.
func materialiseIntoProject(configDir string, spec *config.VerifierSpec, opts InstallOptions) error {
	fetchFn := opts.Fetch
	if fetchFn == nil {
		fetchFn = verifiedDownload
	}
	body, err := fetchFn(opts.Manifest.RawURL, opts.Manifest.SHA256)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", opts.Manifest.RawURL, err)
	}

	kind := strings.ToLower(opts.Manifest.Type)
	rel, mode := projectArtefactPath(configDir, kind, artefactSlug(spec.Name, opts.Manifest.Slug))
	abs := filepath.Join(configDir, rel)
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

// projectArtefactPath returns the config-relative destination and mode for a
// materialised artefact. New project configs live inside .sidekick and use
// ./skills; legacy root sidekick.yaml files keep using ./.sidekick/skills.
// Scripts get +x because the runner exec(2)s them.
func projectArtefactPath(configDir, kind, slug string) (string, os.FileMode) {
	prefix := ".sidekick"
	if filepath.Base(configDir) == ".sidekick" {
		prefix = "."
	}
	switch kind {
	case "agent", "llm":
		return filepath.Join(prefix, "skills", slug, "SKILL.md"), 0o644
	default:
		return filepath.Join(prefix, "verifiers", slug+".sh"), 0o755
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
