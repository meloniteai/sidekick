---
name: sidekick
description: Use the Sidekick compass to keep the agent honest about the active goal. Call `sidekick_set_goal` at the start of a task and whenever the goal materially shifts; call `sidekick_status` after edits to read the verifier compass; call `sidekick_explain` to expand the reason for any verifier with a high distance.
---

# sidekick

`sidekick` is a compass-style Sidekick for agentic coding sessions. It runs as a background daemon and exposes three MCP tools: `sidekick_set_goal`, `sidekick_status`, and `sidekick_explain`. Verifiers (Architect, Test, Security, Deployment, …) each report a `distance ∈ [0, 1]` from the active goal — `0` means satisfied, `1` means maximally unsatisfied.

## When to use

- The user has `sidekick start` running (the daemon must be live for any tool call to succeed).
- You are doing real coding work — editing files, fixing bugs, shipping features. Ignore for pure Q&A.

## How to use

### 1. Set the goal at the start of a task

The first thing you do on a substantive task is call `sidekick_set_goal` with one short sentence describing what you are about to achieve. This sentence is what every verifier evaluates against, so it should describe the *outcome*, not your immediate next action.

Good: `"Add OAuth2 PKCE support to the auth module"`
Bad: `"read auth.go"` (that's a step, not a goal)

**Working in a git worktree? Create or `cd` into it BEFORE calling `sidekick_set_goal`.** The goal call anchors the session to your current working directory, and the write hook only fires verifiers for files under that anchored path. If you set the goal from the repo root and then move work into a worktree (or vice versa), edits land outside the anchor, no verifier runs trigger, and the compass silently goes stale. If you realize mid-task that the work belongs in a different worktree, switch cwd first and call `sidekick_set_goal` again to re-anchor.

Sidekick supports — and encourages — running parallel sessions, one per worktree or one per repo. Each session keeps its own compass scoped to the cwd it was anchored to at `sidekick_set_goal` time, so getting the cwd right on that first call is what lets the parallel sessions coexist without stepping on each other.

Re-set the goal whenever the user pivots, you start a clearly distinct sub-task, you switch worktrees, or you realize the original framing was wrong. Don't reset it on every message — only when the target (or the cwd it's anchored to) genuinely changes.

### 2. Read the compass after meaningful edits

After you write/edit files, call `sidekick_status` to read the snapshot. File edits trigger debounced verifier runs in the background, so the snapshot reflects the latest state without you needing to ask for recomputation. Use the result to decide whether to keep going or course-correct.

`sidekick_status` is read-only and never triggers recomputation. Spamming it is cheap.

### 3. Expand a high-distance verifier with `sidekick_explain`

If `sidekick_status` shows a verifier with a high distance (say, > 0.5), call `sidekick_explain` with that verifier's name to get the full reason. Use that reason to decide your next edit.

## What not to do

- Don't call `sidekick_set_goal` at every turn. The goal should be stable across a task.
- Don't ignore the compass. If a verifier flags a regression in an area outside your immediate change (e.g. you fixed a test but Security spiked), investigate before moving on.
- Don't fabricate verifier names for `sidekick_explain` — only use names that appeared in the last `sidekick_status` reply.
- If `sidekick_set_goal` or `sidekick_status` returns "daemon unreachable," tell the user `sidekick start` isn't running and stop trying.

## Install

Drop this skill into your skills directory:

```bash
cp -R skills/sidekick ~/.claude/skills/
```

And register the MCP server + write hook (see `examples/claude-settings.json`):

```bash
# Merge into your project's .claude/settings.json or user-level settings.
```
