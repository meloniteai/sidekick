// Package registry talks to the public GitHub-hosted verifier catalog
// (default meloniteai/sidekick-verifiers) so the in-TUI Remote Verifier
// Browser can list and install community verifiers without a separate
// auth flow.
//
// The repo is laid out as:
//
//	command/<slug>/manifest.yaml
//	command/<slug>/<artefact>
//	agent/<slug>/manifest.yaml
//	agent/<slug>/<artefact>.md
//	binary/<slug>/manifest.yaml
//	binary/<slug>/<artefact>
//
// Client walks the three top-level directories via the GitHub Contents
// API and fetches each per-verifier manifest from raw.githubusercontent.com.
// Per-verifier failures are tolerated (logged + skipped) so one bad
// manifest doesn't blank the browser.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultOwner / DefaultRepo / DefaultRef point at the canonical catalog.
// Callers (and tests) can override via NewClient when they need a fork
// or a pinned ref.
const (
	DefaultOwner = "meloniteai"
	DefaultRepo  = "sidekick-verifiers"
	DefaultRef   = "main"
)

// Type-name prefixes the catalog uses as directory names. Kept here
// rather than reimported from internal/verifier so this package stays
// self-describing and the catalog schema can evolve independently.
var manifestTypeDirs = []string{"command", "agent", "binary"}

// Manifest is the parsed contents of a per-verifier manifest.yaml. The
// shape is intentionally a superset of what sidekick.yaml understands today:
// fields the loader doesn't know about (Description, DefaultTimeout) are
// kept here for the UI but stripped before being written into sidekick.yaml
// at install time.
type Manifest struct {
	Name           string          `yaml:"name"`
	Type           string          `yaml:"type"` // command | agent | binary
	Description    string          `yaml:"description,omitempty"`
	Direction      string          `yaml:"direction,omitempty"`
	DefaultTimeout string          `yaml:"default_timeout,omitempty"`
	Artefact       string          `yaml:"artefact"`
	SHA256         string          `yaml:"sha256"`
	Agent          ManifestAgent   `yaml:"agent,omitempty"`
	Permissions    ManifestPermSet `yaml:"permissions,omitempty"`

	// Slug is the catalog directory name (e.g. "needs-tests"). Filled in
	// by the client, not by the YAML.
	Slug string `yaml:"-"`
	// RawURL is the raw.githubusercontent.com URL of Artefact. Filled in
	// by the client so the installer can hand it straight to fetch.Resolve.
	RawURL string `yaml:"-"`
}

