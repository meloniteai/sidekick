package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubCatalog stands in for both the GitHub API and the raw host. The
// path prefix tells the handler which role to play:
//
//	/api/repos/...   → Contents-API endpoint, returns JSON directory listing
//	/raw/...         → raw.githubusercontent.com, returns file bytes
//
// Each role is keyed off the fixture map so individual tests can
// declare exactly which directories exist and what their manifests
// contain.
func stubCatalog(t *testing.T, dirs map[string][]string, manifests map[string]string, opts ...func(http.Header)) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		// /api/repos/{owner}/{repo}/contents/{dir} – strip the prefix
		// and pull the last path segment.
		path := strings.TrimPrefix(r.URL.Path, "/api/")
		parts := strings.Split(path, "/")
		// repos/<owner>/<repo>/contents/<dir>
		if len(parts) < 5 || parts[0] != "repos" || parts[3] != "contents" {
			http.NotFound(w, r)
			return
		}
		dir := parts[4]
		entries, ok := dirs[dir]
		for _, opt := range opts {
			opt(w.Header())
		}
		if w.Header().Get("X-RateLimit-Remaining") == "0" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body := "["
		for i, name := range entries {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"name":%q,"type":"dir"}`, name)
		}
		body += "]"
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		// /raw/{owner}/{repo}/{ref}/{dir}/{slug}/manifest.yaml
		path := strings.TrimPrefix(r.URL.Path, "/raw/")
		body, ok := manifests[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New("acme", "verifiers", "main").WithEndpoints(srv.URL+"/api", srv.URL+"/raw")
	c.httpc = srv.Client()
	return srv, c
}

func TestClientList_HappyPath(t *testing.T) {
	dirs := map[string][]string{
		"command": {"echo-ok"},
		"agent":   {"needs-tests"},
		"binary":  {},
	}
	manifests := map[string]string{
		"acme/verifiers/main/command/echo-ok/manifest.yaml": `name: echo-ok
type: command
direction: N
artefact: run.sh
sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`,
		"acme/verifiers/main/agent/needs-tests/manifest.yaml": `name: needs-tests
type: agent
direction: NE
description: ensures every changed file has tests
artefact: SKILL.md
sha256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
agent:
  agent: claude
  model: claude-sonnet-4-6
permissions:
  network: false
  filesystem: read-only
  allowed_tools:
    - "Bash(go test:*)"
`,
	}
	_, c := stubCatalog(t, dirs, manifests)
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d manifests, want 2: %+v", len(got), got)
	}
	// Order is non-deterministic because of parallel directory walk;
	// look up by slug.
	byName := map[string]Manifest{}
	for _, m := range got {
		byName[m.Slug] = m
	}
	if m, ok := byName["needs-tests"]; !ok {
		t.Fatal("missing needs-tests manifest")
	} else {
		if m.Type != "agent" {
			t.Errorf("type = %q, want agent", m.Type)
		}
		if !strings.HasSuffix(m.RawURL, "/agent/needs-tests/SKILL.md") {
			t.Errorf("RawURL = %q, want suffix /agent/needs-tests/SKILL.md", m.RawURL)
		}
		if len(m.Permissions.AllowedTools) != 1 || m.Permissions.AllowedTools[0] != "Bash(go test:*)" {
			t.Errorf("AllowedTools = %v, want [Bash(go test:*)]", m.Permissions.AllowedTools)
		}
	}
	if m, ok := byName["echo-ok"]; !ok {
		t.Fatal("missing echo-ok manifest")
	} else {
		if m.Type != "command" {
			t.Errorf("type = %q, want command", m.Type)
		}
	}
}

func TestClientList_SkipsBrokenManifest(t *testing.T) {
	dirs := map[string][]string{
		"command": {"good", "broken"},
	}
	manifests := map[string]string{
		"acme/verifiers/main/command/good/manifest.yaml": `name: good
type: command
artefact: run.sh
sha256: cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
`,
		// "broken" intentionally omitted → 404 from /raw → skipped.
	}
	_, c := stubCatalog(t, dirs, manifests)
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Slug != "good" {
		t.Fatalf("want only 'good' manifest, got %+v", got)
	}
}

func TestClientList_RateLimited(t *testing.T) {
	dirs := map[string][]string{}
	resetAt := time.Now().Add(45 * time.Minute).Unix()
	_, c := stubCatalog(t, dirs, nil, func(h http.Header) {
		h.Set("X-RateLimit-Remaining", "0")
		h.Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
	})
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if !IsRateLimit(err) {
		t.Fatalf("want RateLimitError, got %T: %v", err, err)
	}
}

func TestClientList_CachesResults(t *testing.T) {
	dirs := map[string][]string{"command": {"only"}}
	manifests := map[string]string{
		"acme/verifiers/main/command/only/manifest.yaml": `name: only
type: command
artefact: run.sh
sha256: dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
`,
	}
	srv, c := stubCatalog(t, dirs, manifests)

	first, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Replace the server's mux so a second uncached call would fail.
	srv.Config.Handler = http.NotFoundHandler()
	second, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("second List should hit cache, got err: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("cache mismatch: first=%d second=%d", len(first), len(second))
	}
}

// touch keeps net/url imported when other tests temporarily remove
// their reliance on it; cheap to keep but should not be flaky.
var _ = url.PathEscape
