#!/usr/bin/env bash
# lint.sh — binary verifier that runs the project's lint tool. Auto-detects
# Go (gofmt + go vet), JS/TS (eslint), or Python (ruff/flake8); falls back
# to "unknown" via exit 0 with no output when none is found.
#
# As a `binary` verifier, this script is scored purely on exit code:
# exit 0 = distance 0 (lint clean), non-zero = distance 1 (lint failed).
# Use the `binary` verifier type in hud.yaml; do not emit JSON yourself.

set -euo pipefail

# Drain stdin so the parent doesn't block.
cat >/dev/null || true

if [[ -f go.mod ]]; then
    if command -v gofmt >/dev/null 2>&1; then
        bad=$(gofmt -l . 2>&1 || true)
        if [[ -n "$bad" ]]; then
            printf 'gofmt issues:\n%s\n' "$bad" >&2
            exit 1
        fi
    fi
    if command -v go >/dev/null 2>&1; then
        if ! go vet ./... 2>&1; then
            exit 1
        fi
    fi
    exit 0
fi

if [[ -f package.json ]]; then
    if command -v npx >/dev/null 2>&1 && npx --no-install eslint --version >/dev/null 2>&1; then
        npx --no-install eslint . >&2
        exit $?
    fi
fi

if compgen -G "*.py" >/dev/null || [[ -f pyproject.toml ]]; then
    if command -v ruff >/dev/null 2>&1; then
        ruff check . >&2
        exit $?
    fi
    if command -v flake8 >/dev/null 2>&1; then
        flake8 . >&2
        exit $?
    fi
fi

# No supported linter found. Treat as a pass so this verifier doesn't
# permanently pin itself to red on unsupported projects.
exit 0
