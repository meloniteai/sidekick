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
  trigger a debounced batch run of every configured verifier.
- Each verifier is just a command. It receives a one-line JSON session blob
  on stdin (goal, changed files, recent agent messages) and writes
  `{"distance": <0..1>, "reason": "..."}` on stdout.
- The TUI re-renders the compass every 200 ms.
- Agents call `hud_status` to read the snapshot — it never triggers
  recomputation, only file writes do.

## Three processes, one daemon

| Binary | Role | Lifetime |
|---|---|---|
| `hud start` | Long-running daemon + Bubble Tea TUI. Owns state, runs verifiers. Listens on `~/.hud/sock`. | foreground |
| `hud hook <event>` | Spawned by Claude Code hooks. Reads CC's hook JSON on stdin, posts a normalized event to the daemon, exits. | one-shot |
| `hud mcp` | Spawned by Claude Code as an MCP server. Proxies `hud_status` / `hud_explain` to the daemon. | per CC session |

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

## Configure verifiers

Drop a `hud.yaml` next to your code. The shipped example registers four
`claude -p`-backed subagent verifiers:

```yaml
goal_source: prompt
verifiers:
  - name: Architect
    direction: N
    command: ["./examples/verifiers/architect.sh"]
    timeout: 90s
  - name: Test
    direction: E
    command: ["./examples/verifiers/test.sh"]
  - name: Security
    direction: S
    command: ["./examples/verifiers/security.sh"]
  - name: Deployment
    direction: W
    command: ["./examples/verifiers/deployment.sh"]
```

Replace any of them with your own scripts — a verifier is just a command
that prints `{"distance": <0..1>, "reason": "..."}` on stdout.
[`examples/verifiers/coverage.sh`](examples/verifiers/coverage.sh) is a
deterministic example backed by `go test -cover`.

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

## Verifier protocol

A verifier is a command. It must:

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

`hud` itself never calls a model API directly — it shells out. That keeps
authentication and model choice with the user's existing tools.

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

- Codex hook integration (verifier scripts can call `codex exec` today).
- Persistence across sessions.
- Multi-project / multi-session daemon multiplexing.
- 8-direction layout when more than 4 verifiers are configured.
