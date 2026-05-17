# AGENTS.md — HUD

## Overview

HUD is a Go CLI tool that renders a live compass-style TUI showing verifier distances from a session goal during agentic coding sessions. It runs alongside Claude Code or Codex, re-evaluating verifiers on file writes and exposing the scores via MCP tools (`hud_status`, `hud_explain`, `hud_set_goal`).

## Essential commands

```bash
# Build
go build -o hud .

# Run all tests
go test ./...

# Run tests for a single package
go test ./internal/hud/...
go test ./internal/verifier/...

# Lint (gopls static analysis via LSP integration — no separate linter config)
# Format
go fmt ./...
```

No Makefile, no CI configs. The project uses Go modules (`go 1.26.3`).

## Architecture: three processes, one daemon

The project compiles to a single `hud` binary with subcommands:

| Subcommand | Role |
|---|---|
| `hud start` | Long-running daemon + Bubble Tea TUI. Owns state, runs verifiers, listens on a Unix socket. |
| `hud menubar` | macOS menu bar variant. Same daemon but renders as a compact menubar item. |
| `hud hook <event>` | Spawned by agent hooks (Claude Code / Codex). Posts normalized write events to the daemon over the socket, exits. |
| `hud mcp` | Spawned by the agent as an MCP stdio server. Proxies `hud_status`/`hud_explain`/`hud_set_goal` to the daemon socket. |
| `hud goal <text>` | CLI shortcut to set the session goal. |
| `hud status` | CLI shortcut to read the JSON snapshot. |
| `hud verifier add|trust|list` | Manage verifiers (remote install, trust ledger, listing). |

### Data flow

```
Agent (Claude/Codex)
  ├─ stdout → edits files
  ├─ hooks → `hud hook write` → IPC socket → daemon State → Runner (debounced batch)
  └─ MCP tools → `hud mcp` → IPC socket → daemon State (read-only snapshot)
                                                      ↓
                                              Bubble Tea TUI (re-renders every 133ms tick)
```

**Key invariant**: Only file-write hooks trigger verifier recomputation. The MCP tools are read-only — they never run verifiers. The `hud_set_goal` MCP tool only updates the goal string.

### Socket protocol

The daemon listens on a repo-scoped Unix socket under `~/.hud/sockets/<sha256-fingerprint>.sock`. Communication is line-delimited JSON (one request, one response, then close). Request types: `write`, `goal`, `status`, `explain`, `ping`. The `Source` field (`"mcp"` or empty) distinguishes MCP traffic for the TUI header.

### Verifier runner

File-write events go through a **debounced batch runner** (`internal/verifier/runner.go`). The default quiet period is 2s — bursts of writes are coalesced. After the quiet window elapses, all enabled verifiers run concurrently (each in its own goroutine). Results are written into `daemon.State` via `UpsertVerifier`.

Three verifier types:
- **command**: reads session JSON on stdin, writes `{"distance", "reason", "status"}` JSON on stdout
- **agent**: loads a `SKILL.md` rubric, assembles a prompt, shells out to `claude -p` (or `codex exec`)
- **binary**: maps exit code (0 = pass, non-zero = fail) to distance 0/1

## Code organization

```
hud/
├── main.go                    # Cobra entrypoint, embeds version file
├── cmd/                       # Cobra subcommands
│   ├── root.go                # Root command assembly
│   ├── start.go               # Daemon init: config load, socket listen, TUI program
│   ├── hook.go                # Hook event parsing (extracts file paths from arbitrary JSON)
│   ├── mcp.go                 # MCP server launcher
│   ├── goal.go, status.go     # CLI shorthands (IPC to daemon)
│   ├── menubar.go             # macOS menubar variant
│   └── verifier.go            # verifier add/trust/list subcommands
├── internal/
│   ├── daemon/                # Unix socket server + shared State
│   │   ├── socket.go          # net.Listen("unix", ...), JSON-line dispatch
│   │   └── state.go           # Mutex-guarded State: goal, verifiers, event log, session edits
│   ├── hud/                   # Bubble Tea TUI
│   │   ├── model.go           # tea.Model: tick loop, key bindings, orb spring physics
│   │   ├── view.go            # Render: compass grid, verifier list, event log, git panel
│   │   ├── layout.go          # Polar-to-grid projection, directionAngle map
│   │   ├── editor.go          # In-TUI config editor (edit/create wizard)
│   │   ├── picker.go          # Verifier picker on startup
│   │   ├── status_wizard.go   # Per-verifier detail view
│   │   └── flare.go           # Animated brand wordmark + color utilities
│   ├── verifier/              # Verifier types + subprocess runner
│   │   ├── verifier.go        # Verifier struct, Verify dispatch, agent prompt builder
│   │   └── runner.go          # Debounced batch runner, history ring buffer
│   ├── config/                # hud.yaml loader, resolver, validator
│   ├── ipc/                   # JSON-line socket protocol types + Send helper
│   ├── mcp/                   # MCP stdio server (mark3labs/mcp-go)
│   ├── gitstats/              # Shells out to git for workspace metadata
│   ├── fetch/                 # Content-addressed remote artifact cache
│   ├── trust/                 # Trust-on-first-use ledger (~/.hud/trust.json)
│   ├── transcript/            # Tails Claude Code session JSONL for verifier context
│   └── menubar/               # macOS menu bar app (darwin-specific)
├── skills/                    # Bundled SKILL.md rubrics: architect, test, security, deployment, agents-md, hud
├── verifiers/                 # In-tree example scripts: coverage.sh, run.sh, dummy.sh
├── examples/                  # Reference hud.yaml, claude-settings.json, codex-hooks.json, community verifiers
└── version                    # Single-line version string (embed at build)
```

