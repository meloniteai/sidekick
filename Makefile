.PHONY: test e2e e2e-agents factory-act factory-act-collaborator factory-act-contributor factory-act-live

test:
	go test ./...

# Full e2e suite. Includes installer tests (T6/T7) which require Docker;
# those tests t.Fatal with a clear message if `docker` is missing so a
# misconfigured CI runner fails loudly rather than silently skipping.
e2e:
	go test -tags=e2e -count=1 -v ./e2e/...

# Agent-verifier e2e (T2). Real claude/codex CLIs against haiku-tier models.
# Requires ANTHROPIC_API_KEY and OPENAI_API_KEY in the environment.
e2e-agents:
	SIDEKICK_E2E_REAL_AGENT=1 go test -tags=e2e -count=1 -run 'AgentVerifier' -v ./e2e/

factory-act:
	scripts/factory-act

factory-act-collaborator:
	scripts/factory-act --event examples/github-issue-factory/issue-event-collaborator.json

factory-act-contributor:
	scripts/factory-act --event examples/github-issue-factory/issue-event-contributor.json

factory-act-live:
	scripts/factory-act --live
