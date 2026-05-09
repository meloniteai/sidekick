package hud

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/ipc"
)

func TestRenderDotStructure(t *testing.T) {
	out := renderDot(0)
	if !strings.HasPrefix(out, "[") || !strings.HasSuffix(out, "]") {
		t.Fatalf("missing brackets: %q", out)
	}
	if got := strings.Count(out, "."); got != 1 {
		t.Errorf("dot count: got %d, want 1", got)
	}
}

func TestRenderDotPingPong(t *testing.T) {
	period := (dotTrack - 1) * 2
	// Frames repeat every period ticks.
	if renderDot(0) != renderDot(period) || renderDot(0) != renderDot(2*period) {
		t.Errorf("expected period %d", period)
	}
	// Adjacent ticks must differ (dot moves each tick).
	if renderDot(0) == renderDot(1) {
		t.Errorf("frames at tick 0 and 1 should differ")
	}
	// The dot should reach both ends: position 0 at tick 0 and position dotTrack-1 at tick dotTrack-1.
	if renderDot(0) == renderDot(dotTrack-1) {
		t.Errorf("opposite ends of track should differ")
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
	lines := strings.Split(strings.TrimRight(m.renderList(80), "\n"), "\n")
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
	if !strings.Contains(running, ".") {
		t.Errorf("running row missing dot: %q", running)
	}
	if strings.Contains(idle, "running") {
		t.Errorf("idle row should not render running indicator: %q", idle)
	}
}

func TestRenderListNoTrailingNewline(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1},
				{Name: "Security", Direction: "S", Distance: 0.2},
			},
		},
	}
	if out := m.renderList(80); strings.HasSuffix(out, "\n") {
		t.Fatalf("renderList should not add a trailing newline: %q", out)
	}
}

func TestRenderListTruncatesReasonToWidth(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{
					Name:      "Architect",
					Direction: "N",
					Distance:  0.1,
					Reason:    strings.Repeat("long visual reason ", 8),
				},
			},
		},
	}
	const width = 54
	out := m.renderList(width)
	if got := lipgloss.Width(out); got > width {
		t.Fatalf("rendered line width = %d, want <= %d: %q", got, width, out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("expected truncated reason marker in %q", out)
	}
}

func TestViewFitsTerminalHeight(t *testing.T) {
	m := Model{
		width:  80,
		height: 24,
		snapshot: ipc.StatusReply{
			Goal: "keep the TUI visually stable",
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1},
				{Name: "Test", Direction: "E", Distance: 0.2},
				{Name: "Security", Direction: "S", Distance: 0.3},
				{Name: "Deployment", Direction: "W", Distance: 0.4},
			},
		},
	}
	out := m.View()
	if lines := len(strings.Split(out, "\n")); lines > m.height {
		t.Fatalf("view rendered %d lines into height %d:\n%s", lines, m.height, out)
	}
}

func TestHeaderAndGridWidthsMatch(t *testing.T) {
	const width = 80
	m := Model{
		snapshot: ipc.StatusReply{
			Goal:    "keep the header aligned with the compass",
			Version: "dev",
		},
	}
	header := m.renderHeader(width)
	grid := styleGrid.Render(m.renderGrid(width-styleGrid.GetHorizontalFrameSize(), 9))
	if got, want := lipgloss.Width(header), lipgloss.Width(grid); got != want {
		t.Fatalf("header width = %d, grid width = %d\nheader:\n%s\ngrid:\n%s", got, want, header, grid)
	}
	if got := lipgloss.Width(header); got != width {
		t.Fatalf("header width = %d, want %d:\n%s", got, width, header)
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
		"keys: ", "q quit", "t trigger",
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

func TestRenderDotNegativeTick(t *testing.T) {
	period := (dotTrack - 1) * 2
	// Must not panic and must be modular: tick=-1 == tick=period-1.
	if renderDot(-1) != renderDot(period-1) {
		t.Errorf("negative tick should be modular")
	}
	if got := strings.Count(renderDot(-1), "."); got != 1 {
		t.Errorf("negative tick dot count: got %d, want 1", got)
	}
}

func TestTruncateUsesVisualWidth(t *testing.T) {
	got := truncate("測試abc", 4)
	if lipgloss.Width(got) > 4 {
		t.Fatalf("truncate width = %d, want <= 4: %q", lipgloss.Width(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix in %q", got)
	}
}
