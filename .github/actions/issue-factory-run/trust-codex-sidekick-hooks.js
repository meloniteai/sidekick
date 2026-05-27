#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const { spawn } = require("child_process");

const workspace = process.env.WORKSPACE;
const home = process.env.HOME;
const artifactDir = process.env.ARTIFACT_DIR;
const timeoutMs = Number(process.env.CODEX_HOOK_LIST_TIMEOUT_MS || "30000");

if (!workspace) {
  fail("WORKSPACE is required");
}
if (!home) {
  fail("HOME is required");
}

function fail(message) {
  console.error(`::error::${message}`);
  process.exit(1);
}

function tomlString(value) {
  return JSON.stringify(String(value));
}

function hookListFor(cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(
      "codex",
      ["app-server", "--enable", "hooks", "--listen", "stdio://"],
      {
        env: process.env,
        stdio: ["pipe", "pipe", "pipe"],
      },
    );

    let stdout = "";
    let stderr = "";
    let settled = false;
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      child.kill();
      reject(new Error(`timed out after ${timeoutMs}ms waiting for Codex hooks/list`));
    }, timeoutMs);

    function finish(err, value) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      child.kill();
      if (err) reject(err);
      else resolve(value);
    }

    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });

    child.stdout.on("data", (chunk) => {
      stdout += chunk;
      let newline;
      while ((newline = stdout.indexOf("\n")) >= 0) {
        const line = stdout.slice(0, newline).trim();
        stdout = stdout.slice(newline + 1);
        if (!line) continue;
        let message;
        try {
          message = JSON.parse(line);
        } catch {
          continue;
        }
        if (message.id !== 2) continue;
        if (message.error) {
          finish(new Error(`Codex hooks/list failed: ${JSON.stringify(message.error)}`));
          return;
        }
        finish(null, { result: message.result, stderr });
      }
    });

    child.on("error", (error) => {
      finish(error);
    });
    child.on("exit", (code, signal) => {
      if (!settled) {
        finish(new Error(`Codex app-server exited before hooks/list (code=${code}, signal=${signal}): ${stderr}`));
      }
    });

    child.stdin.write(JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        clientInfo: {
          name: "sidekick_issue_factory",
          title: "Sidekick issue factory",
          version: "0",
        },
      },
    }) + "\n");
    child.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "initialized", params: {} }) + "\n");
    child.stdin.write(JSON.stringify({
      jsonrpc: "2.0",
      id: 2,
      method: "hooks/list",
      params: { cwds: [cwd] },
    }) + "\n");
  });
}

(async () => {
  const { result, stderr } = await hookListFor(workspace);
  if (artifactDir) {
    fs.mkdirSync(artifactDir, { recursive: true });
    fs.writeFileSync(
      path.join(artifactDir, "codex-hooks-list.json"),
      JSON.stringify(result, null, 2) + "\n",
    );
    if (stderr.trim()) {
      fs.writeFileSync(path.join(artifactDir, "codex-hooks-list.err"), stderr);
    }
  }

  const entries = Array.isArray(result && result.data) ? result.data : [];
  const hooks = entries.flatMap((entry) => Array.isArray(entry.hooks) ? entry.hooks : []);
  const sidekickHooks = hooks.filter((hook) => hook.command === "sidekick hook write");

  if (sidekickHooks.length === 0) {
    fail("Codex discovered no Sidekick hooks to trust.");
  }

  const missingHash = sidekickHooks.filter((hook) => typeof hook.key !== "string" || typeof hook.currentHash !== "string");
  if (missingHash.length > 0) {
    fail("Codex Sidekick hook discovery did not return stable key/hash data.");
  }

  const configPath = path.join(home, ".codex", "config.toml");
  fs.mkdirSync(path.dirname(configPath), { recursive: true });
  let toml = "";
  if (!fs.existsSync(configPath) || !fs.readFileSync(configPath, "utf8").includes("[features]")) {
    toml += "\n[features]\nhooks = true\n";
  }
  for (const hook of sidekickHooks) {
    toml += `\n[hooks.state.${tomlString(hook.key)}]\ntrusted_hash = ${tomlString(hook.currentHash)}\n`;
  }
  fs.appendFileSync(configPath, toml, { mode: 0o600 });

  console.log(`Trusted ${sidekickHooks.length} Sidekick Codex hook(s).`);
})().catch((error) => {
  fail(error && error.message ? error.message : String(error));
});
