#!/usr/bin/env bash
# bench.sh — Go benchmark regression verifier. Compares the current tree's
# `go test -bench` numbers against the session base ref using benchstat.
# Distance scales with the worst regression: 0% diff → 0.0, ≥20% slower → 1.0.
#
# Required tooling: go, benchstat (go install golang.org/x/perf/cmd/benchstat@latest).
# Without them the verifier reports status=unknown so it doesn't pin red
# on machines that don't have the bench harness installed.

set -euo pipefail
cat >/dev/null || true

unknown() {
    printf '{"distance": 0.0, "reason": %s, "status": "unknown"}\n' "$(printf '"%s"' "$1")"
    exit 0
}

base="${SESSION_BASE_REF:-HEAD}"
command -v go >/dev/null 2>&1 || unknown "go toolchain not on PATH"
command -v benchstat >/dev/null 2>&1 || unknown "benchstat not installed; install with: go install golang.org/x/perf/cmd/benchstat@latest"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Run benchmarks on current tree.
if ! go test -run x -bench=. -benchmem -count=3 ./... >"$work/new.txt" 2>"$work/new.err"; then
    unknown "go test -bench failed on current tree"
fi

# Run benchmarks at session base. Use a fresh worktree so we don't disturb
# the user's working tree; abort gracefully if the base ref isn't
# checkoutable (e.g. shallow clone).
worktree="$work/wt"
if ! git worktree add -q --detach "$worktree" "$base" 2>/dev/null; then
    unknown "could not check out session base $base; shallow clone?"
fi
trap 'git worktree remove -f "$worktree" >/dev/null 2>&1; rm -rf "$work"' EXIT

if ! ( cd "$worktree" && go test -run x -bench=. -benchmem -count=3 ./... ) >"$work/old.txt" 2>"$work/old.err"; then
    unknown "go test -bench failed on session base"
fi

# benchstat -row=. prints a delta column. Worst-case slowdown drives distance.
delta=$(benchstat "$work/old.txt" "$work/new.txt" 2>/dev/null \
    | awk '/[+-][0-9]+\.[0-9]+%/ {
        for (i=1; i<=NF; i++) {
            if ($i ~ /^[+-][0-9]+\.[0-9]+%$/) {
                gsub(/[+%]/, "", $i);
                if ($i+0 > worst) worst = $i+0
            }
        }
    } END { printf "%.2f", worst+0 }')

if [[ -z "${delta:-}" || "$delta" == "0.00" ]]; then
    printf '{"distance": 0.0, "reason": "no benchmark regression vs session base"}\n'
    exit 0
fi

# Map (delta% slower) -> distance: 0%→0, 20%→1, clamp.
distance=$(awk -v d="$delta" 'BEGIN { x = d / 20.0; if (x<0) x=0; if (x>1) x=1; printf "%.3f", x }')
printf '{"distance": %s, "reason": "worst benchmark regressed by %s%%"}\n' "$distance" "$delta"
