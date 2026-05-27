#!/usr/bin/env node

const fs = require("fs");

const statusPath = process.argv[2];
const status = JSON.parse(fs.readFileSync(statusPath, "utf8"));
const verifiers = Array.isArray(status.verifiers) ? status.verifiers : [];

function hasVerifierActivity(verifier) {
  if (verifier && verifier.running === true) {
    return true;
  }
  const computedAt = verifier && typeof verifier.computed_at === "string" ? verifier.computed_at : "";
  if (computedAt && !computedAt.startsWith("0001-01-01")) {
    return true;
  }
  return Array.isArray(verifier && verifier.history) && verifier.history.length > 0;
}

if (!verifiers.some(hasVerifierActivity)) {
  console.error(
    "::error::Codex changed files, but no Sidekick verifier started before the fallback notifier. " +
      "Codex file-write hooks may be untrusted or unable to reach the Sidekick socket."
  );
  process.exit(1);
}

