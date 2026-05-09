package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "hud.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveValid(t *testing.T) {
	p := writeTemp(t, `
goal_source: prompt
verifiers:
  - name: Architect
    direction: n
    command: ["./bin/architect"]
    timeout: 30s
  - name: Test
    direction: E
    command: ["echo", "hi"]
`)
	f, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := f.Resolve(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2, got %d", len(vs))
	}
	// direction should be uppercased
	if vs[0].Direction != "N" {
		t.Errorf("direction not uppercased: %q", vs[0].Direction)
	}
	// relative ./bin/architect should be joined to configDir
	if !filepath.IsAbs(vs[0].Command[0]) {
		t.Errorf("relative command not resolved: %q", vs[0].Command[0])
	}
	if vs[0].Timeout.Seconds() != 30 {
		t.Errorf("timeout not parsed: %v", vs[0].Timeout)
	}
}

func TestResolveDuplicateName(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - {name: A, direction: N, command: ["x"]}
  - {name: A, direction: S, command: ["y"]}`)
	f, _, _ := Load(p)
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestResolveBadDirection(t *testing.T) {
	p := writeTemp(t, `verifiers:
  - {name: A, direction: NORTH, command: ["x"]}`)
	f, _, _ := Load(p)
	if _, err := f.Resolve(filepath.Dir(p)); err == nil {
		t.Fatal("expected bad direction error")
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Load(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("expected missing file error")
	}
}
