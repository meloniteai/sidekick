package verifier

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var verRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// writeSkill writes a SKILL.md with `body` and returns its path. `frontmatter`
// (the text between the leading --- delimiters) is optional.
func writeSkill(t *testing.T, frontmatter, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "SKILL.md")
	content := body
	if frontmatter != "" {
		content = "---\n" + frontmatter + "\n---\n" + body
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func agentVerifier(skill string) Verifier {
	return Verifier{
		Name:      "A",
		Type:      TypeAgent,
		Direction: "N",
		Agent:     AgentConfig{Agent: "claude", Skill: skill},
	}
}

// --- skill BODY not PATH drives the version ---

func TestVerifierVersionSkillBody(t *testing.T) {
	body := "judge whether the change is in scope\n"
	p1 := writeSkill(t, "", body)
	p2 := writeSkill(t, "", body) // identical body, different path
	v1 := agentVerifier(p1)
	v2 := agentVerifier(p2)
	if verifierVersion(v1) != verifierVersion(v2) {
		t.Fatalf("identical body at different paths must share version: %q vs %q",
			verifierVersion(v1), verifierVersion(v2))
	}
	// One-byte body change -> different version.
	p3 := writeSkill(t, "", body+"x")
	v3 := agentVerifier(p3)
	if verifierVersion(v3) == verifierVersion(v1) {
		t.Fatalf("body content change did not change version: %q", verifierVersion(v3))
	}
}

// --- direction folded in, case-normalized ---

func TestVerifierVersionDirection(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	vN := agentVerifier(p)
	vNE := agentVerifier(p)
	vNE.Direction = "NE"
	if verifierVersion(vN) == verifierVersion(vNE) {
		t.Fatal("direction N vs NE must change the version")
	}
	vLower := agentVerifier(p)
	vLower.Direction = "n"
	if verifierVersion(vLower) != verifierVersion(vN) {
		t.Fatalf("direction case must normalize: %q vs %q", verifierVersion(vLower), verifierVersion(vN))
	}
}

// --- agent, model, thinking each change the version ---

func TestVerifierVersionAgentModelThinking(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	base := agentVerifier(p)
	baseV := verifierVersion(base)

	cases := []struct {
		name   string
		mutate func(*Verifier)
	}{
		{"agent", func(v *Verifier) { v.Agent.Agent = "codex" }},
		{"model", func(v *Verifier) { v.Agent.Model = "sonnet" }},
		{"thinking", func(v *Verifier) { v.Agent.Thinking = "high" }},
	}
	for _, c := range cases {
		v := agentVerifier(p)
		c.mutate(&v)
		if verifierVersion(v) == baseV {
			t.Errorf("mutating %s did not change the version", c.name)
		}
	}
}

// --- exactly the documented inputs; excluded fields don't bleed in ---

func TestVerifierVersionInputs(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	base := agentVerifier(p)
	baseV := verifierVersion(base)

	// Excluded fields MUST NOT change the version.
	excluded := []struct {
		name   string
		mutate func(*Verifier)
	}{
		{"timeout", func(v *Verifier) { v.Timeout = 99 }},
		{"permissions.env", func(v *Verifier) { v.Permissions.Env = []string{"FOO=1"} }},
		{"permissions.network", func(v *Verifier) { v.Permissions.Network = true }},
	}
	for _, c := range excluded {
		v := agentVerifier(p)
		c.mutate(&v)
		if verifierVersion(v) != baseV {
			t.Errorf("excluded field %s changed the version", c.name)
		}
	}
}

// --- output shape ---

func TestVerifierVersionShape(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	for _, v := range []Verifier{
		agentVerifier(p),
		{Name: "C", Type: TypeCommand, Direction: "S", Command: []string{"go", "vet"}},
		{Name: "B", Type: TypeBinary, Direction: "E", Binary: BinaryConfig{Command: []string{"lint"}}},
	} {
		got := verifierVersion(v)
		if !verRe.MatchString(got) {
			t.Errorf("verifierVersion(%s) = %q, want 12-hex", v.Name, got)
		}
	}
}

// --- determinism ---

func TestVerifierVersionDeterministic(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	v := agentVerifier(p)
	first := verifierVersion(v)
	for i := 0; i < 20; i++ {
		if got := verifierVersion(v); got != first {
			t.Fatalf("nondeterministic version: %q vs %q", got, first)
		}
	}
}

// --- pinned golden value (catches input drift) ---

func TestVerifierVersionGolden(t *testing.T) {
	p := writeSkill(t, "", "the rubric body\n")
	v := agentVerifier(p) // agent=claude, model="", thinking="", direction=N
	const want = "250cc2bbc362"
	if got := verifierVersion(v); got != want {
		t.Fatalf("golden version drifted: got %q want %q (update docs + changelog if intentional)", got, want)
	}
}

// --- missing skill -> "" and an error, no panic ---

func TestVerifierVersionMissingSkill(t *testing.T) {
	v := agentVerifier(filepath.Join(t.TempDir(), "does-not-exist.md"))
	got, err := verifierVersionErr(v)
	if got != "" {
		t.Fatalf("missing skill should yield empty version, got %q", got)
	}
	if err == nil {
		t.Fatal("missing skill should surface an error for logging")
	}
	// Fail-safe wrapper swallows the error.
	if verifierVersion(v) != "" {
		t.Fatal("verifierVersion should be empty on missing skill")
	}
}

// --- command/binary verifiers, no skill read, stable + command-sensitive ---

func TestVerifierVersionNonAgent(t *testing.T) {
	bin := Verifier{Name: "B", Type: TypeBinary, Direction: "E", Binary: BinaryConfig{Command: []string{"lint", "--strict"}}}
	v1, err := verifierVersionErr(bin)
	if err != nil {
		t.Fatalf("binary verifier must not error (no skill read): %v", err)
	}
	if !verRe.MatchString(v1) {
		t.Fatalf("binary version shape: %q", v1)
	}
	for i := 0; i < 10; i++ {
		if verifierVersion(bin) != v1 {
			t.Fatal("binary version not stable across calls")
		}
	}
	bin2 := bin
	bin2.Binary = BinaryConfig{Command: []string{"lint", "--lax"}}
	if verifierVersion(bin2) == v1 {
		t.Fatal("changing the binary command must change the version")
	}
}

// --- frontmatter-only edit leaves the body (and version) unchanged ---

func TestVerifierVersionFrontmatterIgnored(t *testing.T) {
	body := "the actual rubric\n"
	p1 := writeSkill(t, "name: A\nversion: 1", body)
	p2 := writeSkill(t, "name: A\nversion: 2\nextra: changed", body)
	if verifierVersion(agentVerifier(p1)) != verifierVersion(agentVerifier(p2)) {
		t.Fatal("frontmatter-only edit must not change the version")
	}
	// A body edit DOES change it.
	p3 := writeSkill(t, "name: A\nversion: 1", body+"another sentence.\n")
	if verifierVersion(agentVerifier(p3)) == verifierVersion(agentVerifier(p1)) {
		t.Fatal("body edit must change the version")
	}
}

// --- remote artifact SHA256 binds to a distinct version ---

func TestVerifierVersionRemote(t *testing.T) {
	p := writeSkill(t, "", "body\n")
	v := agentVerifier(p)
	v.Source = "remote"
	v.SourceURL = "https://example.test/skill.md"
	v.SHA256 = "aaaa"
	vA := verifierVersion(v)
	v.SHA256 = "bbbb"
	vB := verifierVersion(v)
	if vA == vB {
		t.Fatal("changing the remote SHA256 must change the version")
	}
}
