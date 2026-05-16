package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/uriahlevy/hud/internal/daemon"
)

type noopHandler struct{}

func (noopHandler) OnWrite(*daemon.State, string) {}
func (noopHandler) OnGoal(*daemon.State, string)  {}

func main() {
	dir, err := os.MkdirTemp("", "hud-probe-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "hud.sock")

	r1 := daemon.NewRegistry(daemon.NewState(), nil)
	s1, err := daemon.Listen(sock, r1, noopHandler{})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s1.Serve(ctx)
	time.Sleep(50 * time.Millisecond)

	_, err = daemon.Listen(sock, daemon.NewRegistry(daemon.NewState(), nil), noopHandler{})
	fmt.Printf("second listen err: %v\n", err)
	fmt.Printf("errors.Is(err, ErrDaemonRunning) = %v\n", errors.Is(err, daemon.ErrDaemonRunning))

	if err := daemon.RemoveSocket(sock); err != nil {
		panic(err)
	}
	s3, err := daemon.Listen(sock, daemon.NewRegistry(daemon.NewState(), nil), noopHandler{})
	fmt.Printf("retry-after-remove err: %v, srv=%v\n", err, s3 != nil)
	if s3 != nil {
		s3.Close()
	}
	s1.Close()
}
