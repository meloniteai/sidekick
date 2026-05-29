# Anchor hunk normalization — the shared contract

> The CLI and sidekick-api implement the normalization below. The weightless Go
> outcome worker MUST re-implement it **byte-for-byte** so a session-time
> dirty-worktree anchor matches the same code's later committed-PR hunk.

This document is the **single source of truth** for two content-addressed
identities the weightless ledger joins on:

1. **`hunk_hash`** — a stable anchor for a single diff hunk, decoupled from line
   numbers and positions, so the CLI's dirty-worktree judgment binds to the
   eventual committed PR hunk with no in-session commit capture.
2. **`verifier_version`** — a stable short hash of a verifier's identity
   (resolved skill body + agent + model + thinking + direction), so every
   judgment is attributable to the exact rubric that produced it.

Reference Go implementation:
- `internal/telemetry/anchor.go` — `hunkAnchor` / `HunkAnchor`,
  `parseDiffHunks`, `normalizeBody`, `collapseWhitespace`, `hashNormalizedBody`.
- `internal/verifier/runner.go` — `verifierVersionErr` / `verifierVersion`,
  `versionInput`.

Golden vectors that pin both sides:
- `internal/telemetry/testdata/anchor_vectors.json` (hunk normalization).
- The pinned `verifier_version` constant in
  `internal/verifier/runner_test.go` (`TestVerifierVersionGolden`).

---

## 1. `hunk_hash` — stable hunk anchor

### Inputs
`hunkAnchor(worktree, baseRef, file, line)`:
- `worktree` — absolute path of the checkout (only used to run git; **never**
  enters the hashed bytes).
- `baseRef` — the session base ref (`SESSION_BASE_REF`); the stable HEAD the
  dirty worktree is measured against.
- `file` — repo-relative path (the normalized key from `NormalizeRepoPath`).
- `line` — 1-based new-side line of the finding (`0`/negative ⇒ no hunk).

### Algorithm (byte-exact)

1. **Diff.** Run `git diff -U0 <baseRef> -- <file>` in `worktree`. The `--`
   disambiguates the pathspec. **`-U0` (zero context lines) is mandatory**: it is
   what makes the anchor position-invariant. With default context, lines
   surrounding the change leak into the hunk body, so a shift in nearby unrelated
   lines would change the hash; with `-U0` the hunk body is exactly the
   added/changed content. Empty output (untracked / unchanged / git error /
   timeout) ⇒ return `("", "")`.
2. **Split into hunks.** Each hunk begins with a header
   `@@ -<oldStart>,<oldLen> +<newStart>,<newLen> @@ …`. The trailing `,<len>` is
   omitted by git when the length is `1`; treat the missing length as `1`. The
   `newStart`/`newLen` are read **only to locate** the hunk — they are **not**
   hashed.
3. **Select body lines.** Within a hunk, keep lines beginning with `+` (added)
   and ` ` (context); **drop** lines beginning with `-` (removed). Under `-U0`
   there are no context lines, so in practice the body is exactly the added
   lines — but the parser keeps context lines too, so the rule is robust if a
   caller ever passes a wider-context diff. Ignore the diff file header
   (`diff …`, `index …`, `--- …`, `+++ …`) and any `\ No newline at end of file`
   marker.
4. **Strip the marker column.** Remove the single leading marker byte (`+` or
   the leading space) from each retained line. The `@@` header is already
   excluded (it is never added to a body).
5. **Collapse whitespace.** For each body line, replace every maximal run of
   `space` / `tab` / `CR` with a single ASCII space, then trim leading and
   trailing whitespace. A line that is **empty after trimming is dropped** (so a
   blank-line insertion is not a content change).
6. **Join.** Join the surviving normalized lines with a single `\n` (no trailing
   newline). This is the `normalizedBody`.
7. **Hash.** `hunk_hash = hex(sha256(normalizedBody))[:16]` — lowercase, exactly
   16 hex chars (8 bytes). An empty `normalizedBody` (no added/context content)
   ⇒ `""`.

`hunk_hash` is the hash of the **single** hunk whose new-side range
`[newStart, newStart+newLen)` contains `line`. If `line` falls in no hunk (or is
`≤ 0`), `hunk_hash` is `""` and the caller falls back to `dirty_diff_hash`.

### `dirty_diff_hash` — whole-file coarse anchor
`dirty_diff_hash` runs the **identical** pipeline (steps 3–7) over the
concatenation of **every** hunk's body in `git diff <baseRef> -- <file>`,
independent of `line`. For a single-hunk file it equals that hunk's `hunk_hash`.
It is the anchor for tree-global / no-line findings (which carry only
`dirty_diff_hash`).

