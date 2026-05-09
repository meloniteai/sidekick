package hud

import (
	"reflect"
	"strings"
	"testing"

	"github.com/uriahlevy/hud/internal/ipc"
)

// litPositions extracts the set of cell indices showing the lit body rune
// from a rendered snake frame, ignoring lipgloss ANSI escape codes.
func litPositions(s string) map[int]bool {
	out := map[int]bool{}
	pos := 0
	for _, r := range s {
		switch r {
		case '█':
			out[pos] = true
			pos++
		case '░':
			pos++
		}
	}
	return out
}

func TestRenderSnakeStructure(t *testing.T) {
	out := renderSnake(0)
	if !strings.HasPrefix(out, "[") || !strings.HasSuffix(out, "]") {
		t.Fatalf("missing brackets: %q", out)
	}
	if got, want := strings.Count(out, "█"), snakeBody; got != want {
		t.Errorf("body cells: got %d, want %d", got, want)
	}
	if got, want := strings.Count(out, "░"), snakeTrack-snakeBody; got != want {
		t.Errorf("empty cells: got %d, want %d", got, want)
	}
}

func TestRenderSnakeWrapAround(t *testing.T) {
	// Frames must repeat every snakeTrack ticks.
	if renderSnake(0) != renderSnake(snakeTrack) || renderSnake(0) != renderSnake(2*snakeTrack) {
		t.Errorf("expected period %d", snakeTrack)
	}
	// Adjacent ticks within a period must differ.
	if renderSnake(0) == renderSnake(1) {
		t.Errorf("frames at tick 0 and 1 should differ")
	}
	// At the wrap point, the body straddles the right edge back to the left.
	lit := litPositions(renderSnake(snakeTrack - 1))
	want := map[int]bool{}
	head := snakeTrack - 1
	for j := 0; j < snakeBody; j++ {
		want[(head-j+snakeTrack)%snakeTrack] = true
	}
	if !reflect.DeepEqual(lit, want) {
		t.Errorf("wrap-around lit positions: got %v, want %v", lit, want)
	}
}

func TestRenderListSnakeGating(t *testing.T) {
	// The snake marquee must appear only on rows whose verifier is Running.
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1, Running: true},
				{Name: "Security", Direction: "S", Distance: 0.0, Running: false},
			},
		},
	}
	lines := strings.Split(strings.TrimRight(m.renderList(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), lines)
	}
	var running, idle string
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "Architect"):
			running = ln
		case strings.Contains(ln, "Security"):
			idle = ln
		}
	}
	if running == "" || idle == "" {
		t.Fatalf("missing verifier lines: running=%q idle=%q", running, idle)
	}
	if !strings.Contains(running, "█") {
		t.Errorf("running row missing snake body: %q", running)
	}
	if strings.Contains(idle, "█") || strings.Contains(idle, "░") {
		t.Errorf("idle row should not render snake: %q", idle)
	}
}

func TestRenderSnakeNegativeTick(t *testing.T) {
	// Must not panic and must remain modular: tick=-1 == tick=snakeTrack-1.
	if renderSnake(-1) != renderSnake(snakeTrack-1) {
		t.Errorf("negative tick should be modular")
	}
	if got := strings.Count(renderSnake(-1), "█"); got != snakeBody {
		t.Errorf("negative tick body cells: got %d, want %d", got, snakeBody)
	}
}
