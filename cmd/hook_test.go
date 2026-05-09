package cmd

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

func TestHookFilesClaudePayload(t *testing.T) {
	files, err := hookFiles([]byte(`{"tool_input":{"file_path":"src/auth.go"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/auth.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesCodexCamelCasePayload(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"hook_event_name": "PostToolUse",
		"toolName": "apply_patch",
		"toolInput": {
			"absolute_file_path": "/repo/internal/hook.go"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/repo/internal/hook.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesApplyPatchPayload(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"toolName": "apply_patch",
		"arguments": {
			"patch": "*** Begin Patch\n*** Update File: cmd/hook.go\n@@\n*** Add File: examples/codex-hooks.json\n+{}\n*** End Patch\n"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/hook.go", "examples/codex-hooks.json"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestHookFilesStringifiedArguments(t *testing.T) {
	files, err := hookFiles([]byte(`{
		"toolName": "apply_patch",
		"arguments": "{\"patch\":\"*** Begin Patch\\n*** Delete File: old.go\\n*** End Patch\\n\"}"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"old.go"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %#v, want %#v", files, want)
	}
}

func TestForwardWriteReachesDaemon(t *testing.T) {
	h := &captureHandler{writes: make(chan string, 1)}
	sock := t.TempDir() + "/hud.sock"
	state := daemon.NewState()
	srv, err := daemon.Listen(sock, state, h)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer srv.Close()
	go func() {
		_ = srv.Serve(ctx)
	}()
	t.Setenv("HUD_SOCK", sock)

	if err := forward(ipc.TypeWrite, ipc.WriteData{File: "cmd/hook.go"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-h.writes:
		if got != "cmd/hook.go" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not receive write")
	}
}

type captureHandler struct {
	writes chan string
}

func (h *captureHandler) OnWrite(file string) { h.writes <- file }
func (h *captureHandler) OnGoal(string)       {}
