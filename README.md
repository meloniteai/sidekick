# hud

**A live compass for coding agents. Set a goal, plug in verifiers, watch the orbs.**

`hud` runs alongside Claude Code (or any agent that speaks MCP). You set a
goal in plain English; you plug in verifiers — small rubrics or scripts that
score how far the working tree is from that goal. After each file edit, hud
re-runs them in parallel and renders the results as orbs on a 2D compass.
Each verifier reports a `distance ∈ [0, 1]`: `0` means "satisfied", `1`
means "maximally unsatisfied". The agent reads the compass via the
`hud_status` and `hud_explain` MCP tools and course-corrects mid-session,
instead of finding out at review time.

> Status: hobbyist OSS MVP. Built in Go, talks to Claude Code via hooks +
> a stdio MCP server. No training, no reward model — just verifiers your
> agent reads while it codes.

---

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

- The active goal is set by the agent itself via the `hud_set_goal` MCP
  tool, driven by the bundled [`hud` skill](skills/hud/SKILL.md).
- File-write hooks (`PostToolUse` on `Write|Edit|MultiEdit|NotebookEdit`)
  trigger a debounced batch run of enabled verifiers.
- Verifiers can be native LLM skill checks, binary pass/fail commands, or
  custom commands that speak HUD's stdin/stdout JSON protocol.
- The TUI re-renders the compass every 200 ms.
- In the TUI footer, press the verifier's number key (`1`-`9`, `0` for the
  tenth) to toggle that verifier on or off for future runs.
- Agents call `hud_status` to read the snapshot — it never triggers
  recomputation, only file writes do.

## Three processes, one daemon

| Binary | Role | Lifetime |
|---|---|---|
| `hud start` | Long-running daemon + Bubble Tea TUI. Owns state, runs verifiers. Listens on a repo-scoped socket under `~/.hud/sockets/<fingerprint>.sock` so multiple projects can run side-by-side. | foreground |
| `hud menubar` | macOS menu bar daemon UI. Owns the same state, runner, hooks, and MCP socket as `hud start`, but renders only as a compact status-menu item. | foreground |
| `hud hook <event>` | Spawned by Claude Code or Codex hooks. Reads hook JSON on stdin, posts a normalized event to the daemon, exits. | one-shot |
| `hud mcp` | Spawned by the agent client as an MCP server. Proxies `hud_status` / `hud_explain` to the daemon. | per agent session |
| `hud verifier add <url>` | Fetches a remote SKILL.md or verifier script, pins it by sha256, and registers it in `hud.yaml`. | one-shot |
| `hud verifier add --local` | Interactive wizard that walks through name, direction, type, command/skill, and timeout, then writes a local entry to `hud.yaml`. | one-shot |
| `hud verifier trust ...` | Manages `~/.hud/trust.json` — the trust-on-first-use ledger for remote verifiers. | one-shot |
| `hud verifier list` | Prints the verifiers configured in `hud.yaml` with source provenance. | one-shot |

## Install

```bash
git clone https://github.com/uriahlevy/hud
cd hud
go build -o hud .
# put `hud` somewhere on PATH, e.g.
mv hud ~/bin/
```

## Wire up Claude Code

Drop the contents of [`examples/claude-settings.json`](examples/claude-settings.json)
into your project's `.claude/settings.json` (or the user-level equivalent).
That registers the file-write hook and the `hud` MCP server.

Then install the bundled skill so the agent knows when to call `hud_set_goal`,
`hud_status`, and `hud_explain`:

```bash
cp -R skills/hud ~/.claude/skills/
```

## Wire up Codex

Drop [`examples/codex-hooks.json`](examples/codex-hooks.json) into your
project's `.codex/hooks.json`, and add the MCP server to `.codex/config.toml`:

```toml
[mcp_servers.hud]
command = "hud"
args = ["mcp"]
```

