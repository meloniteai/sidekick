//go:build darwin && cgo

package menubar

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>

void SIDEKICKSetActionFD(int fd);
void SIDEKICKRun(void);
void SIDEKICKUpdateMenu(const char *json);
void SIDEKICKStop(void);
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/meloniteai/sidekick/internal/daemon"
)

func init() {
	// AppKit requires NSStatusItem/NSWindow creation on the original process
	// main thread. Lock during package initialization, while Go is still on
	// that thread, so Cobra and signal setup cannot migrate the menubar path.
	runtime.LockOSThread()
}

// Actions are invoked from native menu item selections.
type Actions struct {
	Trigger       func()
	StopRun       func()
	SwitchSession func(worktree string) bool
	Quit          func()
}

// Run starts a macOS status item and blocks until the AppKit run loop exits.
// It does not create a window; all UI is rendered inside the status-bar menu.
func Run(ctx context.Context, registry *daemon.Registry, actions Actions) error {
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create menu action pipe: %w", err)
	}
	defer readPipe.Close()
	defer writePipe.Close()

	C.SIDEKICKSetActionFD(C.int(writePipe.Fd()))

	go pumpActions(ctx, readPipe, registry, actions)
	go pumpMenu(ctx, registry)
	go func() {
		<-ctx.Done()
		C.SIDEKICKStop()
		_ = readPipe.Close()
	}()

	C.SIDEKICKRun()
	return nil
}

func pumpActions(ctx context.Context, r *os.File, registry *daemon.Registry, actions Actions) {
	buf := []byte{0}
	for {
		if _, err := r.Read(buf); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		switch int(buf[0]) {
		case actionTrigger:
			if actions.Trigger != nil {
				actions.Trigger()
			}
		case actionStop:
			if actions.StopRun != nil {
				actions.StopRun()
			}
		case actionQuit:
			if actions.Quit != nil {
				actions.Quit()
			}
			C.SIDEKICKStop()
			return
		default:
			action := int(buf[0])
			if action >= actionSessionBase && actions.SwitchSession != nil {
				idx := action - actionSessionBase
				sessions := registry.Sessions()
				if idx >= 0 && idx < len(sessions) {
					actions.SwitchSession(sessions[idx].Worktree)
				}
			}
		}
	}
}

func pumpMenu(ctx context.Context, registry *daemon.Registry) {
	update := func() {
		b, err := RenderJSON(registry.DisplayedSnapshot())
		if err != nil {
			return
		}
		cstr := C.CString(string(b))
		C.SIDEKICKUpdateMenu(cstr)
		C.free(unsafe.Pointer(cstr))
	}
	update()
	t := newTicker(ctx)
	defer t.stop()
	for t.next() {
		update()
	}
}
