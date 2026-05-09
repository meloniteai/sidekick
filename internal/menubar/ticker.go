package menubar

import (
	"context"
	"time"
)

type menuTicker struct {
	t *time.Ticker
	c <-chan time.Time
}

func newTicker(ctx context.Context) menuTicker {
	t := time.NewTicker(time.Second)
	c := make(chan time.Time)
	go func() {
		defer close(c)
		for {
			select {
			case tick := <-t.C:
				select {
				case c <- tick:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return menuTicker{t: t, c: c}
}

func (t menuTicker) next() bool {
	_, ok := <-t.c
	return ok
}

func (t menuTicker) stop() {
	t.t.Stop()
}
