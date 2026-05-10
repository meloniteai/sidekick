package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TestResolveValidPin downloads a body whose sha256 matches the pin,
// caches it, and returns the cached path.
func TestResolveValidPin(t *testing.T) {
	body := []byte("# my skill\nbody.\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cache := t.TempDir()
	t.Setenv("HUD_CACHE_DIR", cache)

	got, err := Resolve(Pin{URL: srv.URL, SHA256: sha(body), Ext: ".md"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != cache {
		t.Fatalf("cached path %s not under %s", got, cache)
	}
	read, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != string(body) {
		t.Fatalf("cached body mismatch")
	}
}

// TestResolveSHAMismatch ensures Resolve rejects a body whose hash drifts
// from the pin. This is the entire trust model: drift must fail loud.
func TestResolveSHAMismatch(t *testing.T) {
	body := []byte("hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("HUD_CACHE_DIR", t.TempDir())

	wrong := strings.Repeat("0", 64)
	_, err := Resolve(Pin{URL: srv.URL, SHA256: wrong})
	if err == nil {
		t.Fatal("expected sha mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got: %v", err)
	}
}

// TestResolveSecondCallUsesCache verifies that a hit on the cache does
// not re-fetch the upstream URL. We register a counter on the test
// server and assert it stayed at 1.
func TestResolveSecondCallUsesCache(t *testing.T) {
	body := []byte("static body")
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("HUD_CACHE_DIR", t.TempDir())

	pin := Pin{URL: srv.URL, SHA256: sha(body)}
	if _, err := Resolve(pin); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(pin); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("expected one upstream fetch; got %d", hits)
	}
}

// TestResolveValidatesPin enforces that a remote artefact must always
// be pinned by sha256 — empty pins are a bug, not a fallback.
func TestResolveValidatesPin(t *testing.T) {
	cases := []struct {
		name string
		pin  Pin
	}{
		{"empty-url", Pin{SHA256: strings.Repeat("a", 64)}},
		{"empty-sha", Pin{URL: "https://example.com/x"}},
		{"bad-scheme", Pin{URL: "ftp://example.com/x", SHA256: strings.Repeat("a", 64)}},
		{"short-sha", Pin{URL: "https://example.com/x", SHA256: "deadbeef"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.pin); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
