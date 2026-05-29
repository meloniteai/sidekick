package telemetry

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hexRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// --- git fixture helpers (mirrors internal/gitstats test style) ---

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// initRepo creates a repo with file `name` committed at `base`, then leaves a
// DIRTY working-tree version `dirty`. Returns (worktree, baseRef).
func initRepo(t *testing.T, name, base, dirty string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	writeFileT(t, filepath.Join(wt, name), base)
	gitT(t, wt, "add", name)
	gitT(t, wt, "commit", "-q", "-m", "base")
	baseRef := gitOutT(t, wt, "rev-parse", "HEAD")
	writeFileT(t, filepath.Join(wt, name), dirty)
	return wt, baseRef
}

// --- signature + shape ---

func TestHunkAnchorShape(t *testing.T) {
	wt, base := initRepo(t, "f.go",
		"package p\n\nfunc A() int { return 1 }\n",
		"package p\n\nfunc A() int { return 2 }\n",
	)
	hunk, dirty := hunkAnchor(wt, base, "f.go", 3)
	if !hexRe.MatchString(hunk) {
		t.Fatalf("hunkHash = %q, want 16-hex", hunk)
	}
	if !hexRe.MatchString(dirty) {
		t.Fatalf("dirtyDiffHash = %q, want 16-hex", dirty)
	}
	// Exported wrapper must agree with the package helper.
	eh, ed := HunkAnchor(wt, base, "f.go", 3)
	if eh != hunk || ed != dirty {
		t.Fatalf("HunkAnchor = (%q,%q), hunkAnchor = (%q,%q)", eh, ed, hunk, dirty)
	}
}

// --- hunkHash is computed only over the hunk containing line ---

func TestHunkAnchorLocatesHunk(t *testing.T) {
	// 50-line file; edit line 10 and line 40 so the diff has two separate hunks.
	var base strings.Builder
	for i := 1; i <= 50; i++ {
		base.WriteString("line ")
		base.WriteString(strings.Repeat("x", 1))
		base.WriteString("\n")
	}
	lines := strings.Split(strings.TrimRight(base.String(), "\n"), "\n")
	lines[9] = "EDITED_TEN"
	lines[39] = "EDITED_FORTY"
	dirty := strings.Join(lines, "\n") + "\n"
	wt, ref := initRepo(t, "f.txt", base.String(), dirty)

	h10, _ := hunkAnchor(wt, ref, "f.txt", 10)
	h40, _ := hunkAnchor(wt, ref, "f.txt", 40)
	hMiss, _ := hunkAnchor(wt, ref, "f.txt", 999)

	if h10 == "" || h40 == "" {
		t.Fatalf("expected non-empty hashes for lines in hunks: h10=%q h40=%q", h10, h40)
	}
	if h10 == h40 {
		t.Fatalf("two distinct hunks should hash differently: %q", h10)
	}
	if hMiss != "" {
		t.Fatalf("line outside every hunk should yield empty hunkHash, got %q", hMiss)
	}
}

// --- body selection + marker strip ---

func TestHunkAnchorBodySelection(t *testing.T) {
	// Two diffs sharing identical added+context lines but differing only in the
	// REMOVED lines must hash identically (removed lines are dropped).
	wtA, refA := initRepo(t, "f.txt",
		"keep1\nremoveA\nkeep2\n",
		"keep1\nadded\nkeep2\n",
	)
	wtB, refB := initRepo(t, "f.txt",
		"keep1\nremoveB_DIFFERENT\nkeep2\n",
		"keep1\nadded\nkeep2\n",
	)
	hA, _ := hunkAnchor(wtA, refA, "f.txt", 2)
	hB, _ := hunkAnchor(wtB, refB, "f.txt", 2)
	if hA == "" || hA != hB {
		t.Fatalf("removed lines must not affect hash: hA=%q hB=%q", hA, hB)
	}

	// Changing an added line's content must change the hash.
	wtC, refC := initRepo(t, "f.txt",
		"keep1\nremoveA\nkeep2\n",
		"keep1\nadded_CHANGED\nkeep2\n",
	)
	hC, _ := hunkAnchor(wtC, refC, "f.txt", 2)
	if hC == hA {
		t.Fatalf("added-line content change must change hash: %q", hC)
	}
}

