# GitHub Issue Factory

The issue factory packages the Sidekick-owned Codex loop as two release assets:

- `.github/actions/issue-factory-run/action.yml` runs one instrumented session.
- `.github/workflows/codex-issue-factory.yml` exposes gate, implement, and publish jobs through `workflow_call`.

Consuming repositories keep a tiny issue workflow that passes issue metadata,
project setup, a Sidekick config path, and auth secrets. The Sidekick repo keeps
the boilerplate.

## Local act Loop

Install the local runner and make sure Docker is running:

```bash
brew install act
docker info
```

Run the stub smoke:

```bash
make factory-act
```

The script copies the current Sidekick checkout into an isolated temp clone
under `.factory-act/`, commits that snapshot inside the temp clone, runs `act` against
`examples/github-issue-factory/local-issue-factory.yml`, and enables the local
artifact server with `--artifact-server-path`. The source checkout should remain
clean except for ignored `.factory-act/` output.

Stub mode writes a deterministic `codex-factory-stub-output.md`, triggers the
Sidekick write hook, uploads implementation artifacts, and dry-runs publishing.
Expected artifacts include:

- `codex.patch`
- `sidekick-status.json`
- `codex-final-message.md`
- `pr-body.md`
- `issue-comment.md`

For a real local run:

```bash
make factory-act-live
```

Live mode reads local `~/.codex/auth.json` and `~/.sidekick/auth.json`, injects
them into act through a temporary secret file under `.factory-act/`, and runs a
generated act-only workflow copy with Codex set to `live`, backend telemetry
enabled, and `danger-full-access` inside the isolated act container. The
checked-in example remains stub-mode, and the reusable workflow still defaults
to `workspace-write`; the local override avoids nested Docker/bubblewrap
namespace failures.

## Consuming Workflow

See `examples/github-issue-factory/consumer-workflow.yml` for the small caller
shape:

```yaml
jobs:
  factory:
    permissions:
      contents: write
      issues: write
      pull-requests: write
    uses: meloniteai/sidekick/.github/workflows/codex-issue-factory.yml@main
    with:
      issue-number: ${{ github.event.issue.number }}
      issue-title: ${{ github.event.issue.title }}
      issue-body: ${{ github.event.issue.body }}
      issue-url: ${{ github.event.issue.html_url }}
      issue-author-association: ${{ github.event.issue.author_association }}
      project-setup-command: npm ci
      sidekick-config-path: .sidekick/sidekick.yaml
      telemetry-mode: backend
      codex-mode: live
      publish-mode: live
    secrets:
      CODEX_AUTH_JSON: ${{ secrets.CODEX_AUTH_JSON }}
      SIDEKICK_AUTH_JSON: ${{ secrets.SIDEKICK_AUTH_JSON }}
```

By default the gate allows `OWNER,MEMBER,COLLABORATOR`. Override
`allowed-author-associations` when a repo wants a broader or narrower policy.

## Secrets

`CODEX_AUTH_JSON` should contain the full contents of `~/.codex/auth.json`.
`SIDEKICK_AUTH_JSON` should contain the full contents of
`~/.sidekick/auth.json` when using backend telemetry. `CODEX_FACTORY_GH_TOKEN`
is optional; the workflow falls back to `github.token` for live publishing.

## Known act Differences

`act` is a Docker emulation of GitHub Actions, not GitHub Actions itself. The
factory harness keeps the important edges close by using an issue event fixture,
explicit workflow file, local artifact server, and the same reusable workflow.
Differences to expect:

- hosted runner images are approximated by the act image you choose;
- artifact upload/download requires `--artifact-server-path`;
- secret handling comes from local files or `--secret` flags;
- dry-run publish writes markdown artifacts instead of calling GitHub APIs.

References:

- GitHub reusable workflows: https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows
- GitHub composite actions: https://docs.github.com/en/actions/sharing-automations/creating-actions/creating-a-composite-action
- act usage guide: https://nektosact.com/usage/
