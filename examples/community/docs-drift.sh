#!/usr/bin/env bash
# docs-drift.sh — score how well the cumulative session diff kept docs in
# sync with code changes. distance = ratio of source files changed without
# a matching docs change.
#
# This is a deterministic command verifier: stdin = session JSON, stdout
# = exactly one {"distance": ..., "reason": "..."} object on a single line.
# All other output goes to stderr (HUD will ignore it).

set -euo pipefail

# We don't need stdin for this verifier; just drain it so the parent
# subprocess pipe doesn't block.
cat >/dev/null

base="${SESSION_BASE_REF:-HEAD}"

if ! command -v git >/dev/null 2>&1; then
    printf '{"distance": 0.0, "reason": "git not on PATH; cannot evaluate", "status": "unknown"}\n'
    exit 0
fi

# List changed files since session base (cumulative work, not just last edit).
# Untracked files appear in `git status --porcelain` separately. Use a
# Bash-3-compatible read loop so the verifier works on macOS's default bash.
tracked=()
while IFS= read -r f; do
    [[ -n "$f" ]] && tracked+=("$f")
done < <(git diff --name-only "$base" 2>/dev/null || true)

untracked=()
while IFS= read -r f; do
    [[ -n "$f" ]] && untracked+=("$f")
done < <(git ls-files --others --exclude-standard 2>/dev/null || true)

files=("${tracked[@]}" "${untracked[@]}")

if [[ ${#files[@]} -eq 0 ]]; then
    printf '{"distance": 0.0, "reason": "no diff since session base; nothing to evaluate", "status": "unknown"}\n'
    exit 0
fi

source_count=0
docs_count=0
for f in "${files[@]}"; do
    case "$f" in
        *.md|*.rst|*.txt|docs/*|README*) docs_count=$((docs_count + 1)) ;;
        *.go|*.ts|*.tsx|*.js|*.jsx|*.py|*.rs|*.java|*.kt|*.rb|*.cs|*.cc|*.cpp|*.c|*.h)
            source_count=$((source_count + 1)) ;;
    esac
done

if [[ $source_count -eq 0 ]]; then
    printf '{"distance": 0.0, "reason": "no code files in diff; docs drift not applicable"}\n'
    exit 0
fi

# Heuristic: ratio of changed source files to docs changes.
# 0 docs changes for any source change → distance = 1.0
# >= 1 docs change for every 5 source files → distance ≈ 0
ratio=$(awk -v s="$source_count" -v d="$docs_count" 'BEGIN {
    if (d == 0) { printf "1.000"; exit }
    r = s / (5 * d);
    if (r < 0) r = 0;
    if (r > 1) r = 1;
    printf "%.3f", r;
}')

reason=$(printf '%d source file(s) changed, %d docs file(s) updated' "$source_count" "$docs_count")
printf '{"distance": %s, "reason": "%s"}\n' "$ratio" "$reason"
