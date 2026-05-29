package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/meloniteai/sidekick/internal/gitstats"
)

// Stable hunk anchors join a session-time judgment (made against a dirty
// worktree) to the eventual committed PR hunk, without capturing a commit SHA.
// We hash *normalized hunk content* — never positions — so the anchor survives
// the dirty→commit transition and a later byte-for-byte re-hash of the PR hunk
// (in the Go outcome worker) lands on the same value.
//
// The normalization contract is documented once in
// docs/anchor-hunk-normalization.md and MUST stay byte-identical on both sides;
// the golden vectors in testdata/anchor_vectors.json pin it so any drift fails
// a test in both repos.

// HunkAnchor is the exported entry point callers (the verifier finalize path)
// use to anchor a finding. It delegates to the package-internal hunkAnchor so
// the contract signature `func hunkAnchor(...)` lives in this package for tests
// and the documented spec.
func HunkAnchor(worktree, baseRef, file string, line int) (hunkHash, dirtyDiffHash string) {
	return hunkAnchor(worktree, baseRef, file, line)
}

// hunkAnchor computes the two content-addressed anchors for a finding at
// (file, line):
//
//   - hunkHash: the normalized content of the single diff hunk whose NEW-side
//     range contains `line`. Empty when line falls in no hunk (the caller may
//     fall back to dirtyDiffHash).
//   - dirtyDiffHash: the normalized content of the WHOLE-file diff (all hunks
//     concatenated), independent of `line` — a coarse anchor for tree-global /
//     no-line findings.
//
// Both are sha256(normalizedBody) truncated to 16 lowercase hex chars. On any
// error (no git, invalid worktree, empty baseRef, untracked/unchanged file, or
// a diff timeout) both are "" — never a panic, mirroring gitstats' empty-on-
// error contract so finalize can record the finding with empty anchors.
func hunkAnchor(worktree, baseRef, file string, line int) (hunkHash, dirtyDiffHash string) {
	hunks := fileHunks(worktree, baseRef, file)
	if len(hunks) == 0 {
		return "", ""
	}

	// dirtyDiffHash: normalize the concatenation of every hunk's body
	// (line-independent whole-file anchor).
	var whole []string
	for _, h := range hunks {
		whole = append(whole, h.body...)
	}
	dirtyDiffHash = hashNormalizedBody(whole)

	// hunkHash: only the hunk whose new-side range contains line. A non-positive
	// line (tree-global / no line) resolves to no hunk, leaving hunkHash empty.
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

// FileHunkHashes returns the per-hunk anchor for EVERY changed hunk of file
// (vs baseRef in worktree), in diff order and de-duplicated. Each value is the
// same 16-hex hash hunkAnchor computes for a line that lands in that hunk, so a
// line-unset / file-level finding can be bound at hunk granularity later. Empty
// slice on any error or no diff, mirroring hunkAnchor's empty-on-error contract.
func FileHunkHashes(worktree, baseRef, file string) []string {
	hunks := fileHunks(worktree, baseRef, file)
	if len(hunks) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]struct{}, len(hunks))
	for _, h := range hunks {
		hash := hashNormalizedBody(h.body)
		if hash == "" {
			continue
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		out = append(out, hash)
	}
	return out
}

// fileHunks runs the documented `-U0` diff and parses it into hunks. Shared by
// hunkAnchor and FileHunkHashes so both compute per-hunk bodies identically.
// Empty slice on empty baseRef/file, no diff, or any git failure.
func fileHunks(worktree, baseRef, file string) []diffHunk {
	baseRef = strings.TrimSpace(baseRef)
	file = strings.TrimSpace(file)
	if baseRef == "" || file == "" {
		return nil
	}
	// `-U0` emits zero context lines so only the actually changed (+/-) lines
	// form the hunk body. This is what makes the anchor position-invariant: with
	// default context, lines surrounding the change leak into the hunk body and
	// a shift in nearby unrelated lines would change the hash. With -U0 the body
	// is exactly the added content (removed lines are dropped during parsing),
	// independent of where in the file the hunk sits. `--` disambiguates the
	// pathspec from a ref of the same name. No-diff and any git failure both
	// surface as "" from RunGit.
	out := gitstats.RunGit(context.Background(), worktree, "diff", "-U0", baseRef, "--", file)
	if out == "" {
		return nil
	}
	return parseDiffHunks(out)
}