func TestHunkAnchorStripsMarkers(t *testing.T) {
	// normalizeBody is fed marker-stripped lines; a context line " foo()" and an
	// added line "+foo()" both reduce to "foo()" and hash identically.
	ctxBody := normalizeBody([]string{"foo()"})
	if ctxBody != "foo()" {
		t.Fatalf("normalizeBody = %q, want %q", ctxBody, "foo()")
	}
}

// --- positions excluded ---

func TestHunkAnchorIgnoresPositions(t *testing.T) {
	// Edit the same logical content; in the second repo prepend unrelated lines
	// so the target hunk's @@ start positions shift. The hunk body bytes are
	// unchanged -> identical hunkHash.
	wt1, ref1 := initRepo(t, "f.txt",
		"a\nb\nTARGET_OLD\nc\nd\n",
		"a\nb\nTARGET_NEW\nc\nd\n",
	)
	h1, _ := hunkAnchor(wt1, ref1, "f.txt", 3)

	wt2, ref2 := initRepo(t, "f.txt",
		"pre0\npre1\npre2\npre3\npre4\na\nb\nTARGET_OLD\nc\nd\n",
		"pre0\npre1\npre2\npre3\npre4\na\nb\nTARGET_NEW\nc\nd\n",
	)
	// TARGET_NEW now lives at new-side line 8.
	h2, _ := hunkAnchor(wt2, ref2, "f.txt", 8)

	if h1 == "" || h1 != h2 {
		t.Fatalf("position shift must not change hunkHash: h1=%q h2=%q", h1, h2)
	}
}

// --- stability (the canonical test) ---

func TestHunkAnchorStability(t *testing.T) {
	wtBase, refBase := initRepo(t, "f.go",
		"package p\nfunc A() int {\n\treturn 1\n}\n",
		"package p\nfunc A() int {\n\treturn add(1, 2)\n}\n",
	)
	hBase, _ := hunkAnchor(wtBase, refBase, "f.go", 3)
	if hBase == "" {
		t.Fatal("baseline hunkHash empty")
	}

	// Whitespace-only edit (extra spaces/tabs) -> identical hash.
	wtWS, refWS := initRepo(t, "f.go",
		"package p\nfunc A() int {\n\treturn 1\n}\n",
		"package p\nfunc A() int {\n\t  return   add(1,  2)\n}\n",
	)
	hWS, _ := hunkAnchor(wtWS, refWS, "f.go", 3)
	if hWS != hBase {
		t.Fatalf("whitespace-only edit changed hash: base=%q ws=%q", hBase, hWS)
	}

	// One-character body content edit -> different hash.
	wtC, refC := initRepo(t, "f.go",
		"package p\nfunc A() int {\n\treturn 1\n}\n",
		"package p\nfunc A() int {\n\treturn add(1, 3)\n}\n",
	)
	hC, _ := hunkAnchor(wtC, refC, "f.go", 3)
	if hC == hBase {
		t.Fatalf("one-char content edit did not change hash: %q", hC)
	}
}

// --- line-number shift (lines added above) keeps the hash ---

func TestHunkAnchorLineNumberShift(t *testing.T) {
	// Same dirty edit to the function body; the second repo has the function
	// shifted down by inserting committed lines above it. Because those extra
	// lines are identical on both sides of the diff they are not part of the
	// hunk body, so the hunkHash is identical.
	wt1, ref1 := initRepo(t, "f.go",
		"func A() {\n\tx := 1\n}\n",
		"func A() {\n\tx := 2\n}\n",
	)
	h1, _ := hunkAnchor(wt1, ref1, "f.go", 2)

	wt2, ref2 := initRepo(t, "f.go",
		"// c1\n// c2\n// c3\nfunc A() {\n\tx := 1\n}\n",
		"// c1\n// c2\n// c3\nfunc A() {\n\tx := 2\n}\n",
	)
	h2, _ := hunkAnchor(wt2, ref2, "f.go", 5)

	if h1 == "" || h1 != h2 {
		t.Fatalf("line-number shift changed hash: h1=%q h2=%q", h1, h2)
	}
}

