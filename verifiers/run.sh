#!/usr/bin/env bash
# run.sh — HUD verifier wrapper backed by a SKILL.md persona.
#
# Usage (from hud.yaml): command: ["./verifiers/run.sh", "<persona>"]
#
# Reads the standard HUD session JSON on stdin, loads
# ../skills/<persona>/SKILL.md as the rubric body, exports the session
# base ref so the persona's `git diff $SESSION_BASE_REF` calls resolve,
# asks `claude -p` to score cumulative session work against the goal,
# and writes {"distance": <0..1>, "reason": "..."} on stdout.
#
# Requires: claude CLI on PATH, jq.

set -euo pipefail

if [[ $# -lt 1 ]]; then
    printf '{"distance": 1.0, "reason": "run.sh: missing persona argument"}\n'
    exit 0
fi
persona="$1"

if ! command -v claude >/dev/null 2>&1; then
    printf '{"distance": 1.0, "reason": "claude CLI not on PATH"}\n'
    exit 0
fi
if ! command -v jq >/dev/null 2>&1; then
    printf '{"distance": 1.0, "reason": "jq not on PATH"}\n'
    exit 0
fi

script_dir="$(cd "$(dirname "$0")" && pwd)"
skill_path="${script_dir}/../skills/${persona}/SKILL.md"
if [[ ! -f "$skill_path" ]]; then
    printf '{"distance": 1.0, "reason": "skill not found at %s"}\n' "$skill_path"
    exit 0
fi

# Strip YAML frontmatter so the prompt body starts with the rubric, not
# the metadata block.
skill_body=$(awk '
    BEGIN { in_fm = 0; seen = 0 }
    /^---[[:space:]]*$/ {
        if (!seen) { seen = 1; in_fm = 1; next }
        if (in_fm)  { in_fm = 0; next }
    }
    !in_fm { print }
' "$skill_path")

session_json=$(cat)
goal=$(printf '%s' "$session_json" | jq -r '.goal // ""')
files=$(printf '%s' "$session_json" | jq -r '.changed_files // [] | join(", ")')
verifier_name=$(printf '%s' "$session_json" | jq -r '.verifier_name // ""')
session_base_ref=$(printf '%s' "$session_json" | jq -r '.session_base_ref // ""')

# Exported so any Bash tool calls inside `claude -p` see the same value
# the SKILL body references with `$SESSION_BASE_REF`.
export SESSION_BASE_REF="$session_base_ref"

# bash 3.2 (macOS default) mishandles unbalanced single quotes inside a
# heredoc nested in $(...). `read -r -d ''` captures the heredoc into a
# variable directly. `|| true` because read exits 1 at EOF before NUL.
IFS= read -r -d '' prompt <<EOF || true
${skill_body}

---

## Session context

Verifier name: ${verifier_name:-${persona}}
Active goal: ${goal:-<no goal set>}
Session base ref (\$SESSION_BASE_REF): ${session_base_ref:-<unset; fall back to HEAD>}
Recently changed files (last write batch, for orientation only — score the cumulative diff, not this list): ${files:-<none>}

## Output contract (HUD verifier mode)

After your evaluation, output exactly one final line of JSON, with no
other text on that line:

{"distance": <number 0.0..1.0>, "reason": "<one short sentence>"}

- 0.0 = the goal is fully satisfied through the cumulative session work.
- 1.0 = it is maximally unsatisfied.
- The reason is the single most load-bearing observation — what should
  change the agent's next decision — not a summary.
- No commentary after the JSON line.
EOF

# Allow the persona to inspect repo state via git, plus read files. No
# write tools — verifiers must not mutate the working tree.
raw=$(printf '%s' "$prompt" | claude -p \
    --output-format json \
    --model sonnet \
    --allowedTools \
        "Bash(git diff:*)" \
        "Bash(git diff)" \
        "Bash(git status:*)" \
        "Bash(git log:*)" \
        "Bash(git show:*)" \
        "Bash(git ls-files:*)" \
        "Bash(git rev-parse:*)" \
        "Read" "Grep" "Glob" \
    2>/dev/null || true)

if [[ -z "$raw" ]]; then
    printf '{"distance": 1.0, "reason": "claude CLI returned no output"}\n'
    exit 0
fi

# `claude -p --output-format json` wraps the model output in an envelope
# with a `result` string field. Pull it; fall back to raw if missing.
result=$(printf '%s' "$raw" | jq -r '.result // empty' 2>/dev/null || true)
if [[ -z "$result" ]]; then
    result="$raw"
fi

# Defensive parse: take the last single-level JSON object containing
# "distance". Tolerates any chatter the model adds before the JSON line.
parsed=$(printf '%s' "$result" \
    | grep -oE '\{[^{}]*"distance"[^{}]*\}' \
    | tail -n1 || true)

if [[ -z "$parsed" ]] || ! printf '%s' "$parsed" | jq -e '.distance' >/dev/null 2>&1; then
    printf '{"distance": 0.5, "reason": "%s could not parse model output"}\n' "${verifier_name:-${persona}}"
    exit 0
fi

printf '%s\n' "$parsed"
