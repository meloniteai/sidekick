# Writing a HUD verifier

This is the contract every verifier must satisfy, plus the conventions
the bundled and community verifiers follow. If you write a verifier,
publish it from your own repo (or any HTTPS-reachable URL); users can
install it with `hud verifier add <url>` once you give them a sha256
pin.

There is no central registry. Pinning by sha256 is mandatory.

---

## The protocol

A verifier is a process that the HUD daemon spawns once per evaluation
batch. There are three types:

| Type | I/O | When to use |
|---|---|---|
| `command` | stdin: session JSON · stdout: `{"distance", "reason", "status"}` JSON · exit 0 | Custom scoring logic. Most extensible. |
| `binary` | stdin: session JSON (may ignore) · stdout/stderr: free · exit code = score | Pass/fail wrappers around existing tools (`go test`, `eslint`). |
| `agent` | stdin: prompt body · stdout: agent CLI output (HUD parses) | Persona-style review with an LLM, driven by a `SKILL.md` rubric. |

### Session JSON (stdin for `command` and `binary`)

```json
{
  "goal": "ship the auth module",
  "session_base_ref": "abc123def...",
  "changed_files": ["src/auth.go", "src/auth_test.go"],
  "last_messages": ["user: ...", "assistant: ..."],
  "verifier_name": "MyVerifier"
}
```

Drain stdin even if you don't need it (`cat >/dev/null`); otherwise
the parent's pipe write will eventually block.

### Score JSON (stdout for `command`)

Output one JSON object on a single line:

```json
{"distance": 0.42, "reason": "one short sentence", "status": "ok"}
```

- `distance` ∈ `[0.0, 1.0]` — clamped by HUD if you over/undershoot.
- `reason` is a single short sentence — the **most load-bearing**
  observation, not a summary.
- `status` is optional. If your verifier ran cleanly, omit it (HUD
  promotes to `"ok"`). If the verifier could not score this run
  (tooling missing, no diff to evaluate, prerequisite step pending),
  set `"status": "unknown"` and HUD will preserve the prior distance
  instead of pretending the score moved.

You may emit log lines on stderr (or even on stdout before your final
JSON line) — HUD is brace-aware and string-aware when extracting the
result, so trailing or leading prose is tolerated.

### Score anchors

To stay legible to agents, calibrate to one of:

- **0.00** — Goal fully satisfied (your dimension).
- **0.25** — Minor friction. Keep moving.
- **0.50** — A real concern. Address before next milestone.
- **0.75** — Blocking issue. Pivot now.
- **1.00** — Goal contradicted, or no diff at all.

Free-floating decimal scores ("0.37"/"0.42"/"0.61") drift between
runs and become noise. Stick to the anchors unless you have strong
evidence for something in between.

### Environment variables

- `SESSION_BASE_REF` — git SHA of `HEAD` when `hud start` began. Diff
  against this for cumulative session work, **not** against the last
  write.
- `HUD_VERIFIER=1` — set automatically. Use this in your script if you
  call `claude` or `codex` and want to be sure HUD's hooks won't recurse
  on writes triggered by the verifier itself.

### Timeouts

Default 60s per verifier. Override per-verifier in `hud.yaml`:

```yaml
- name: Slow
  type: command
  timeout: 120s
  command: ["./verifiers/slow.sh"]
```

The subprocess receives a SIGTERM at the timeout, then SIGKILL shortly
after. Make sure your tool propagates signals (or wrap with `exec`).

---

## Authoring a `command` verifier

A minimum viable shell verifier:

```bash
#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null   # drain stdin

if ! command -v go >/dev/null; then
    printf '{"distance": 0.0, "reason": "go not on PATH", "status": "unknown"}\n'
    exit 0
fi

count=$(go build ./... 2>&1 | grep -c '^')
distance=$(awk -v c="$count" 'BEGIN { x = c / 10.0; if (x>1) x=1; printf "%.3f", x }')
printf '{"distance": %s, "reason": "%d build warnings"}\n' "$distance" "$count"
```

Then in `hud.yaml`:

```yaml
verifiers:
  - name: BuildWarnings
    type: command
    direction: NW
    timeout: 60s
    command: ["./verifiers/build-warnings.sh"]
```

See [`examples/community/docs-drift.sh`](examples/community/docs-drift.sh)
for a more complete example.