// diffHunk is one `@@ -a,b +c,d @@` section of a unified diff. body holds the
// added ('+') and context (' ') lines with their single leading marker char
// already stripped; removed ('-') lines are dropped during parsing and never
// contribute to the hash. newStart/newCount describe the hunk's new-side line
// range so a finding line can be located, but the positions themselves are
// excluded from the hashed bytes.
type diffHunk struct {
	newStart int
	newCount int
	body     []string
}

// parseDiffHunks splits a `git diff` payload into hunks. It tolerates the diff
// header (diff/index/---/+++ lines before the first @@) and "\ No newline at
// end of file" markers, neither of which contribute to a hunk body.
func parseDiffHunks(diff string) []diffHunk {
	var hunks []diffHunk
	var cur *diffHunk
	for _, ln := range strings.Split(diff, "\n") {
		if strings.HasPrefix(ln, "@@") {
			start, count, ok := parseHunkHeader(ln)
			if !ok {
				cur = nil
				continue
			}
			hunks = append(hunks, diffHunk{newStart: start, newCount: count})
			cur = &hunks[len(hunks)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "+"):
			// Added line: keep content with the leading marker stripped.
			cur.body = append(cur.body, ln[1:])
		case strings.HasPrefix(ln, " "):
			// Context line: keep content with the leading marker stripped.
			cur.body = append(cur.body, ln[1:])
		case strings.HasPrefix(ln, "-"):
			// Removed line: excluded from the body entirely.
		default:
			// "\ No newline at end of file" and any stray line: ignore.
		}
	}
	return hunks
}

// parseHunkHeader extracts the new-side start line and length from a unified
// diff header `@@ -a,b +c,d @@ optional`. The +c,d run is the new file; ",d" is
// omitted by git when the length is 1. Only positions are read here — they are
// used to LOCATE the hunk, never hashed (per the normalization contract).
func parseHunkHeader(line string) (start, count int, ok bool) {
	// Header form: @@ -<old> +<new> @@ ...
	rest := strings.TrimPrefix(line, "@@")
	idx := strings.Index(rest, "+")
	if idx < 0 {
		return 0, 0, false
	}
	rest = rest[idx+1:] // after the '+'
	// New segment ends at the next space (before the closing @@).
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	startStr := rest
	countStr := "1"
	if comma := strings.IndexByte(rest, ','); comma >= 0 {
		startStr = rest[:comma]
		countStr = rest[comma+1:]
	}
	start = atoiOrNeg(startStr)
	count = atoiOrNeg(countStr)
	if start < 0 || count < 0 {
		return 0, 0, false
	}
	return start, count, true
}

// atoiOrNeg parses a non-negative integer, returning -1 on any malformed input
// so the caller can reject a bad @@ header without importing strconv error
// handling at every call site.
func atoiOrNeg(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// hashNormalizedBody applies the documented normalization and returns the
// 16-hex anchor. An empty body (no added/context lines) hashes to "" rather
// than a hash of zero bytes, so "no anchorable content" is distinguishable.
func hashNormalizedBody(body []string) string {
	norm := normalizeBody(body)
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:16]
}

// normalizeBody collapses whitespace so the anchor is insensitive to
// reformatting but sensitive to content. The pipeline, per body line:
//
//  1. collapse every run of whitespace (spaces and tabs) to a single space;
//  2. trim leading and trailing whitespace.
//
// Lines that are empty after trimming are dropped so a blank-line insertion is
// not a content change. Surviving lines are joined with '\n'. The '@@' header
// and all positions are already excluded (they never enter `body`), and diff
// markers were stripped during parsing — so only normalized content is hashed.
func normalizeBody(body []string) string {
	out := make([]string, 0, len(body))
	for _, ln := range body {
		ln = collapseWhitespace(ln)
		if ln == "" {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// collapseWhitespace replaces every maximal run of space/tab/CR characters with
// a single space and trims the ends. This is the one whitespace rule both Go
// sides must share; see docs/anchor-hunk-normalization.md.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteByte(c)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
