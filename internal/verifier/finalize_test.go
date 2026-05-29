package verifier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/meloniteai/sidekick/internal/daemon"
	"github.com/meloniteai/sidekick/internal/ipc"
	"github.com/meloniteai/sidekick/internal/telemetry"
)

// captureEmitter records the run + findings handed to it so the finalize wiring
// (anchor + verifier_version stamping) can be asserted.
type captureEmitter struct {
	runs     []telemetry.VerifierRunRecord
	findings []telemetry.FindingRecord
}

func (c *captureEmitter) RecordSession(telemetry.SessionRecord) error { return nil }
func (c *captureEmitter) RecordEdit(telemetry.EditRecord) error       { return nil }
func (c *captureEmitter) RecordBatch(telemetry.BatchRecord) error     { return nil }
func (c *captureEmitter) RecordVerifierRun(r telemetry.VerifierRunRecord) (int64, error) {
	c.runs = append(c.runs, r)
	return int64(len(c.runs)), nil
}
func (c *captureEmitter) RecordFindings(_ int64, fs []telemetry.FindingRecord) error {
	c.findings = append(c.findings, fs...)
	return nil
}
func (c *captureEmitter) RecordHeartbeat(telemetry.HeartbeatRecord) error { return nil }
func (c *captureEmitter) Close() error                                    { return nil }

var anchorHexRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// dirtyGitRepo creates a committed file and a dirty edit; returns (worktree, baseRef).
func dirtyGitRepo(t *testing.T, name, base, dirty string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wt := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(wt, name), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", name)
	run("commit", "-q", "-m", "base")
	out, err := exec.Command("git", "-C", wt, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, name), []byte(dirty), 0o600); err != nil {
		t.Fatal(err)
	}
	return wt, strings.TrimSpace(string(out))
}

func setupRunner(t *testing.T, worktree, baseRef string) (*Runner, *captureEmitter, *daemon.State) {
	t.Helper()
	state := daemon.NewState()
	cap := &captureEmitter{}
	state.SetEmitter(cap)
	state.SetSessionWorktree(worktree)
	state.SetSessionBaseRef(baseRef)
	state.StartTelemetrySession("goal") // mints telemetrySessionID
	r := NewRunner(context.Background(), state, nil)
	return r, cap, state
}

// finalize stamps a non-empty HunkHash on a line-bearing finding and a
// verifier_version on the run.
func TestFinalizeStampsAnchorAndVersion(t *testing.T) {
	wt, ref := dirtyGitRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 2 }\n",
	)
	r, cap, _ := setupRunner(t, wt, ref)

	p := writeSkill(t, "", "rubric body\n")
	v := agentVerifier(p)
	cur := ipc.VerifierStatus{Distance: 0.5, Reason: "x", Status: ipc.StatusOK}
	findings := []Finding{{Path: "f.go", Line: 2, Distance: 0.5, Reason: "x"}}

	r.recordVerifierRun("b1", v, cur, 100, nil, findings)

	if len(cap.runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(cap.runs))
	}
	if got := cap.runs[0].VerifierVersion; !verRe.MatchString(got) {
		t.Fatalf("VerifierVersion = %q, want 12-hex", got)
	}
	if got, want := cap.runs[0].VerifierVersion, verifierVersion(v); got != want {
		t.Fatalf("VerifierVersion = %q, want %q", got, want)
	}
	if len(cap.findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(cap.findings))
	}
	if got := cap.findings[0].HunkHash; !anchorHexRe.MatchString(got) {
		t.Fatalf("HunkHash = %q, want non-empty 16-hex", got)
	}
	if cap.findings[0].DirtyDiffHash == "" {
		t.Fatalf("DirtyDiffHash should be set for a line finding")
	}
}

// tree-global finding (no path) -> both hashes empty;
// a path-bearing finding with Line==0 -> hunk empty, dirty set.
func TestTreeGlobalFindingHashes(t *testing.T) {
	wt, ref := dirtyGitRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 7 }\n",
	)
	r, cap, _ := setupRunner(t, wt, ref)
	v := agentVerifier(writeSkill(t, "", "rubric\n"))
	cur := ipc.VerifierStatus{Distance: 0.5, Status: ipc.StatusOK}

	findings := []Finding{
		{Path: "", Line: 0, Distance: 0.5},     // fully tree-global
		{Path: "f.go", Line: 0, Distance: 0.5}, // file but no line
	}
	r.recordVerifierRun("b1", v, cur, 1, nil, findings)

	if len(cap.findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(cap.findings))
	}
	global := cap.findings[0]
	if global.HunkHash != "" || global.DirtyDiffHash != "" {
		t.Fatalf("tree-global finding must have empty hashes: %+v", global)
	}
	fileNoLine := cap.findings[1]
	if fileNoLine.HunkHash != "" {
		t.Fatalf("file/no-line finding must have empty HunkHash, got %q", fileNoLine.HunkHash)
	}
	if fileNoLine.DirtyDiffHash == "" {
		t.Fatalf("file/no-line finding must carry a DirtyDiffHash")
	}
}

