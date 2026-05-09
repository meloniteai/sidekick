//go:build !darwin

package menubar

import (
	"context"
	"errors"

	"github.com/uriahlevy/hud/internal/daemon"
)

type Actions struct {
	Trigger func()
	StopRun func()
	Quit    func()
}

func Run(ctx context.Context, state *daemon.State, actions Actions) error {
	return errors.New("hud menubar is only available on macOS")
}
