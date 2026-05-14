package hud

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/uriahlevy/hud/internal/ipc"
	"github.com/uriahlevy/hud/internal/verifier"
)

// TestVisualPreview renders a synthetic snapshot to stdout when
// HUD_VISUAL=1 is set. Used for hand-eyeballing the flamboyance pass.
//
//	HUD_VISUAL=1 TICK=7 go test -run TestVisualPreview ./internal/hud/ -v
func TestVisualPreview(t *testing.T) {
	if os.Getenv("HUD_VISUAL") == "" {
		t.Skip("set HUD_VISUAL=1 to print a rendered frame")
	}
	tick, _ := strconv.Atoi(os.Getenv("TICK"))
	w, _ := strconv.Atoi(os.Getenv("COLS"))
	h, _ := strconv.Atoi(os.Getenv("ROWS"))
	if w == 0 {
		w = 100
	}
	if h == 0 {
		h = 30
	}

	now := time.Date(2026, 5, 12, 20, 30, 0, 0, time.UTC)
	m := Model{
		width:  w,
		height: h,
		tick:   tick,
		snapshot: ipc.StatusReply{
			Goal:         "ship the flamboyant TUI and survive the demo",
			Version:      "flare-preview",
			LastSocketAt: now.Add(-3 * time.Second),
			LastMCPAt:    now.Add(-12 * time.Second),
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.05, Status: ipc.StatusOK, Reason: "converged"},
				{Name: "Tests", Direction: "E", Distance: 0.45, Status: ipc.StatusOK, Reason: "passing"},
				{Name: "Security", Direction: "S", Distance: 0.75, Status: ipc.StatusError, Reason: "stale finding"},
				{Name: "Deploy", Direction: "W", Distance: 0.90, Running: true, Reason: "running"},
				{Name: "Bench", Direction: "NE", Distance: 0.30, Status: ipc.StatusOK},
				{Name: "Lint", Direction: "NW", Distance: 0.55, Status: ipc.StatusOK},
			},
		},
		anims: map[string]arrowAnim{},
	}
	fmt.Println(m.View())
}

// TestVisualPreviewLanding renders the landing/splash to stdout when
// HUD_VISUAL=1 is set so we can eyeball the KIKAITE HUD banner and the
// coral chrome. Sized for a typical 120-wide terminal.
//
//	HUD_VISUAL=1 go test -run TestVisualPreviewLanding ./internal/hud/ -v
func TestVisualPreviewLanding(t *testing.T) {
	if os.Getenv("HUD_VISUAL") == "" {
		t.Skip("set HUD_VISUAL=1 to print a rendered frame")
	}
	vs := []verifier.Verifier{
		{Name: "Architect", Direction: "N"},
		{Name: "Test Engineer", Direction: "E"},
		{Name: "Security", Direction: "S"},
	}
	l := NewLanding(vs, "0.1", "/Users/u/.hud/sockets/abc.sock", "/Users/u/repos/hud")
	next, _ := l.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	fmt.Println(next.(Landing).View())
}
