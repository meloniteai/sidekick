//go:build e2e

// Package installer holds the Docker-driven e2e tests for install.sh and
// `sidekick install`. They live in a sub-package so the heavyweight Docker
// build/run only runs when this sub-suite is exercised, and so the docker
// image-build sync.Once is scoped to these tests.
package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// ensureDocker fails the test (rather than skipping) if `docker` isn't on
// PATH or the daemon isn't reachable. We want missing-Docker in CI to be
// loud, not silently green.
func ensureDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI not on PATH: %v (installer e2e tests need Docker)", err)
	}
	cmd := exec.Command("docker", "info")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker daemon not reachable: %v\n%s", err, out)
	}
}

var (
	imageOnce sync.Once
	imageTag  string
	imageErr  error
)

// BuildImage builds the test Dockerfile once per `go test` invocation,
// tagged by the Dockerfile's sha256 so repeated runs (and incidental
// rebuilds during dev) only pay the build cost on actual changes.
func BuildImage(t *testing.T) string {
	t.Helper()
	ensureDocker(t)
	imageOnce.Do(func() {
		dir := dockerDir()
		dockerfile := filepath.Join(dir, "Dockerfile")
		raw, err := os.ReadFile(dockerfile)
		if err != nil {
			imageErr = fmt.Errorf("read Dockerfile: %w", err)
			return
		}
		sum := sha256.Sum256(raw)
		tag := "sidekick-e2e-installer:" + hex.EncodeToString(sum[:])[:12]
		cmd := exec.Command("docker", "build", "-q", "-t", tag, dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			imageErr = fmt.Errorf("docker build: %v\n%s", err, out)
			return
		}
		imageTag = tag
	})
	if imageErr != nil {
		t.Fatalf("build image: %v", imageErr)
	}
	return imageTag
}

func dockerDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// RunInContainer runs `bash -lc <script>` inside a one-shot container of
// the built image. Returns combined stdout/stderr and the exec error.
func RunInContainer(t *testing.T, script string) (string, error) {
	t.Helper()
	tag := BuildImage(t)
	cmd := exec.Command("docker", "run", "--rm", tag, "bash", "-lc", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunInstallScript copies the *current branch's* install.sh into a clean
// container and pipes it into bash with the supplied environment variables
// prepended. The script still talks to real github.com for release
// artifacts — only install.sh itself is sourced locally, so the PR's
// changes to install.sh are gated by these tests.
func RunInstallScript(t *testing.T, envLine string, postCommands string) (string, error) {
	t.Helper()
	tag := BuildImage(t)
	script := fmt.Sprintf(`set -euo pipefail
%s bash /work/install.sh
%s`, envLine, postCommands)
	cmd := exec.Command("docker", "run", "--rm",
		"-v", filepath.Join(repoRoot(), "install.sh")+":/work/install.sh:ro",
		tag, "bash", "-lc", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// repoRoot returns the absolute path of the sidekick repo root, walking up
// from this file. e2e/installer/installer.go -> e2e/installer -> e2e -> repo
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