// A verifier_version computation failure (missing/unreadable skill)
// is fail-safe AND observable. recordVerifierRun must (1) still emit the run row
// with VerifierVersion=="" rather than panicking/aborting, and (2) surface the
// failure via state.LogEvent(daemon.EventError, ...) AT THE RECORDING SITE — not
// merely have verifierVersionErr return an error. We assert the emitted event by
// capturing it from the real daemon.State the runner logs into.
func TestRecordVerifierRunEmitsEventErrorOnMissingSkill(t *testing.T) {
	wt, ref := dirtyGitRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 2 }\n",
	)
	r, cap, state := setupRunner(t, wt, ref)

	// Agent verifier whose skill path does not exist -> skillBody read error ->
	// verifierVersionErr returns ("", err). Use a guaranteed-absent path.
	missing := filepath.Join(t.TempDir(), "does-not-exist-SKILL.md")
	v := agentVerifier(missing)
	if _, err := verifierVersionErr(v); err == nil {
		t.Fatal("precondition: missing skill should make verifierVersionErr return an error")
	}

	cur := ipc.VerifierStatus{Distance: 0.5, Reason: "x", Status: ipc.StatusOK}
	// Must not panic.
	r.recordVerifierRun("b1", v, cur, 100, nil, nil)

	// (1) The run is still recorded, with an empty (NULL-on-store) version.
	if len(cap.runs) != 1 {
		t.Fatalf("expected the run to still be recorded, got %d runs", len(cap.runs))
	}
	if got := cap.runs[0].VerifierVersion; got != "" {
		t.Fatalf("VerifierVersion should be empty on skill read failure, got %q", got)
	}

	// (2) An EventError was emitted at the recording site, mentioning the
	// verifier_version failure and the verifier name (so the un-versioned
	// judgment is observable, not silent).
	var found *daemon.EventEntry
	for i := range state.Events() {
		e := state.Events()[i]
		if e.Level == daemon.EventError && strings.Contains(e.Msg, "verifier_version") {
			ev := e
			found = &ev
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a daemon.EventError mentioning verifier_version, got events: %+v", state.Events())
	}
	if !strings.Contains(found.Msg, v.Name) {
		t.Errorf("EventError should name the verifier (%q): %q", v.Name, found.Msg)
	}
}

// verifier_version is stamped for every run kind/status, including
// agent/command/binary and error/unknown statuses.
func TestVerifierVersionStampedEveryRun(t *testing.T) {
	wt, ref := dirtyGitRepo(t, "f.go", "package p\n", "package p\nvar X = 1\n")
	r, cap, _ := setupRunner(t, wt, ref)

	skill := writeSkill(t, "", "rubric\n")
	verifiers := []Verifier{
		agentVerifier(skill),
		{Name: "cmd", Type: TypeCommand, Direction: "S", Command: []string{"true"}},
		{Name: "bin", Type: TypeBinary, Direction: "E", Binary: BinaryConfig{Command: []string{"lint"}}},
	}
	statuses := []ipc.VerifierStatus{
		{Distance: 0.5, Status: ipc.StatusOK},
		{Distance: 1.0, Status: ipc.StatusError, Reason: "boom"},
		{Distance: 0.3, Status: ipc.StatusUnknown},
	}
	for _, v := range verifiers {
		for _, st := range statuses {
			r.recordVerifierRun("b", v, st, 1, nil, nil)
		}
	}
	if len(cap.runs) != len(verifiers)*len(statuses) {
		t.Fatalf("expected %d runs, got %d", len(verifiers)*len(statuses), len(cap.runs))
	}
	for i, rec := range cap.runs {
		if !verRe.MatchString(rec.VerifierVersion) {
			t.Errorf("run %d (%s/%s): VerifierVersion=%q not 12-hex", i, rec.VerifierName, rec.Status, rec.VerifierVersion)
		}
	}
}
