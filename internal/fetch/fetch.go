// Package fetch retrieves remote verifier artefacts (SKILL.md files, custom
// command scripts) into a content-addressed local cache.
//
// Trust model: every remote artefact is pinned by sha256 in hud.yaml and the
// fetcher refuses to return content whose hash does not match the pin. This
// is the smallest possible "registry" — there is no central server, just
// HTTPS URLs the user has explicitly trusted by writing the pin into config.
//
// Cache layout: $HOME/.hud/cache/<sha256>[.<ext>]. Files are written
// atomically (tmp + rename) so concurrent fetches cannot observe a torn
// partial download. A successfully cached artefact is reused without
// re-downloading; cache misses fall back to HTTPS GET.
package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheDir returns the on-disk cache root, $HOME/.hud/cache by default.
// Overridable via $HUD_CACHE_DIR (mostly for tests).
func CacheDir() (string, error) {
	if p := os.Getenv("HUD_CACHE_DIR"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hud", "cache"), nil
}

// Pin describes a remote artefact: where to download it from and the
// expected sha256 hex digest. An empty SHA256 is rejected — every remote
// fetch must be content-addressed.
type Pin struct {
	URL    string
	SHA256 string
	Ext    string // optional file extension hint, e.g. ".md", ".sh" — only affects on-disk filename
}

// Resolve returns the local cache path for p. If the cached copy already
// exists and matches the pin, it is returned without network I/O. Otherwise
// the URL is fetched, hashed, verified against p.SHA256, and atomically
// installed into the cache.
//
// Returned errors carry actionable text (URL, expected/actual hash) so the
// failure mode is recoverable from a single log line.
func Resolve(p Pin) (string, error) {
	if err := validate(p); err != nil {
		return "", err
	}
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	cached := cachePath(dir, p)
	if got, ok := verifyExisting(cached, p.SHA256); ok {
		return cached, nil
	} else if got != "" {
		// File exists but hash mismatched — surface this loudly. It almost
		// certainly means the upstream content changed and the user needs
		// to re-pin (or that they updated the pin without invalidating the
		// cache). Either way, do not silently overwrite.
		return "", fmt.Errorf("cached %s sha256 mismatch: have %s, pinned %s — delete and re-fetch", cached, got, p.SHA256)
	}
	body, err := download(p.URL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", p.URL, err)
	}
	got := hashHex(body)
	if !strings.EqualFold(got, p.SHA256) {
		return "", fmt.Errorf("sha256 mismatch fetching %s: got %s, expected %s", p.URL, got, p.SHA256)
	}
	if err := writeAtomic(cached, body, 0o644); err != nil {
		return "", fmt.Errorf("write cache %s: %w", cached, err)
	}
	return cached, nil
}

// Hash hex-encodes sha256(body); used by `hud verifier add` after fetch.
func Hash(body []byte) string {
	return hashHex(body)
}

// Download fetches url and returns the body. Exposed for the `hud verifier
// add` flow which needs to inspect content before pinning. Resolve()
// callers should not use this directly — they should pin first.
func Download(url string) ([]byte, error) {
	return download(url)
}

func validate(p Pin) error {
	if p.URL == "" {
		return errors.New("fetch: empty URL")
	}
	if p.SHA256 == "" {
		return fmt.Errorf("fetch: %s requires sha256 pin", p.URL)
	}
	if !strings.HasPrefix(p.URL, "https://") && !strings.HasPrefix(p.URL, "http://") {
		return fmt.Errorf("fetch: %s must be http(s)", p.URL)
	}
	if len(p.SHA256) != 64 {
		return fmt.Errorf("fetch: sha256 %q must be 64 hex chars", p.SHA256)
	}
	return nil
}

func cachePath(dir string, p Pin) string {
	name := strings.ToLower(p.SHA256)
	if p.Ext != "" {
		name += p.Ext
	}
	return filepath.Join(dir, name)
}

func verifyExisting(path, want string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	got := hashHex(body)
	return got, strings.EqualFold(got, want)
}

func hashHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func download(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hud/0.1 (+https://github.com/uriahlevy/hud)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	const maxArtefactBytes = 4 << 20 // 4MiB cap; verifier scripts and rubrics are tiny
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtefactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxArtefactBytes {
		return nil, fmt.Errorf("artefact exceeds %d byte cap", maxArtefactBytes)
	}
	return body, nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
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