// ManifestAgent mirrors the subset of AgentVerifierSpec a registry
// author can pre-declare for an agent verifier.
type ManifestAgent struct {
	Agent    string `yaml:"agent,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Thinking string `yaml:"thinking,omitempty"`
}

// ManifestPermSet mirrors PermissionsSpec without depending on the
// config package. AllowedTools is the headline new field: the install
// dialog displays it so the user can see what the verifier will be
// allowed to call before they accept.
type ManifestPermSet struct {
	Network      bool     `yaml:"network,omitempty"`
	Filesystem   string   `yaml:"filesystem,omitempty"`
	Env          []string `yaml:"env,omitempty"`
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
}

// Client fetches manifests from a GitHub-hosted verifier catalog. Safe
// for concurrent use; the per-listing cache is mutex-guarded.
type Client struct {
	httpc    *http.Client
	owner    string
	repo     string
	ref      string
	apiBase  string // overridable for tests; defaults to https://api.github.com
	rawBase  string // overridable for tests; defaults to https://raw.githubusercontent.com
	mu       sync.Mutex
	cache    []Manifest
	cacheErr error
	cached   bool
}

// New returns a Client pointed at owner/repo@ref. Empty arguments fall
// back to the Default constants so the common case is `registry.New("",
// "", "")`.
func New(owner, repo, ref string) *Client {
	if owner == "" {
		owner = DefaultOwner
	}
	if repo == "" {
		repo = DefaultRepo
	}
	if ref == "" {
		ref = DefaultRef
	}
	return &Client{
		httpc:   &http.Client{Timeout: 30 * time.Second},
		owner:   owner,
		repo:    repo,
		ref:     ref,
		apiBase: "https://api.github.com",
		rawBase: "https://raw.githubusercontent.com",
	}
}

// WithEndpoints overrides the GitHub API + raw base URLs. Test-only:
// production code uses the defaults baked into New.
func (c *Client) WithEndpoints(apiBase, rawBase string) *Client {
	c.apiBase = apiBase
	c.rawBase = rawBase
	return c
}

// contentsEntry is the slice of the GitHub Contents API response we
// actually use. We only need name + type to walk into per-verifier
// directories; everything else is dropped.
type contentsEntry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "dir" | "file"
}

// List fetches every verifier manifest in the catalog and returns them
// flattened. Results are cached for the lifetime of the Client so the
// detail pane doesn't re-fetch when the user navigates back and forth.
// Per-verifier failures (missing manifest, unparseable YAML) are
// dropped silently — one bad entry should not blank the whole browser.
func (c *Client) List(ctx context.Context) ([]Manifest, error) {
	c.mu.Lock()
	if c.cached {
		out, err := c.cache, c.cacheErr
		c.mu.Unlock()
		return out, err
	}
	c.mu.Unlock()

	var (
		all []Manifest
		mu  sync.Mutex
		wg  sync.WaitGroup
	)
	errs := make(chan error, len(manifestTypeDirs))
	for _, dir := range manifestTypeDirs {
		dir := dir
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries, err := c.listDir(ctx, dir)
			if err != nil {
				errs <- fmt.Errorf("%s: %w", dir, err)
				return
			}
			for _, e := range entries {
				if e.Type != "dir" {
					continue
				}
				m, err := c.fetchManifest(ctx, dir, e.Name)
				if err != nil {
					// Skip individual broken manifests rather than abort
					// the whole listing — the browser still has value
					// even with one bad entry.
					continue
				}
				mu.Lock()
				all = append(all, m)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)

	// We only surface a top-level error if EVERY directory failed —
	// otherwise we have partial results worth showing. The first error
	// is propagated; the rest are dropped.
	if len(all) == 0 {
		for err := range errs {
			if err != nil {
				c.mu.Lock()
				c.cached = true
				c.cacheErr = err
				c.mu.Unlock()
				return nil, err
			}
		}
	}

	c.mu.Lock()
	c.cache = all
	c.cacheErr = nil
	c.cached = true
	c.mu.Unlock()
	return all, nil
}

// listDir calls the GitHub Contents API for one of the top-level type
// directories. Returns a RateLimitError when the API responds 403 with
// X-RateLimit-Remaining: 0 so the UI can show a helpful reset time.
func (c *Client) listDir(ctx context.Context, dir string) ([]contentsEntry, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", c.apiBase, c.owner, c.repo, dir, c.ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sidekick/0.1 (+https://github.com/meloniteai/sidekick)")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, rateLimitErrorFromHeader(resp.Header)
	}
	if resp.StatusCode == http.StatusNotFound {
		// Catalog may legitimately omit a type directory (e.g. no
		// binary verifiers published yet). Treat as empty rather than
		// erroring out.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var entries []contentsEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse contents response: %w", err)
	}
	return entries, nil
}

// fetchManifest pulls manifest.yaml for a single verifier from the raw
// host and fills in the synthetic Slug + RawURL fields.
func (c *Client) fetchManifest(ctx context.Context, dir, slug string) (Manifest, error) {
	u := fmt.Sprintf("%s/%s/%s/%s/%s/%s/manifest.yaml", c.rawBase, c.owner, c.repo, c.ref, dir, slug)
	body, err := c.getRaw(ctx, u)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s/%s manifest: %w", dir, slug, err)
	}
	if m.Type == "" {
		// Trust the directory if the manifest forgot to declare type —
		// makes authoring less error-prone.
		m.Type = dir
	}
	if m.Name == "" {
		m.Name = slug
	}
	m.Slug = slug
	if m.Artefact != "" {
		m.RawURL = fmt.Sprintf("%s/%s/%s/%s/%s/%s/%s", c.rawBase, c.owner, c.repo, c.ref, dir, slug, m.Artefact)
	}
	return m, nil
}

// getRaw GETs an arbitrary URL. Used for the raw.githubusercontent.com
// hostname which doesn't need the API headers.
func (c *Client) getRaw(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sidekick/0.1 (+https://github.com/meloniteai/sidekick)")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// RateLimitError signals the unauthenticated 60/hr GitHub quota was hit.
// The UI prints ResetAt so users know when to retry.
type RateLimitError struct {
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	if e.ResetAt.IsZero() {
		return "github rate limit exceeded; try again later"
	}
	return fmt.Sprintf("github rate limit exceeded; resets at %s", e.ResetAt.Format(time.RFC1123))
}

// IsRateLimit reports whether err is a RateLimitError (or wraps one).
func IsRateLimit(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl)
}

func rateLimitErrorFromHeader(h http.Header) *RateLimitError {
	reset := h.Get("X-RateLimit-Reset")
	if reset == "" {
		return &RateLimitError{}
	}
	ts, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return &RateLimitError{}
	}
	return &RateLimitError{ResetAt: time.Unix(ts, 0)}
}