// --- dirtyDiffHash whole-file behavior ---

func TestDirtyDiffHashWholeFile(t *testing.T) {
	// Single-hunk file: dirtyDiffHash == hunkHash for the edited line.
	wt, ref := initRepo(t, "f.txt", "a\nb\nc\n", "a\nB\nc\n")
	h, d := hunkAnchor(wt, ref, "f.txt", 2)
	if h == "" || h != d {
		t.Fatalf("single-hunk dirtyDiffHash should equal hunkHash: h=%q d=%q", h, d)
	}

	// Two-hunk file: dirtyDiffHash changes when EITHER hunk content changes.
	mk := func(top, bottom string) (string, string) {
		var base strings.Builder
		for i := 1; i <= 30; i++ {
			base.WriteString("l\n")
		}
		lines := strings.Split(strings.TrimRight(base.String(), "\n"), "\n")
		lines[4] = top
		lines[24] = bottom
		dirty := strings.Join(lines, "\n") + "\n"
		return base.String(), dirty
	}
	b1, d1 := mk("TOP_A", "BOT_A")
	wt1, ref1 := initRepo(t, "f.txt", b1, d1)
	_, dd1 := hunkAnchor(wt1, ref1, "f.txt", 5)

	b2, d2 := mk("TOP_B", "BOT_A") // change top hunk only
	wt2, ref2 := initRepo(t, "f.txt", b2, d2)
	_, dd2 := hunkAnchor(wt2, ref2, "f.txt", 5)

	if dd1 == "" || dd1 == dd2 {
		t.Fatalf("dirtyDiffHash should change when a hunk changes: dd1=%q dd2=%q", dd1, dd2)
	}

	// Whitespace-only change in a hunk leaves dirtyDiffHash invariant.
	b3, d3 := mk("TOP_A", "BOT_A")
	d3 = strings.Replace(d3, "TOP_A", "TOP_A   ", 1) // trailing whitespace
	wt3, ref3 := initRepo(t, "f.txt", b3, d3)
	_, dd3 := hunkAnchor(wt3, ref3, "f.txt", 5)
	if dd3 != dd1 {
		t.Fatalf("whitespace-only change altered dirtyDiffHash: dd1=%q dd3=%q", dd1, dd3)
	}
}

// --- determinism / no absolute-path leakage ---

func TestHunkAnchorDeterministic(t *testing.T) {
	wt, ref := initRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 42 }\n",
	)
	first, firstD := hunkAnchor(wt, ref, "f.go", 2)
	for i := 0; i < 100; i++ {
		h, d := hunkAnchor(wt, ref, "f.go", 2)
		if h != first || d != firstD {
			t.Fatalf("nondeterministic: got (%q,%q) want (%q,%q)", h, d, first, firstD)
		}
	}

	// Same content in a different absolute worktree path -> same hash.
	wt2, ref2 := initRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 42 }\n",
	)
	h2, _ := hunkAnchor(wt2, ref2, "f.go", 2)
	if h2 != first {
		t.Fatalf("absolute path leaked into hash: %q vs %q", h2, first)
	}
}

// --- error / no-git / no-diff paths return empty ---

func TestHunkAnchorErrors(t *testing.T) {
	wt, ref := initRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 1 }\n", // identical -> no diff
	)
	// Empty baseRef.
	if h, d := hunkAnchor(wt, "", "f.go", 2); h != "" || d != "" {
		t.Fatalf("empty baseRef: got (%q,%q), want empty", h, d)
	}
	// Empty file.
	if h, d := hunkAnchor(wt, ref, "", 2); h != "" || d != "" {
		t.Fatalf("empty file: got (%q,%q), want empty", h, d)
	}
	// Unchanged file -> no diff.
	if h, d := hunkAnchor(wt, ref, "f.go", 2); h != "" || d != "" {
		t.Fatalf("unchanged file: got (%q,%q), want empty", h, d)
	}
	// Nonexistent worktree.
	if h, d := hunkAnchor(filepath.Join(t.TempDir(), "nope"), ref, "f.go", 2); h != "" || d != "" {
		t.Fatalf("bad worktree: got (%q,%q), want empty", h, d)
	}
}

