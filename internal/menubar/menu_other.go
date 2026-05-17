//go:build !darwin || !cgo

package menubar

import (
	"context"
	"errors"

	"github.com/meloniteai/sidekick/internal/daemon"
)

type Actions struct {
	Trigger       func()
	StopRun       func()
	SwitchSession func(worktree string) bool
	Quit          func()
}

func Run(ctx context.Context, registry *daemon.Registry, actions Actions) error {
	return errors.New("sidekick menubar is only available on macOS")
}
