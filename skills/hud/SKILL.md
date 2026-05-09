---
name: hud
description: Use the HUD compass to keep the agent honest about the active goal. Call `hud_set_goal` at the start of a task and whenever the goal materially shifts; call `hud_status` after edits to read the verifier compass; call `hud_explain` to expand the reason for any verifier with a high distance.
---

# hud

`hud` is a compass-style HUD for agentic coding sessions. It runs as a background daemon and exposes three MCP tools: `hud_set_goal`, `hud_status`, and `hud_explain`. Verifiers (Architect, Test, Security, Deployment, …) each report a `distance ∈ [0, 1]` from the active goal — `0` means satisfied, `1` means maximally unsatisfied.

## When to use

- The user has `hud start` running (the daemon must be live for any tool call to succeed).
- You are doing real coding work — editing files, fixing bugs, shipping features. Ignore for pure Q&A.

## How to use

### 1. Set the goal at the start of a task

The first thing you do on a substantive task is call `hud_set_goal` with one short sentence describing what you are about to achieve. This sentence is what every verifier evaluates against, so it should describe the *outcome*, not your immediate next action.

Good: `"Add OAuth2 PKCE support to the auth module"`
Bad: `"read auth.go"` (that's a step, not a goal)

Re-set the goal whenever the user pivots, you start a clearly distinct sub-task, or you realize the original framing was wrong. Don't reset it on every message — only when the target genuinely changes.

### 2. Read the compass after meaningful edits

After you write/edit files, call `hud_status` to read the snapshot. File edits trigger debounced verifier runs in the background, so the snapshot reflects the latest state without you needing to ask for recomputation. Use the result to decide whether to keep going or course-correct.

`hud_status` is read-only and never triggers recomputation. Spamming it is cheap.

### 3. Expand a high-distance verifier with `hud_explain`

If `hud_status` shows a verifier with a high distance (say, > 0.5), call `hud_explain` with that verifier's name to get the full reason. Use that reason to decide your next edit.

## What not to do

- Don't call `hud_set_goal` at every turn. The goal should be stable across a task.
- Don't ignore the compass. If a verifier flags a regression in an area outside your immediate change (e.g. you fixed a test but Security spiked), investigate before moving on.
- Don't fabricate verifier names for `hud_explain` — only use names that appeared in the last `hud_status` reply.
- If `hud_set_goal` or `hud_status` returns "daemon unreachable," tell the user `hud start` isn't running and stop trying.

## Install

Drop this skill into your skills directory:

```bash
cp -R skills/hud ~/.claude/skills/
```

And register the MCP server + write hook (see `examples/claude-settings.json`):

```bash
# Merge into your project's .claude/settings.json or user-level settings.
```
