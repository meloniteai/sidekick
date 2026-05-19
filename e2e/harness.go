//go:build e2e

// Package e2e contains end-to-end tests that exercise the sidekick binary
// (and, for installer tests, install.sh inside Docker). All tests in this
// package are guarded by the `e2e` build tag so `go test ./...` ignores
// them; run them via `make e2e`.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/meloniteai/sidekick/internal/ipc"
)

var (
	buildOnce   sync.Once
	builtBinary string
	buildErr    error
)

// BuildBinary builds the sidekick binary once per `go test` invocation and
// returns the absolute path. Subsequent calls reuse the cached path.
func BuildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sidekick-e2e-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "sidekick")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		// `go build ./` from repo root produces the main binary.
		root := repoRoot()
		cmd := exec.Command("go", "build", "-o", bin, "./")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
			return
		}
		builtBinary = bin
	})
	if buildErr != nil {
		t.Fatalf("build sidekick: %v", buildErr)
	}
	return builtBinary
}

// repoRoot returns the absolute path of the sidekick repo root, walking up
// from this file's location. Works whether tests run from the worktree or a
// clone (Go embeds runtime.Caller paths as compile-time absolute paths).
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	// e2e/harness.go -> e2e -> repo root
	return filepath.Dir(filepath.Dir(file))
}

// ShortSockPath returns a Unix socket path short enough to fit in
// sockaddr_un (104 bytes on darwin). `t.TempDir()` paths often exceed
// this limit when test names are long.
func ShortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "skl-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// ScratchRepo creates a temp dir initialised as a fresh git repo with one
// empty commit and a configured user identity, and returns the canonical
// (symlink-resolved) absolute path.
func ScratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	return canonical(t, dir)
}

// ScratchRepoWithWorktree creates a scratch repo plus one linked git
// worktree, returning canonical (trunk, worktree) paths.
func ScratchRepoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	trunk := ScratchRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, trunk, "worktree", "add", "-q", wt)
	return trunk, canonical(t, wt)
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "e2e@sidekick.local")
	runGit(t, dir, "config", "user.name", "sidekick-e2e")
	runGit(t, dir, "commit", "--allow-empty", "-q", "-m", "init")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func canonical(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

// WriteSidekickYAML writes sidekick.yaml into dir.
func WriteSidekickYAML(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "sidekick.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Daemon wraps a `sidekick start --headless` subprocess and the socket it
// listens on. Spawned daemons must be Stopped (via t.Cleanup or explicitly)
// so the socket file is removed and the process doesn't outlive the test.
type Daemon struct {
	t       *testing.T
	bin     string
	sock    string
	repoDir string
	cmd     *exec.Cmd
	stderr  *syncBuffer
	wait    chan error
	cancel  context.CancelFunc
}

// StartDaemon launches `sidekick start --headless` in repoDir with an
// isolated socket path and HOME, then waits until the daemon responds to a
// ping. Returns a handle the test can use to send hooks/status/etc.
func StartDaemon(t *testing.T, repoDir string) *Daemon {
	t.Helper()
	bin := BuildBinary(t)
	sock := ShortSockPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "start", "--headless")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"SIDEKICK_SOCK="+sock,
		// Pin HOME so the daemon can't read a real ~/.sidekick/.
		"HOME="+t.TempDir(),
		// Block fallback to a user-global sidekick.yaml.
		"SIDEKICK_GLOBAL_CONFIG="+filepath.Join(t.TempDir(), "no-global.yaml"),
	)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	// Run in its own process group so SIGTERM doesn't escape if the test
	// itself is interrupted.
	cmd.SysProcAttr = procAttr()

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start sidekick: %v", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	d := &Daemon{
		t: t, bin: bin, sock: sock, repoDir: repoDir,
		cmd: cmd, stderr: stderr, wait: wait, cancel: cancel,
	}
	t.Cleanup(func() { d.Stop() })

	if err := d.waitReady(5 * time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\n--- stderr ---\n%s", err, stderr.String())
	}
	return d
}

// Sock returns the daemon's socket path.
func (d *Daemon) Sock() string { return d.sock }

// Bin returns the sidekick binary path.
func (d *Daemon) Bin() string { return d.bin }

// RepoDir returns the daemon's working directory.
func (d *Daemon) RepoDir() string { return d.repoDir }

// Stop sends SIGTERM and waits up to 3s for the daemon to exit. Idempotent.
func (d *Daemon) Stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-d.wait:
	case <-time.After(3 * time.Second):
		_ = d.cmd.Process.Kill()
		<-d.wait
	}
	d.cancel()
	d.cmd = nil
}

// Stderr returns everything the daemon has written to stderr so far.
func (d *Daemon) Stderr() string { return d.stderr.String() }