---

## Authoring an `agent` verifier (SKILL.md)

A minimum viable rubric:

```markdown
---
name: my-verifier
description: One-line description shown in skill listings.
---

# my-verifier

You are the [Persona] reviewer for the HUD compass. Evaluate the
cumulative session work through [your specific lens], against the
agent's stated goal.

## How to evaluate

1. `$SESSION_BASE_REF` is the commit `HEAD` was at when `hud start` ran.
2. Run `git diff $SESSION_BASE_REF --stat` to size the change.
3. Run `git diff $SESSION_BASE_REF` to read it.
4. Run `git status --porcelain` for untracked files.

## What you care about
- (positive criteria)

## What to penalize
- (negative criteria)

## Score anchors ([dimension])
- 0.00 — ...
- 0.25 — ...
- 0.50 — ...
- 0.75 — ...
- 1.00 — ...
```

Then in `hud.yaml`:

```yaml
verifiers:
  - name: My Persona
    type: agent
    direction: NE
    llm:
      agent: claude         # or codex
      model: haiku
      thinking: low
      skill: ./skills/my-persona/SKILL.md
```

HUD strips your YAML frontmatter, appends the runtime score-anchor
contract, and shells out to the configured agent CLI. The agent runs
with a tool allowlist of read-only git/file operations only.

See [`skills/architect/SKILL.md`](skills/architect/SKILL.md) and
[`examples/community/migration-safety/SKILL.md`](examples/community/migration-safety/SKILL.md)
for full rubrics.

---

## Publishing a verifier

1. Push your script or `SKILL.md` to a public URL (GitHub raw, Gitea
   raw, your own static host — anything HTTPS-reachable).
2. Compute the sha256 (`shasum -a 256 my-verifier.sh`) or let
   `hud verifier add` compute it for you on first download.
3. In your README, give users the install command:

   ```bash
   hud verifier add https://raw.githubusercontent.com/you/yours/v1/my-verifier.sh \
     --name MyVerifier --direction NE
   ```

   Or, if you want them to pin a specific revision yourself:

   ```yaml
   # hud.yaml
   verifiers:
     - name: MyVerifier
       type: command
       direction: NE
       source:
         url: https://raw.githubusercontent.com/you/yours/v1/my-verifier.sh
         sha256: <64 hex chars>
   ```

HUD verifies the sha256 on every load. If you ship a new version, bump
the URL (e.g. `/v2/...`) and the sha256 — never silently overwrite
content at the same URL, because HUD will refuse to load a body whose
hash drifted from the pin.

## Permissions

Declare what your verifier needs. v0.1 surfaces these in the TUI on
first run for trust-on-first-use; future versions will use them to
configure platform sandboxes.

```yaml
- name: MyVerifier
  type: command
  command: ["./verifiers/mine.sh"]
  permissions:
    filesystem: read-only        # read-only | read-write | none
    network: false                # default false
    env: ["PATH", "HOME"]         # allowlist; everything else stripped
```

Be conservative. A verifier that doesn't need network shouldn't
declare it; users will trust your verifier more if its declared
surface matches what it actually does.

## Testing a verifier locally

```bash
# Smoke-test the JSON contract:
echo '{"goal":"x","changed_files":["a.go"],"verifier_name":"Test"}' \
  | ./verifiers/mine.sh
# Expected: a single JSON line with distance, reason, optional status.

# Run inside HUD:
hud start --headless &
echo '{"tool_input":{"file_path":"a.go"}}' | hud hook write
hud status   # see your verifier's distance/reason in the snapshot
```

## Common pitfalls

- **Forgetting to drain stdin.** Hangs the parent eventually.
- **Emitting JSON as a quoted string** (`echo "{...}"`). The shell may
  swallow the braces. Use `printf '{"distance": %s, ...}\n' "$x"`.
- **Computing distance from a single instance instead of cumulative
  work.** Diff against `$SESSION_BASE_REF`, not `HEAD~1`.
- **Hard-coding paths.** `cd` in the script if you need to, or take
  paths relative to `$PWD` (HUD runs verifiers from the directory
  it was started in).
- **Returning errors as distance=1.** That conflates "goal contradicted"
  with "tooling broken." Return `status: unknown` instead — HUD will
  preserve the prior distance and flag the row as not-yet-evaluable.