Codex may ask you to review and trust the hooks. The bundled hook config
triggers HUD after `apply_patch` plus Claude-style write/edit tool names.

## Configure verifiers

Drop a `hud.yaml` next to your code. The shipped example registers four
native agent verifiers that load bundled `SKILL.md` rubrics and run `claude -p`:

```yaml
goal_source: prompt
verifiers:
  - name: Architect
    type: agent
    direction: N
    timeout: 90s
    llm:
      agent: claude
      model: haiku
      thinking: low
      skill: ./skills/architect/SKILL.md
  - name: Test
    type: agent
    direction: E
    llm:
      agent: claude
      model: haiku
      thinking: low
      skill: ./skills/test/SKILL.md
  - name: Security
    type: agent
    direction: S
    llm:
      agent: claude
      model: haiku
      thinking: low
      skill: ./skills/security/SKILL.md
  - name: Deployment
    type: agent
    direction: W
    llm:
      agent: claude
      model: haiku
      thinking: low
      skill: ./skills/deployment/SKILL.md
```

For simple pass/fail checks, use `type: binary`; exit code `0` maps to
`distance = 0`, and any non-zero exit maps to `distance = 1`:

```yaml
  - name: Unit Tests
    type: binary
    direction: E
    binary:
      command: ["go", "test", "./..."]
      pass_reason: "unit tests pass"
      fail_reason: "unit tests failed"
```

For fully custom scoring, use `type: command` (or omit `type` for backward
compatibility). [`examples/verifiers/coverage.sh`](examples/verifiers/coverage.sh)
is a deterministic command verifier backed by `go test -cover`.

## Run

```bash
# 1. Start the HUD in a terminal
hud start

# 2. In another terminal, run Claude Code as usual
claude

# 3. Type a prompt. The agent calls hud_set_goal to register the goal.
# 4. As the agent edits files, the compass updates.
# 5. The agent calls hud_status to read the compass and course-correct.
```

You can also poke the daemon directly:

```bash
hud goal "ship the auth module"     # set goal explicitly
hud status                           # print JSON snapshot
echo '{"tool_input":{"file_path":"src/auth.go"}}' | hud hook write
```

On macOS, use the menu bar UI instead of the full terminal UI:

```bash
hud menubar
```

It starts the same daemon, verifier runner, hook listener, and MCP socket as
`hud start`. The status item shows the current overall distance, and its menu
keeps the compact HUD controls: goal, socket/MCP activity, verifier distance
and reason rows, manual trigger, stop current run, and quit.

## Verifier types

`command` verifiers are the protocol-level extension point. They must:

1. Read one line of JSON on stdin. The shape is:
   ```json
   {
     "goal": "...",
     "changed_files": ["...", "..."],
     "last_messages": ["user: ...", "assistant: ..."],
     "verifier_name": "Architect"
   }
   ```
2. Print exactly one JSON object on stdout (additional log lines on
   preceding lines OR trailing lines are tolerated — HUD scans the
   stream for the last balanced object containing `"distance"`):
   ```json
   {"distance": 0.42, "reason": "one short sentence", "status": "ok"}
   ```
   `status` is optional. Set it to `"unknown"` if your verifier ran but
   could not score this run (tooling missing, no diff to evaluate). HUD
   preserves the previous distance instead of fabricating one, and the
   row renders with a distinct `?` badge so agents can disambiguate.
3. Exit zero. Any non-zero exit, missing JSON, or timeout (default 60s)
   pins the verifier at `distance = 1.0` with `status = "error"` and the
   failure reason.

### Score anchors

The runtime injects a 5-point rubric (0.00 / 0.25 / 0.50 / 0.75 / 1.00)
into agent verifier prompts so scores stay comparable across runs and
across verifiers. Free-floating decimals like 0.37 drift between runs
and become noise; the anchors give the agent a discrete scale to
calibrate against. See [`CONTRIBUTING-VERIFIERS.md`](CONTRIBUTING-VERIFIERS.md)
for the per-dimension calibration each bundled skill uses.

