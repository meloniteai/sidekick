#!/usr/bin/env bash
# coverage.sh — deterministic example Sidekick verifier.
#
# Reads the standard sidekick session JSON on stdin (we ignore it for this
# verifier) and writes {"distance": <0..1>, "reason": "..."} on stdout.
#
# Distance derivation: run `go test -cover ./...` in the project root, parse
# the highest coverage percentage, and map (100% → 0.0, 0% → 1.0).
#
# A non-Go project, or one with no tests, is reported as distance=1.0.

set -euo pipefail

# Discard stdin (we don't need it here).
cat >/dev/null

if ! command -v go >/dev/null 2>&1; then
    printf '{"distance": 1.0, "reason": "go toolchain not on PATH"}\n'
    exit 0
fi

if ! ls *.go go.mod >/dev/null 2>&1; then
    printf '{"distance": 1.0, "reason": "no Go module detected in cwd"}\n'
    exit 0
fi

raw=$(go test -cover ./... 2>&1 || true)
# Highest "X.Y% of statements" found in the output.
pct=$(printf '%s\n' "$raw" \
    | grep -oE '[0-9]+\.[0-9]+% of statements' \
    | grep -oE '[0-9]+\.[0-9]+' \
    | sort -gr \
    | head -n1 || true)

if [[ -z "${pct:-}" ]]; then
    printf '{"distance": 1.0, "reason": "no coverage data; tests may have failed or be missing"}\n'
    exit 0
fi

distance=$(awk -v p="$pct" 'BEGIN { d = 1 - (p/100); if (d<0) d=0; if (d>1) d=1; printf "%.3f", d }')
reason=$(printf 'best-package coverage: %s%%' "$pct")
printf '{"distance": %s, "reason": "%s"}\n' "$distance" "$reason"
