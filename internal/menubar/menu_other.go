//go:build !darwin

package menubar

import (
	"context"
	"errors"

	"github.com/uriahlevy/hud/internal/daemon"
)

type Actions struct {
	Trigger       func()
	StopRun       func()
	SwitchSession func(worktree string) bool
	Quit          func()
}

func Run(ctx context.Context, registry *daemon.Registry, actions Actions) error {
	return errors.New("hud menubar is only available on macOS")
}