`agent` verifiers internalize the old `run.sh` wrapper: HUD loads the
configured `SKILL.md`, appends the active goal, `SESSION_BASE_REF`,
changed files, and the JSON output contract (with score anchors), then
shells out to `claude` or `codex`. Authentication and model access
still stay with the user's installed agent CLI.

Set `agent: custom` to plug in any other LLM CLI via a templated argv:

```yaml
- name: GeminiArchitect
  type: agent
  direction: N
  llm:
    agent: custom
    model: gemini-1.5-flash
    thinking: low
    skill: ./skills/architect/SKILL.md
    custom:
      command: ["my-llm", "--model={{.Model}}", "--effort={{.Thinking}}", "-"]
```

`binary` verifiers receive the same session JSON on stdin and
`SESSION_BASE_REF` in the environment, but HUD scores them purely from
exit code.

## Remote (community) verifiers

Verifiers can be loaded from any HTTPS URL with a sha256 pin. Install
one with:

```bash
hud verifier add https://raw.githubusercontent.com/you/yours/v1/perf.sh \
  --name Performance --direction NE
```

This downloads the file, prints a 20-line preview, prompts for
confirmation, computes the sha256, writes a `source:` block into
`hud.yaml`, and records the approved hash in `~/.hud/trust.json`. On
subsequent loads, HUD verifies the hash from the on-disk cache before
running the script — drift fails loud.

To pin a verifier by hand without going through `add`:

```yaml
# hud.yaml
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

Then approve the hash before `hud start` will execute it:

```bash
hud verifier trust Performance
```

See [`CONTRIBUTING-VERIFIERS.md`](CONTRIBUTING-VERIFIERS.md) for the
full protocol and [`examples/community/`](examples/community/) for
reference verifiers (docs-drift, lint, bench, migration-safety).

## Local verifiers (interactive)

For a verifier that lives entirely in your repo (no URL, no sha256 pin),
use the wizard:

```bash
hud verifier add --local
```

It prompts field-by-field for name, compass direction, type (agent /
command / binary), the per-type config (skill path or command argv),
optional timeout, and optional advisory permissions, then appends the
entry to `hud.yaml`. `--name`, `--direction`, `--type`, and
`--permissions` flags pre-fill defaults if you already know them.

## Layout

```
hud/
├── main.go                       cobra entrypoint
├── cmd/                          subcommands: start, hook, goal, status, mcp, menubar, verifier
├── internal/
│   ├── daemon/                   socket server + shared State
│   ├── hud/                      Bubble Tea TUI (compass + list + sparkline)
│   ├── verifier/                 subprocess runner, debouncer, history
│   ├── mcp/                      stdio MCP server (mark3labs/mcp-go)
│   ├── ipc/                      JSON-line socket protocol shared by all binaries
│   ├── transcript/               tails CC's session JSONL for context
│   ├── fetch/                    content-addressed remote artefact cache
│   ├── trust/                    trust-on-first-use ledger (~/.hud/trust.json)
│   └── config/                   hud.yaml loader (validates at load, fetches remote sources)
├── skills/                       bundled SKILL.md rubrics: architect, test, security, deployment, hud
└── examples/
    ├── hud.yaml                  reference config
    ├── claude-settings.json      Claude Code hook + MCP wiring
    ├── codex-hooks.json          Codex hook wiring
    ├── verifiers/                in-tree command verifiers (coverage.sh, run.sh)
    └── community/                vetted community verifier templates
```

## What's not here yet

- Codex hook integration (native `type: agent` verifiers can call `codex exec`).
- Persistence across sessions (history is in-memory only).
- 8-direction layout when more than 4 verifiers are configured.
- Enforced (vs advisory) per-verifier sandboxing — `permissions:` blocks
  are surfaced in the TUI today; future versions will map them onto
  platform sandboxes.
