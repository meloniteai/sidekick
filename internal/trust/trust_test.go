package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApproveAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")

	a, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Load(); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if a.IsApproved(hash) {
		t.Fatal("fresh store should not approve anything")
	}
	a.Approve(hash, Entry{URL: "https://x", Verifier: "Test"})
	if !a.IsApproved(hash) {
		t.Fatal("Approve did not register")
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload from disk in a fresh store and verify persistence + that
	// approval is case-insensitive on the hash.
	b, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Load(); err != nil {
		t.Fatal(err)
	}
	if !b.IsApproved(strings.ToUpper(hash)) {
		t.Fatal("approval did not persist or lookup is case-sensitive")
	}
	if !b.Revoke(hash) {
		t.Fatal("Revoke returned false for an approved hash")
	}
	if b.IsApproved(hash) {
		t.Fatal("Revoke did not remove approval")
	}
}

// TestLoadMalformedRefuses ensures a corrupt trust.json is loud, not
// silently dropped. Silent drops would let an attacker who can write to
// the file re-prompt the user on every approval they want to bypass.
func TestLoadMalformedRefuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(); err == nil {
		t.Fatal("expected error on malformed trust.json")
	}
}

// TestLoadMissingIsEmpty exercises the first-time-user path: no
// trust.json exists, Load returns nil error, and the store starts
// empty.
func TestLoadMissingIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("Load on missing file should be a no-op: %v", err)
	}
	if len(s.Approvals()) != 0 {
		t.Fatalf("expected empty store, got %d entries", len(s.Approvals()))
	}
}
