//go:build darwin

package menubar

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>

void HUDSetActionFD(int fd);
void HUDRun(void);
void HUDUpdateMenu(const char *json);
void HUDStop(void);
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/uriahlevy/hud/internal/daemon"
)

func init() {
	// AppKit requires NSStatusItem/NSWindow creation on the original process
	// main thread. Lock during package initialization, while Go is still on
	// that thread, so Cobra and signal setup cannot migrate the menubar path.
	runtime.LockOSThread()
}

// Actions are invoked from native menu item selections.
type Actions struct {
	Trigger func()
	StopRun func()
	Quit    func()
}

// Run starts a macOS status item and blocks until the AppKit run loop exits.
// It does not create a window; all UI is rendered inside the status-bar menu.
func Run(ctx context.Context, state *daemon.State, actions Actions) error {
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create menu action pipe: %w", err)
	}
	defer readPipe.Close()
	defer writePipe.Close()

	C.HUDSetActionFD(C.int(writePipe.Fd()))

	go pumpActions(ctx, readPipe, actions)
	go pumpMenu(ctx, state)
	go func() {
		<-ctx.Done()
		C.HUDStop()
		_ = readPipe.Close()
	}()

	C.HUDRun()
	return nil
}

func pumpActions(ctx context.Context, r *os.File, actions Actions) {
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
			C.HUDStop()
			return
		}
	}
}

func pumpMenu(ctx context.Context, state *daemon.State) {
	update := func() {
		b, err := RenderJSON(state.Snapshot())
		if err != nil {
			return
		}
		cstr := C.CString(string(b))
		C.HUDUpdateMenu(cstr)
		C.free(unsafe.Pointer(cstr))
	}
	update()
	t := newTicker(ctx)
	defer t.stop()
	for t.next() {
		update()
	}
}
