//go:build e2e

package installer

import (
	"regexp"
	"strings"
	"testing"
)

// TestInstallScriptDropsBinary runs the branch's install.sh inside a
// clean Ubuntu container with no pre-existing sidekick binary, no agent
// CLIs, and SIDEKICK_SKIP_AGENTS=1 so the post-install agent wiring step
// is a no-op (T7 covers the agent path). We then assert that the binary
// lands on PATH and reports a sane semver from `sidekick --version`.
//
// The install.sh under test is the local copy from the current branch —
// changes to the script are gated by this test. Release artifacts (the
// tarball + checksums) are still fetched from the real github.com so the
// download/verify/install plumbing is genuinely exercised end-to-end.
func TestInstallScriptDropsBinary(t *testing.T) {
	out, err := RunInstallScript(t,
		"SIDEKICK_SKIP_AGENTS=1",
		`command -v sidekick
sidekick --version`,
	)
	if err != nil {
		t.Fatalf("install.sh run failed: %v\n--- output ---\n%s", err, out)
	}

	if !strings.Contains(out, "sidekick") {
		t.Fatalf("expected sidekick path in output:\n%s", out)
	}

	// `sidekick --version` should print at least one line like "0.0.11" or
	// "v0.0.11". The version file is just digits.dots, so a loose match is
	// safer than pinning a format.
	semver := regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)
	if !semver.MatchString(out) {
		t.Fatalf("no semver in --version output:\n%s", out)
	}
}