## Key patterns and conventions

### Bubble Tea TUI

- The TUI renders at **133ms tick interval** — pulls fresh `State.Snapshot()` every tick, no broadcast plumbing needed.
- The model holds **callback functions** wired by `cmd/start.go` (manual trigger, toggle verifier, stop all, config reload) — the TUI package never imports the runner or daemon directly.
- **Orb physics**: verifier positions use a critically damped harmonic spring (`charmbracelet/harmonica`) so orbs glide smoothly when distance/direction changes. On first observation, position snaps without spring.
- **Arrow animations**: when a verifier's `ComputedAt` timestamp advances, an arrow sweeps along the axis for 5 ticks (~665ms). While a verifier subprocess is running, a calibrating ping-pong animation plays.
- **Modal screens**: the main grid view, editor wizard, and status wizard are mutually exclusive — `Update` delegates to the active sub-model.

### State management

- `daemon.State` is a single mutex-guarded in-memory struct. Everything (goal, verifier statuses, event log, session edits) lives here.
- `Snapshot()` returns a **deep copy** — callers never hold references to internal maps.
- The event log uses `charmbracelet/log` writing to a custom `eventLogSink` that captures pre-styled lines for the TUI log panel. Buffer capped at 500 entries.
- Verifier history is a ring buffer of 32 `HistoryPoint` structs per verifier.

### Config loading

- `config.Load(path)` walks upward from cwd to find `hud.yaml` if no path given.
- `config.Resolve()` validates structurally, checks remote trust, fetches/caches remote artifacts, resolves local paths, and returns runtime `verifier.Verifier` instances.
- When no `hud.yaml` exists, `hud start` falls back to **demo verifiers** that emit placeholder scores.

### Verifier scoring

- Distance ∈ `[0, 1]`: 0 = goal satisfied, 1 = maximally far.
- Score anchors (0.00, 0.25, 0.50, 0.75, 1.00) are injected into agent prompts for consistency.
- `status: "unknown"` is a special outcome: the verifier ran but couldn't score. HUD **preserves the previous distance** instead of fabricating one.
- Agent verifiers parse Claude's `--output-format=json` envelope, unwrapping the `result` field and harvesting usage telemetry.

### File path extraction from hooks

The hook handler (`cmd/hook.go`) must extract file paths from arbitrary JSON (Claude Code and Codex use different shapes). It recursively walks JSON, matching keys like `file_path`, `absolute_file_path`, `notebook_path`, and also parses patch text for `*** Add File:` markers.

### Platform-specific code

- `internal/menubar/menu_darwin.go` + `menu_darwin.m` — Objective-C menubar integration, macOS only.
- `menu_other.go` — stub for non-macOS.
- No build tags needed; the files use `_darwin` suffix convention.

## Testing patterns

- Tests use Go's standard `testing` package. No external test frameworks.
- Many tests create a `daemon.State`, seed verifiers via `UpsertVerifier`, and verify `Snapshot()` output.
- Verifier runner tests create a `context.Background()` runner, call `RunNow()` (synchronous variant of `runBatch`), and assert on state.
- The `test_log_style_test.go` convention: some packages use `_test` package suffix (external tests), others same-package (internal tests).
- No test fixtures or mocks — tests shell out to real `echo` commands for command verifiers.

## Gotchas

- **Verifier subprocess hang**: The runner sets `cmd.WaitDelay = 500ms` to bound post-cancellation drain. Without this, `cmd.Wait` can hang on macOS when pipes aren't fully drained after SIGTERM.
- **stdin drain**: Custom verifiers MUST drain stdin (`cat >/dev/null`) even if they don't use it, or the pipe write blocks.
- **HUD_VERIFIER=1**: Set in the environment for all verifier subprocesses. Hooks check this to avoid recursing on writes triggered by verifiers themselves.
- **`SESSION_BASE_REF`**: Captured at `hud start` time via `git rev-parse HEAD`. Verifiers diff against this, NOT against the last write. This is critical — `git diff $SESSION_BASE_REF` shows cumulative session work.
- **No persistence**: All state (verifier history, event log, session edits) is in-memory only. Restarting the daemon loses everything.
- **`go mod tidy` is flaky with `go 1.26.3`**: The `go.mod` specifies a pre-release Go version. If you get module errors, check your Go toolchain version.
- **Terminal escape handling**: `charmbracelet/log` auto-detects TTY support. Since the event log sink is a plain `io.Writer` (not a terminal), `NewState()` explicitly sets `SetColorProfile(termenv.TrueColor)` so ANSI escapes are emitted.
- **Brace-aware JSON parsing**: `findLastDistanceObject()` in `verifier/verifier.go` is a custom brace/string-aware scanner that finds the last JSON object containing `"distance"` in arbitrary output. This replaces a regex that silently dropped objects with nested braces.
- **Dropped writes during KillBatch**: When the user presses ESC to stop a batch, any writes that accumulated during the kill are discarded (not immediately rescheduled).
