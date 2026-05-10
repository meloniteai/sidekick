# hud

A live, compass-style HUD for agentic coding sessions.

`hud` runs alongside Claude Code (or any agent that speaks MCP) and renders
each verifier — Architect, Test, Security, Deployment, … — on a 2D compass.
Each verifier reports a `distance ∈ [0, 1]` from the goal: `0` means
"satisfied", `1` means "maximally unsatisfied". As the agent edits files
the HUD re-evaluates, and the agent itself can read the result via the
`hud_status` and `hud_explain` MCP tools to course-correct.

> Status: hobbyist OSS MVP. Built in Go, talks to Claude Code via hooks +
> a stdio MCP server.

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
| `hud start` | Long-running daemon + Bubble Tea TUI. Owns state, runs verifiers. Listens on `~/.hud/sock`. | foreground |
| `hud menubar` | macOS menu bar daemon UI. Owns the same state, runner, hooks, and MCP socket as `hud start`, but renders only as a compact status-menu item. | foreground |
| `hud hook <event>` | Spawned by Claude Code or Codex hooks. Reads hook JSON on stdin, posts a normalized event to the daemon, exits. | one-shot |
| `hud mcp` | Spawned by the agent client as an MCP server. Proxies `hud_status` / `hud_explain` to the daemon. | per agent session |

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
   preceding lines are tolerated):
   ```json
   {"distance": 0.42, "reason": "one short sentence"}
   ```
3. Exit zero. Any non-zero exit, missing JSON, or timeout (default 60s)
   pins the verifier at `distance = 1.0` with the failure reason.

`llm` verifiers internalize the old `run.sh` wrapper: HUD loads the configured
`SKILL.md`, appends the active goal, `SESSION_BASE_REF`, changed files, and the
JSON output contract, then shells out to `claude` or `codex`. Authentication
and model access still stay with the user's installed agent CLI.

`binary` verifiers receive the same session JSON on stdin and
`SESSION_BASE_REF` in the environment, but HUD scores them purely from exit
code.

## Layout

```
hud/
├── main.go                       cobra entrypoint
├── cmd/                          subcommands: start, hook, goal, status, mcp
├── internal/
│   ├── daemon/                   socket server + shared State
│   ├── hud/                      Bubble Tea TUI (compass + list)
│   ├── verifier/                 subprocess runner, debouncer
│   ├── mcp/                      stdio MCP server (mark3labs/mcp-go)
│   ├── ipc/                      JSON-line socket protocol shared by all binaries
│   ├── transcript/               tails CC's session JSONL for context
│   └── config/                   hud.yaml loader
└── examples/                     hud.yaml, claude-settings.json, verifier scripts
```

## What's not here yet

- Codex hook integration (native `type: agent` verifiers can call `codex exec`).
- Persistence across sessions.
- Multi-project / multi-session daemon multiplexing.
- 8-direction layout when more than 4 verifiers are configured.