### `hunk_hashes` — the per-file hunk-hash SET
`FileHunkHashes(worktree, baseRef, file)` returns the `hunk_hash` of **every**
changed hunk of `file` (steps 3–7 per hunk), in diff order, de-duplicated. A
line-unset / file-level finding (`line == 0`) carries no `hunk_hash`, so it
anchors via this set instead — any of the hashes binds it at hunk granularity
later. `dirty_diff_hash` remains the last-resort coarse anchor. The set is empty
for tree-global findings (no file) and unchanged by whitespace-only or
position-shift edits (each hash is computed exactly as the single `hunk_hash`).

### Grains summary
| Finding shape                         | `hunk_hash` | `hunk_hashes` | `dirty_diff_hash` |
| ------------------------------------- | ----------- | ------------- | ----------------- |
| file + line inside a hunk             | set         | all hunks     | set               |
| file present, line `0` / no hunk      | `""`        | all hunks     | set               |
| no file (tree-global)                 | `""`        | empty         | `""`              |
| file present but no diff vs base      | `""`        | empty         | `""`              |

### Stability guarantees (what the contract buys)
- **Whitespace-only edit** ⇒ identical `hunk_hash` (step 5).
- **Line-number / position shift** (lines inserted above the hunk) ⇒ identical
  `hunk_hash` (positions are excluded; step 2/3).
- **One-character body content edit** ⇒ different `hunk_hash`.
- **Determinism**: content-only; no time, randomness, map-iteration order, or
  absolute paths enter the hashed bytes — repeated calls and calls from
  different worktree paths over identical content yield the same pair.

### Golden vector (pinned)
Body lines (markers already stripped), from
`internal/telemetry/testdata/anchor_vectors.json`:

```
func Add(a, b int) int {
\treturn a + b
}
```

⇒ `hunk_hash = becae935df3b19d6`

The whitespace-reflowed variant hashes to the **same** `becae935df3b19d6`; a
one-character content edit (`+` → `-`) hashes to `3945ebb26cf1c908`.

---

## 2. `verifier_version`

### Formula
```
verifier_version = sha256(resolvedSkillBody + agent + model + thinking + direction)[:12]
```

### Serialization (byte-exact)
The five canonical inputs (plus a type discriminator and type-appropriate
provenance) are placed into a fixed-field-order struct and `json.Marshal`ed (no
maps, so the bytes are stable across processes/machines), then
`hex(sha256(bytes))[:12]`. The struct, in order, is:

| JSON key      | Source                                              | Notes |
| ------------- | --------------------------------------------------- | ----- |
| `type`        | `agent` \| `command` \| `binary`                    | discriminator |
| `agent`       | `resolveAgent(v.Agent.Agent)` (default `claude`)    | agent only |
| `model`       | `v.Agent.Model`                                     | agent only |
| `thinking`    | `v.Agent.Thinking`                                  | agent only |
| `direction`   | `strings.ToUpper(TrimSpace(v.Direction))`           | case-normalized |
| `skill_body`  | `skillBody(v.Agent.Skill)`                          | **resolved body, not path** |
| `command`     | `strings.Join(argv, "\x00")`                        | command/binary only |
| `source`      | `v.Source`                                          | remote provenance |
| `source_url`  | `v.SourceURL`                                       | remote provenance |
| `sha256`      | `v.SHA256`                                          | remote content identity |

Rules:
- **Skill body, not path.** Two identical rubrics at different paths share a
  version; a one-byte body change changes it.
- **Body is hashed verbatim** (frontmatter stripped per `skillBody`, **no**
  whitespace collapse) — a reworded rubric is a re-induction. A
  frontmatter-only edit (above the first `---`) leaves the body unchanged ⇒ the
  version is unchanged.
- **Excluded fields** (`Permissions`, `Timeout`, the skill **path**) MUST NOT
  affect the version.
- **Fail-safe**: an unreadable skill or marshal failure yields `""` (the run is
  still recorded; the failure is logged) — never a panic.
- **Output**: exactly 12 lowercase hex chars (`^[0-9a-f]{12}$`).
- **Write-once**: a stored `verifier_version` is never rewritten on existing
  rows; re-induction produces a new version and old judgments keep the old one.

### Golden vector (pinned)
Agent verifier, `skill_body = "the rubric body\n"`, `agent = claude`,
`model = ""`, `thinking = ""`, `direction = N`:

```
{"type":"agent","agent":"claude","model":"","thinking":"","direction":"N","skill_body":"the rubric body\n","command":"","source":"","source_url":"","sha256":""}
```

⇒ `verifier_version = 250cc2bbc362`

> Changing the serialization order or separators invalidates **all** prior
> `verifier_version` joins; treat it as a deliberate, versioned migration with a
> changelog entry.
