package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIssueFactorySecurityControls(t *testing.T) {
	action := readText(t, ".github/actions/issue-factory-run/action.yml")
	factoryAct := readText(t, "scripts/factory-act")

	requiredActionSnippets := []string{
		"codex-sandbox=danger-full-access is not allowed",
		"<untrusted-issue-body format=\"json-string\">",
		"-c approval_policy=never",
		"-c sandbox_workspace_write.network_access=false",
		"--dangerously-bypass-hook-trust",
		"command !== \"sidekick hook write\"",
		"--add-dir \"$HOME/.sidekick/sockets\"",
		"sidekick-status-after-codex-hooks.json",
		"require-sidekick-hook-verifiers",
	}
	for _, snippet := range requiredActionSnippets {
		if !strings.Contains(action, snippet) {
			t.Fatalf("issue factory action missing security control %q", snippet)
		}
	}

	if strings.Contains(factoryAct, "codex-sandbox: danger-full-access") {
		t.Fatal("factory-act must not rewrite live runs back to danger-full-access")
	}
	if strings.Contains(action, "sandbox_workspace_write.network_access=true") {
		t.Fatal("issue factory must not enable outbound network access inside Codex workspace-write")
	}
	if strings.Contains(action, "--add-dir \"$HOME/.sidekick\"") {
		t.Fatal("issue factory must not expose the full Sidekick home directory to Codex")
	}
	if strings.Contains(factoryAct, "security-opt seccomp=unconfined") {
		t.Fatal("factory-act live mode should not require disabling the Docker seccomp profile")
	}
}

func TestAdversarialIssueFactoryFixture(t *testing.T) {
	var event struct {
		Issue struct {
			Body string `json:"body"`
		} `json:"issue"`
	}
	data := readBytes(t, "examples/github-issue-factory/adversarial-issue-event.json")
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("parse adversarial fixture: %v", err)
	}

	for _, needle := range []string{
		"$HOME/.codex/auth.json",
		"$HOME/.sidekick/auth.json",
		"POST",
		"danger-full-access",
	} {
		if !strings.Contains(event.Issue.Body, needle) {
			t.Fatalf("adversarial fixture body missing %q", needle)
		}
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	return string(readBytes(t, path))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
