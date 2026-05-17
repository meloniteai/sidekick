# Community verifiers

This directory holds vetted reference implementations of Sidekick verifiers
that go beyond the built-in compass quartet (Architect / Test / Security
/ Deployment). They are intended as:

1. **Drop-in additions** for users who want extra dimensions on their
   compass without writing a verifier from scratch.
2. **Templates** for authors building their own verifiers — copy the one
   closest to what you need and adapt.

Each verifier here passes the contract documented in
[`CONTRIBUTING-VERIFIERS.md`](../../CONTRIBUTING-VERIFIERS.md): reads
session JSON on stdin, emits a single `{"distance": ..., "reason": ...}`
JSON line on stdout, exits 0.

| Verifier | Type | What it scores |
|---|---|---|
| [`coverage.sh`](../verifiers/coverage.sh) | command | Go test coverage best-package percent (already shipped in `examples/`) |
| [`docs-drift.sh`](docs-drift.sh) | command | Whether the diff updated `*.md` alongside changed source code |
| [`lint.sh`](lint.sh) | binary | Pass/fail on the project's lint tool (gofmt, eslint, ruff — auto-detected) |
| [`bench.sh`](bench.sh) | command | Whether `go test -bench` regressed against the session base ref |
| [`migration-safety/SKILL.md`](migration-safety/SKILL.md) | agent | Reviews schema migrations for rollback safety, online-DDL fitness, and downstream coordination |

## Try one

```yaml
# sidekick.yaml
verifiers:
  - name: Docs
    type: command
    direction: NE
    timeout: 30s
    command: ["./examples/community/docs-drift.sh"]
    permissions:
      filesystem: read-only
      network: false
```

Or fetch the same script from a fork by URL with a sha256 pin (no copy
required):

```bash
sidekick verifier add https://example.com/community/docs-drift.sh \
  --name Docs --direction NE
```