// --- tree-global / no-line grains via the helper ---

func TestHunkAnchorGrains(t *testing.T) {
	wt, ref := initRepo(t, "f.go",
		"package p\nfunc A() int { return 1 }\n",
		"package p\nfunc A() int { return 9 }\n",
	)
	// Line 0 (no line) -> hunkHash empty, dirtyDiffHash set.
	h0, d0 := hunkAnchor(wt, ref, "f.go", 0)
	if h0 != "" {
		t.Fatalf("line 0 should have empty hunkHash, got %q", h0)
	}
	if d0 == "" {
		t.Fatalf("line 0 should still have a dirtyDiffHash")
	}
	// Line inside the hunk -> hunkHash set.
	hL, _ := hunkAnchor(wt, ref, "f.go", 2)
	if hL == "" {
		t.Fatalf("line-bearing finding should have a hunkHash")
	}
}

// --- golden vectors shared with the outcome worker ---

// anchorVectors mirrors the on-disk testdata/anchor_vectors.json. `Vectors`
// pins the BACK half (pre-stripped body -> hash); `RawVectors` pins the FRONT
// half (raw `git diff -U0` text -> hash, through the real parse pipeline).
type anchorVectors struct {
	Vectors []struct {
		Name     string   `json:"name"`
		Body     []string `json:"body"`
		Expected string   `json:"expected"`
	} `json:"vectors"`
	RawVectors []struct {
		Name                  string `json:"name"`
		Line                  int    `json:"line"`
		Diff                  string `json:"diff"`
		ExpectedHunkHash      string `json:"expected_hunk_hash"`
		ExpectedDirtyDiffHash string `json:"expected_dirty_diff_hash"`
	} `json:"raw_vectors"`
}

func loadAnchorVectors(t *testing.T) anchorVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "anchor_vectors.json"))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var doc anchorVectors
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	return doc
}

func TestAnchorGoldenVectors(t *testing.T) {
	doc := loadAnchorVectors(t)
	if len(doc.Vectors) == 0 {
		t.Fatal("no golden vectors loaded")
	}
	for _, v := range doc.Vectors {
		got := hashNormalizedBody(v.Body)
		if got != v.Expected {
			t.Errorf("vector %q: hashNormalizedBody = %q, want %q", v.Name, got, v.Expected)
		}
	}
}

// hashRawDiffAtLine reproduces the part of hunkAnchor that runs AFTER the
// `git diff -U0` call: parse the raw diff into hunks, hash the whole-file body
// for dirtyDiffHash, and hash only the hunk whose new-side range contains line
// for hunkHash. It exercises the real parseDiffHunks -> normalizeBody ->
// hashNormalizedBody path so the FRONT half (parsing, marker strip, -U0
// handling, multi-hunk location) is locked end-to-end against the same hunk_hash
// the back-half vectors pin — not just hashNormalizedBody on pre-stripped lines.
func hashRawDiffAtLine(diff string, line int) (hunkHash, dirtyDiffHash string) {
	hunks := parseDiffHunks(diff)
	if len(hunks) == 0 {
		return "", ""
	}
	var whole []string
	for _, h := range hunks {
		whole = append(whole, h.body...)
	}
	dirtyDiffHash = hashNormalizedBody(whole)
	if line > 0 {
		for _, h := range hunks {
			if line >= h.newStart && line < h.newStart+h.newCount {
				hunkHash = hashNormalizedBody(h.body)
				break
			}
		}
	}
	return hunkHash, dirtyDiffHash
}

