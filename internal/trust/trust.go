// Package trust persists user approval of remote verifier artefacts.
//
// HUD's trust model for remote verifiers is two-step: hud.yaml pins each
// remote source by sha256 (so the bytes can't drift), and trust.json
// records that the user approved that sha256 ("I read it; I run it").
// The combination means a verifier loaded from the network has been
// explicitly endorsed by the user once, by hash, and any drift requires
// a fresh approval.
//
// Local verifiers (no `source:` block in hud.yaml) are implicitly
// trusted — the user wrote or checked them in. Trust applies only to
// content that came from outside the repo.
package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// File is the on-disk shape of trust.json. Versioned so we can migrate
// later without breaking existing files.
type File struct {
	Version  int              `json:"version"`
	Approved map[string]Entry `json:"approved"`
}

// Entry is one approval record. The map key is sha256 (lowercase hex).
type Entry struct {
	URL        string    `json:"url,omitempty"`
	Verifier   string    `json:"verifier,omitempty"`
	ApprovedAt time.Time `json:"approved_at,omitempty"`
}

// Path returns the trust.json location, $HOME/.hud/trust.json by default.
// Overridable via $HUD_TRUST_FILE for tests.
func Path() (string, error) {
	if p := os.Getenv("HUD_TRUST_FILE"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hud", "trust.json"), nil
}

// Store wraps an on-disk trust file with a mutex. Cheap to construct;
// Load is the explicit I/O step.
type Store struct {
	path string
	mu   sync.Mutex
	file File
}

// New returns a Store bound to the default path (or the path supplied).
// New does not read the file — call Load first.
func New(path string) (*Store, error) {
	if path == "" {
		p, err := Path()
		if err != nil {
			return nil, err
		}
		path = p
	}
	return &Store{path: path, file: File{Version: 1, Approved: map[string]Entry{}}}, nil
}

// Load reads trust.json from disk. A missing file is not an error; the
// store starts empty (first-time usage). A malformed file IS an error,
// because silently dropping prior approvals would let an attacker who
// can corrupt the file re-prompt the user under any name they choose.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Approved == nil {
		f.Approved = map[string]Entry{}
	}
	if f.Version == 0 {
		f.Version = 1
	}
	s.file = f
	return nil
}

// Save writes the store back to disk atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// IsApproved returns true when sha256 has a recorded entry. Lookup is
// case-insensitive on the hash so callers don't need to normalise.
func (s *Store) IsApproved(sha256 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.file.Approved[strings.ToLower(sha256)]
	return ok
}

// Approve records a new approval for sha256. Idempotent — re-approving
// the same hash refreshes the metadata but doesn't error. Caller must
// invoke Save to persist; we keep approval and persistence separate so
// `hud verifier add` can batch a set of approvals into a single write.
func (s *Store) Approve(sha256 string, e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ApprovedAt.IsZero() {
		e.ApprovedAt = time.Now().UTC()
	}
	s.file.Approved[strings.ToLower(sha256)] = e
}

// Revoke removes an approval. Useful for `hud verifier trust --revoke`
// (not yet wired) and for tests.
func (s *Store) Revoke(sha256 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(sha256)
	if _, ok := s.file.Approved[key]; !ok {
		return false
	}
	delete(s.file.Approved, key)
	return true
}

// Approvals returns a snapshot of the current set, useful for `hud
// verifier trust --list` (not yet wired).
func (s *Store) Approvals() map[string]Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Entry, len(s.file.Approved))
	for k, v := range s.file.Approved {
		out[k] = v
	}
	return out
}
