![sidekick](assets/sidekick-hero.png)

# Sidekick

**A live compass for coding agents. Set a goal, plug in verifiers, correct course in-flight.**

`sidekick` runs alongside coding agents. The agent sets the session's goal in plain English; you plug in verifiers — small rubrics or scripts that
score how far the working tree is from that goal. 

Sidekick fires on file edit, re-runs them in parallel and renders the results as orbs on a 2D compass.
Each verifier reports a `distance ∈ [0, 1]`: `0` means "satisfied", `1`
means "maximally unsatisfied". The agent reads the compass via the
`sidekick_status` and `sidekick_explain` MCP tools and course-corrects mid-session,
instead of finding out at review time.

---

## Demo

https://github.com/user-attachments/assets/07dd4bd8-afa5-4194-9f21-7bcf8aef113d

## How it works

```
                  ▲ Architect
                  N
                  ●
   Deployment ●---+---● Test
                  ●
                  S
                Security
```

- The active goal is set by the agent itself via the `sidekick_set_goal` MCP
  tool, driven by the bundled [`sidekick` skill](skills/sidekick/SKILL.md).
- File-write hooks 
  trigger a debounced batch run of enabled verifiers.
- Verifiers can be native LLM skill checks, binary pass/fail commands, or
  custom commands that speak Sidekick's stdin/stdout JSON protocol.
- The TUI re-renders the compass every 200 ms.
  tenth) to toggle that verifier on or off for future runs.
- Agents call `sidekick_status` to read the snapshot — it never triggers
  recomputation, only file writes do.

## Three processes, one daemon

| Binary | Role | Lifetime |
|---|---|---|
| `sidekick` | Long-running daemon + Bubble Tea TUI. Owns state, runs verifiers. Listens on a repo-scoped socket under `~/.sidekick/sockets/<fingerprint>.sock` so multiple projects can run side-by-side. | foreground |
| `sidekick hook <event>` | Spawned by Claude Code or Codex hooks. Reads hook JSON on stdin, posts a normalized event to the daemon, exits. | one-shot |
| `sidekick mcp` | Spawned by the agent client as an MCP server. Proxies `sidekick_status` / `sidekick_explain` to the daemon. | per agent session |
| `sidekick verifier add <url>` | Fetches a remote SKILL.md or verifier script, pins it by sha256, and registers it in `sidekick.yaml`. | one-shot |
| `sidekick verifier add --local` | Interactive wizard that walks through name, direction, type, command/skill, and timeout, then writes a local entry to `sidekick.yaml`. | one-shot |
| `sidekick verifier list` | Prints the verifiers configured in `sidekick.yaml` with source provenance. | one-shot |

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/meloniteai/sidekick/main/install.sh | bash
```

or build from source:
```bash
git clone https://github.com/meloniteai/sidekick
cd sidekick
go build -o sidekick .
# put `sidekick` somewhere on PATH, e.g.
mv sidekick ~/bin/
```

## Wire up Claude Code, Codex

Use the install command and follow the wizard:

```bash
sidekick install
```

## Run

```bash
# 1. Start the Sidekick in a terminal
sidekick 

# 2. In another terminal, run Claude Code as usual
claude