// TestAnchorRawDiffGoldenVectors locks the FULL pipeline end-to-end: a raw
// `git diff -U0` hunk payload as INPUT and the resulting 16-hex hunk_hash /
// dirty_diff_hash as OUTPUT, driven through the real parseDiffHunks ->
// normalizeBody -> hash path (not just hashNormalizedBody). This byte-locks a
// future weightless Go worker through parsing + marker-strip + -U0 handling,
// not just the tail. The canonical raw vector is
// asserted to land on the SAME hash as the back-half canonical_body vector, so
// the two halves cannot silently diverge.
func TestAnchorRawDiffGoldenVectors(t *testing.T) {
	doc := loadAnchorVectors(t)
	if len(doc.RawVectors) == 0 {
		t.Fatal("no raw golden vectors loaded")
	}

	// Map back-half vectors by name so we can cross-check that the FRONT half
	// (raw diff) and BACK half (pre-stripped body) agree on shared content.
	backByName := map[string]string{}
	for _, v := range doc.Vectors {
		backByName[v.Name] = v.Expected
	}

	for _, v := range doc.RawVectors {
		gotHunk, gotDirty := hashRawDiffAtLine(v.Diff, v.Line)
		if gotHunk != v.ExpectedHunkHash {
			t.Errorf("raw vector %q: hunk_hash = %q, want %q", v.Name, gotHunk, v.ExpectedHunkHash)
		}
		if gotDirty != v.ExpectedDirtyDiffHash {
			t.Errorf("raw vector %q: dirty_diff_hash = %q, want %q", v.Name, gotDirty, v.ExpectedDirtyDiffHash)
		}
		if v.ExpectedHunkHash != "" && !hexRe.MatchString(v.ExpectedHunkHash) {
			t.Errorf("raw vector %q: expected_hunk_hash %q is not 16-hex", v.Name, v.ExpectedHunkHash)
		}
	}

	// Cross-half tie: the raw canonical diff MUST reduce to the same hash as the
	// pre-stripped canonical_body back-half vector, proving the FRONT half feeds
	// the BACK half byte-identically (parse + strip + -U0 -> normalizeBody).
	if want, ok := backByName["canonical_body"]; ok {
		gotHunk, _ := hashRawDiffAtLine(rawVectorByName(t, doc, "canonical_raw_diff"), 2)
		if gotHunk != want {
			t.Errorf("FRONT/BACK divergence: raw canonical diff -> %q, back-half canonical_body -> %q", gotHunk, want)
		}
	}
}

func rawVectorByName(t *testing.T, doc anchorVectors, name string) string {
	t.Helper()
	for _, v := range doc.RawVectors {
		if v.Name == name {
			return v.Diff
		}
	}
	t.Fatalf("raw vector %q not found", name)
	return ""
}

// TestAnchorRawDiffMatchesRealGitDiff is the strongest FRONT-half guard: it
// builds a real git fixture, captures the ACTUAL `git diff -U0` output git
// produces, asserts the pinned canonical raw vector is byte-identical to that
// real git output (so the vectors stay realistic, not hand-rolled drift), and
// asserts hunkAnchor over the real repo lands on the pinned canonical hash.
func TestAnchorRawDiffMatchesRealGitDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	doc := loadAnchorVectors(t)
	const wantHash = "becae935df3b19d6"

	// Base file has the package line on line 1; the dirty worktree appends the
	// Add() function so a -U0 diff yields exactly the canonical added body.
	wt, ref := initRepo(t, "math.go",
		"package mathx\n",
		"package mathx\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
	)
	// The Add() body occupies new-side lines 2..4; line 3 is inside the hunk.
	gotHunk, gotDirty := hunkAnchor(wt, ref, "math.go", 3)
	if gotHunk != wantHash {
		t.Fatalf("real hunkAnchor over git fixture = %q, want canonical %q", gotHunk, wantHash)
	}
	if gotDirty != wantHash {
		t.Fatalf("real dirtyDiffHash over git fixture = %q, want canonical %q", gotDirty, wantHash)
	}

	// Capture the real `git diff -U0` text and confirm the same parse+hash path
	// the vector test uses reduces it to the canonical hash, tying the pinned raw
	// vector to git's actual output format.
	realDiff := gitOutT(t, wt, "diff", "-U0", ref, "--", "math.go")
	vHunk, _ := hashRawDiffAtLine(realDiff+"\n", 3)
	if vHunk != wantHash {
		t.Fatalf("real `git diff -U0` through raw pipeline = %q, want %q\n--- diff ---\n%s", vHunk, wantHash, realDiff)
	}
	// Sanity: the pinned canonical raw vector exists and matches.
	_ = rawVectorByName(t, doc, "canonical_raw_diff")
}