func (d *Daemon) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-d.wait:
			return fmt.Errorf("daemon exited early: %v", err)
		default:
		}
		if _, err := os.Stat(d.sock); err == nil {
			// Socket file exists — try a ping.
			if _, err := d.send(ipc.Request{Type: ipc.TypePing}); err == nil {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("timed out waiting for daemon")
}

// send dials the daemon's socket directly. Used by Status, Hook, and tests
// that need cwd-routed messages.
func (d *Daemon) send(req ipc.Request) (ipc.Response, error) {
	return dial(d.sock, req)
}

func dial(sock string, req ipc.Request) (ipc.Response, error) {
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return ipc.Response{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ipc.Response{}, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return ipc.Response{}, err
	}
	var resp ipc.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return ipc.Response{}, err
	}
	return resp, nil
}

// SendIPC sends a raw request to the daemon's socket. The caller controls
// Cwd / Data fields directly — useful for worktree-targeted requests in T5.
func (d *Daemon) SendIPC(t *testing.T, req ipc.Request) ipc.Response {
	t.Helper()
	resp, err := d.send(req)
	if err != nil {
		t.Fatalf("send %s: %v", req.Type, err)
	}
	if !resp.OK {
		t.Fatalf("%s replied error: %s", req.Type, resp.Error)
	}
	return resp
}

// Status fetches the daemon's StatusReply for the daemon's repoDir.
func (d *Daemon) Status(t *testing.T) ipc.StatusReply {
	t.Helper()
	return d.StatusFrom(t, d.repoDir)
}

// StatusFrom fetches the StatusReply for a specific cwd (used to target a
// worktree session).
func (d *Daemon) StatusFrom(t *testing.T, cwd string) ipc.StatusReply {
	t.Helper()
	resp := d.SendIPC(t, ipc.Request{Type: ipc.TypeStatus, Cwd: cwd})
	var s ipc.StatusReply
	if err := json.Unmarshal(resp.Data, &s); err != nil {
		t.Fatalf("decode status: %v\nraw=%s", err, resp.Data)
	}
	return s
}

// Hook fires `sidekick hook write` with a Claude-style payload pointing at
// the given file path. Runs in d.repoDir; honours SIDEKICK_SOCK so the
// subprocess hits this daemon, not a real user daemon.
func (d *Daemon) Hook(t *testing.T, file string) {
	t.Helper()
	d.HookFrom(t, d.repoDir, file)
}

// HookFrom is like Hook but runs the CLI in a specific cwd. Use when
// firing hooks for a worktree that isn't d.repoDir.
func (d *Daemon) HookFrom(t *testing.T, cwd, file string) {
	t.Helper()
	payload := fmt.Sprintf(`{"tool_input":{"file_path":%q}}`, file)
	cmd := exec.Command(d.bin, "hook", "write")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "SIDEKICK_SOCK="+d.sock, "HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader(payload)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook write: %v\noutput=%s", err, buf.String())
	}
}

// WaitForVerifier polls Status() until pred returns true for the named
// verifier, or fails the test after timeout.
func (d *Daemon) WaitForVerifier(t *testing.T, name string, pred func(ipc.VerifierStatus) bool, timeout time.Duration) ipc.VerifierStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last ipc.VerifierStatus
	for time.Now().Before(deadline) {
		st := d.Status(t)
		for _, v := range st.Verifiers {
			if v.Name == name {
				last = v
				if pred(v) {
					return v
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("verifier %q did not satisfy predicate within %s; last=%+v\nstderr:\n%s",
		name, timeout, last, d.Stderr())
	return last
}

// SidekickCmd builds an *exec.Cmd that invokes the test-built binary in cwd
// with this daemon's socket. Used by tests that drive subcommands beyond
// hook/status (e.g. `verifier add`).
func (d *Daemon) SidekickCmd(cwd string, args ...string) *exec.Cmd {
	cmd := exec.Command(d.bin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"SIDEKICK_SOCK="+d.sock,
		"HOME="+d.t.TempDir(),
		"SIDEKICK_GLOBAL_CONFIG="+filepath.Join(d.t.TempDir(), "no-global.yaml"),
	)
	return cmd
}

// RunSidekick runs a one-shot sidekick subcommand in cwd. Returns combined
// output and any exec error. Used by tests that don't need a daemon
// (e.g. T3 — `sidekick verifier add ...`).
func RunSidekick(t *testing.T, cwd string, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	bin := BuildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"SIDEKICK_GLOBAL_CONFIG="+filepath.Join(t.TempDir(), "no-global.yaml"),
	)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// syncBuffer is a goroutine-safe bytes.Buffer, used to collect daemon
// stderr without racing the test goroutine that reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
