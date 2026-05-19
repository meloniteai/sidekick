---
name: e2e-minimal
description: A deterministic verifier for the e2e suite. Always returns a fixed pass score so the test only validates plumbing, not model judgement.
---

# E2E minimal verifier

You are running inside Sidekick's end-to-end test suite. Your single job is
to print exactly one line of JSON to stdout and then stop. Do not call any
tools, do not read any files, do not run any commands.

Print this line verbatim:

```
{"distance":0.0,"reason":"e2e-ok","status":"ok"}
```

That's it. Output nothing else — no commentary, no markdown fences in your
response, no explanation. The Sidekick runner parses the last JSON object on
stdout that contains a `distance` field, so any extra prose risks confusing
the parser.