# 3. Type a prompt. The agent calls sidekick_set_goal to register the goal.
# 4. As the agent edits files, the compass updates.
# 5. The agent calls sidekick_status to read the compass and course-correct.
```

Once the TUI is up, hit `ctrl+v` to open the remote verifier browser and
install your first verifiers from the community registry — no editing
`sidekick.yaml` by hand required.

![Remote verifier browser](https://github.com/user-attachments/assets/24796075-a805-4789-a858-58ece7a31179)

Installs come in two scopes (toggle with `p`/`g` in the browser). **Project**
copies the verifier's skill into `.sidekick/` next to your `sidekick.yaml`, so
it is fully editable in-session — press `e` on it to open the SKILL.md editor.
**Global** pins it in the shared cache under `~/.sidekick/`, where it stays
read-only across every repo; the editor shows it but blocks edits. Install at
project scope to fork a global verifier you want to tweak.

## TUI or Headless execution

Pass `--headless` to run the daemon without the Bubble Tea TUI — useful for
CI, sandboxes, or remote dev containers. Hooks, MCP, `sidekick status`, and
`sidekick goal` behave the same; verifiers load from `sidekick.yaml` as
configured (no landing picker, no mid-session toggles).

```bash
sidekick --headless                    # agent sets the goal via MCP
sidekick --headless --goal "ship auth" # pin the goal up front
```

Git worktrees are supported in both modes: the daemon spins up a separate
session per worktree on first hook event, and the TUI lets you switch between them.

## Backend telemetry

Sidekick can emit live telemetry to `sidekick-api` instead of only writing the
local SQLite store. Pair the CLI once per org:

```bash
sidekick login --org acme
sidekick auth status
```

Pass `--api-base` only when pairing with a non-default backend.

The login command opens a browser approval page, polls until the signed-in
Sidekick user approves the device, and stores an opaque CLI token in
`~/.sidekick/auth.json` with `0600` permissions. `sidekick logout` revokes and
removes the active token.

When an auth profile exists, `sidekick start` in auto/backend telemetry mode
uses the stored org and token, resolves the repo through
`/api/orgs/{org}/projects/resolve`, and sends each telemetry event with the CLI
bearer token. If backend auth is missing or invalid, auto mode falls back to the
local store and prints the login command to run.

## Verifiers

There are multiple options. Choose what works best for you:

1. Quickstart: use the remote verifier browser in the tui (`ctrl+v` once inside the sidekick tui)
2. Configure verifiers in the TUI, or use `sidekick verifier add --local`, to add your own new local verifiers

3. Provision a tracked `sidekick.yaml` next to your code. 

## Agent verifiers

Use `type: agent` for qualitative checks that benefit from an LLM reviewing
the active goal, base ref, changed files, and recent context. Sidekick loads
the configured `SKILL.md`, adds the JSON output contract and score anchors,
then shells out to `claude`, `codex`, or a custom CLI. Authentication and
model access stay with the user's installed agent CLI.

Score anchors use a fixed 5-point scale (0.00 / 0.25 / 0.50 / 0.75 / 1.00)
so agent scores stay comparable across runs. See the [verifier registry](https://github.com/meloniteai/sidekick-verifiers/blob/main/CONTRIBUTING-VERIFIERS.md)
for the per-dimension calibration each bundled skill uses.

Basic Claude-backed verifier:

```yaml
- name: Architect
  type: agent
  direction: N
  timeout: 90s
  llm:
    agent: claude
    model: sonnet
    thinking: low
    skill: ./skills/architect/SKILL.md
```

`binary` verifiers receive the same session JSON on stdin and
`SESSION_BASE_REF` in the environment, but Sidekick scores them purely from
exit code.

Use `type: command` only when you need a deterministic script or custom tool
to compute its own score. Command verifiers read the session JSON on stdin,
print one JSON object with `distance`, `reason`, and optional `status`, and
exit zero. Non-zero exits, missing JSON, or timeouts are reported as verifier
errors.

## Remote (community) verifiers

Verifiers can be loaded from any HTTPS URL with a sha256 pin. Install
one with:

```bash
sidekick verifier add https://raw.githubusercontent.com/you/yours/v1/perf.sh \
  --name Performance --direction NE
```

This downloads the file, prints a 20-line preview, prompts for
confirmation, computes the sha256, writes a `source:` block into
`sidekick.yaml`, and records the approved hash in `~/.sidekick/trust.json`. On
subsequent loads, Sidekick verifies the hash from the on-disk cache before
running the script — drift fails loud.

To pin a verifier by hand without going through `add`:

```yaml
# sidekick.yaml
verifiers:
  - name: Performance
    type: command
    direction: NE
    source:
      url: https://raw.githubusercontent.com/you/yours/v1/perf.sh
      sha256: <64 hex chars>
    permissions:
      filesystem: read-only
      network: false
```

See the [verifier registry](https://github.com/meloniteai/sidekick-verifiers)
for the full protocol, contribution flow, and a catalog of
community verifiers.

## Local verifiers (interactive)

For a verifier that lives entirely in your repo (no URL, no sha256 pin),
use the wizard:

```bash
sidekick verifier add --local
```

It prompts field-by-field for name, compass direction, type (agent /
command / binary), the per-type config (skill path or command argv),
optional timeout, and optional advisory permissions, then appends the
entry to `sidekick.yaml`. `--name`, `--direction`, `--type`, and
`--permissions` flags pre-fill defaults if you already know them.
