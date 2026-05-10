#!/usr/bin/env bash
# dummy.sh — deterministic no-op HUD verifier.
#
# Reads and ignores the standard HUD session JSON, then always reports
# distance 0.0 so it sits on the goal.

set -euo pipefail

cat >/dev/null

printf '{"distance": 0.0, "reason": "dummy verifier always passes"}\n'
