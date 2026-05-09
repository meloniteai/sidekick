package hud

import (
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestRenderGridArrowAnimation pins down the post-computation arrow on a
// verifier's compass plane: when the snapshot reports a fresh ComputedAt the
// model arms an animation, and the next render places the direction's arrow
// glyph somewhere along the path between the goal and that verifier's orb.
func TestRenderGridArrowAnimation(t *testing.T) {
	earlier := time.Unix(1_700_000_000, 0)
	later := earlier.Add(time.Second)

	m := Model{
		width:  41,
		height: 21,
		anims:  map[string]arrowAnim{},
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.8, ComputedAt: earlier},
			},
		},
	}
	// First refresh seeds the anim entry without scheduling an animation.
	m.refreshAnims()
	first := m.renderGrid(41, 21)
	if strings.Contains(first, "↑") {
		t.Fatalf("first render should not paint the arrow; got:\n%s", first)
	}

	// New ComputedAt arrives → next refresh starts the animation; the next
	// render must paint the arrow somewhere in the grid.
	m.tick++
	m.snapshot.Verifiers[0].ComputedAt = later
	m.refreshAnims()
	if frame, active := m.animFrame("Architect", false); !active || frame != 0 {
		t.Fatalf("expected frame 0 active after fresh ComputedAt, got frame=%d active=%v", frame, active)
	}
	mid := m.renderGrid(41, 21)
	if !strings.Contains(mid, "↑") {
		t.Fatalf("animating render should paint ↑; got:\n%s", mid)
	}

	// After arrowAnimFrames ticks, the arrow stops painting.
	m.tick += arrowAnimFrames
	if _, active := m.animFrame("Architect", false); active {
		t.Fatalf("animation should be over after %d ticks", arrowAnimFrames)
	}
	done := m.renderGrid(41, 21)
	if strings.Contains(done, "↑") {
		t.Fatalf("post-animation render should not paint ↑; got:\n%s", done)
	}
}

// TestRenderGridArrowDistanceZero guards the visually-meaningless case where
// a verifier sits exactly on the goal — no path to animate, so we skip.
func TestRenderGridArrowDistanceZero(t *testing.T) {
	earlier := time.Unix(1_700_000_000, 0)
	later := earlier.Add(time.Second)
	m := Model{
		width:  41,
		height: 21,
		anims:  map[string]arrowAnim{},
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Test", Direction: "E", Distance: 0.0, ComputedAt: earlier},
			},
		},
	}
	m.refreshAnims()
	m.tick++
	m.snapshot.Verifiers[0].ComputedAt = later
	m.refreshAnims()
	if out := m.renderGrid(41, 21); strings.Contains(out, "→") {
		t.Errorf("zero-distance verifier should not paint an arrow; got:\n%s", out)
	}
}

// TestRenderHeaderFields confirms the framed header surfaces every metadata
// field the TUI promises (version, session indicator, both timestamps,
// verifier count, goal). It also pins the empty-state behaviour so the box
// doesn't collapse before the daemon has seen any traffic.
func TestRenderHeaderFields(t *testing.T) {
	at := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	m := Model{
		width:  120,
		height: 40,
		snapshot: ipc.StatusReply{
			Goal:         "ship the header",
			Version:      "dev",
			LastSocketAt: at,
			LastMCPAt:    at.Add(-time.Second),
			Verifiers: []ipc.VerifierStatus{
				{Name: "A", Direction: "N"},
				{Name: "B", Direction: "E"},
				{Name: "C", Direction: "S"},
			},
		},
	}
	out := m.renderHeader(80)
	for _, want := range []string{
		"version: ", "dev",
		"session: ", "active",
		"last socket: ", "12:34:56",
		"last mcp: ", "12:34:55",
		"verifiers: ", "3",
		"goal: ", "ship the header",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q in:\n%s", want, out)
		}
	}

	// Zero-value timestamps must still render (em-dash placeholder), not
	// blow up the layout.
	empty := Model{width: 120, height: 40, snapshot: ipc.StatusReply{}}.renderHeader(80)
	if !strings.Contains(empty, "—") {
		t.Errorf("zero-time header should show em-dash placeholder; got:\n%s", empty)
	}
	if !strings.Contains(empty, "verifiers: 0") {
		t.Errorf("empty header should report 0 verifiers; got:\n%s", empty)
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
