package hud

import (
	"regexp"
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
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %q", len(lines), lines)
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
					Config: ipc.VerifierConfig{
						Type:  "agent",
						Agent: "claude",
						Model: "haiku",
					},
				},
			},
		},
	}
	const width = 54
	out := m.renderList(width)
	if got := lipgloss.Width(out); got > width {
		t.Fatalf("rendered line width = %d, want <= %d: %q", got, width, out)
	}
	if !strings.Contains(out, "...") {
		t.Fatalf("expected truncated reason marker in %q", out)
	}
}

func TestRenderListOmitsVerifierConfigMetadata(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{
					Name:      "Architect",
					Direction: "N",
					Distance:  0.1,
					Config: ipc.VerifierConfig{
						Type:     "agent",
						Agent:    "claude",
						Model:    "haiku",
						Thinking: "low",
						Timeout:  "90s",
					},
				},
				{
					Name:      "Unit Tests",
					Direction: "S",
					Distance:  0,
					Status:    ipc.StatusOK,
					Config: ipc.VerifierConfig{
						Type:       "binary",
						Command:    []string{"./scripts/test.sh"},
						PassReason: "tests pass",
						FailReason: "tests failed",
					},
				},
				{
					Name:      "Lint",
					Direction: "SW",
					Distance:  1,
					Status:    ipc.StatusOK,
					Config: ipc.VerifierConfig{
						Type:       "binary",
						Command:    []string{"./scripts/lint.sh"},
						PassReason: "lint passed",
						FailReason: "lint failed",
					},
				},
			},
		},
	}
	out := m.renderList(180)
	for _, want := range []string{
		"key", "verifier", "type", "status", "reason",
		"agent", "binary", "d=0.10", "d=0.00", "d=1.00",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("browser row missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"config",
		"agent=claude", "model=haiku", "thinking=low", "timeout=90s",
		"cmd=./scripts/test.sh", "pass=tests pass", "fail=tests failed",
		"cmd=./scripts/lint.sh", "pass=lint passed", "fail=lint failed",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("browser row unexpectedly contained config metadata %q in:\n%s", unwanted, out)
		}
	}
}

func TestRenderListShowsDisabledVerifierToggle(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1},
				{Name: "Test", Direction: "E", Distance: 0.2, Disabled: true, Reason: "disabled"},
			},
		},
	}
	out := m.renderList(80)
	for _, want := range []string{"[1]", "Architect", "[2]", "Test", "off", "disabled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("disabled footer row missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderListShowsBrowserActionsAndSelection(t *testing.T) {
	m := Model{
		selectedVerifier: 1,
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1},
				{Name: "Test", Direction: "E", Distance: 0.2},
			},
		},
	}
	out := m.renderList(180)
	for _, want := range []string{
		"key", "verifier", "dir", "type", "status", "reason",
		"keys:", "enter status", "space toggle", "r run one", "t all", "e edit", "1-9/0 toggle",
		">", "[2]", "Test",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("browser footer missing %q in:\n%s", want, out)
		}
	}
	firstLine := strings.SplitN(out, "\n", 2)[0]
	if strings.Contains(firstLine, "sel") {
		t.Fatalf("table header should not include selection column:\n%s", out)
	}
	if strings.Contains(firstLine, "config") {
		t.Fatalf("table header should not include config column:\n%s", out)
	}
}

func TestRenderListColumnsStayAlignedAndTruncated(t *testing.T) {
	m := Model{
		selectedVerifier: 1,
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{
					Name:      "Ridiculously Long Verifier Name",
					Direction: "NE",
					Distance:  0.12,
					Reason:    "first very long reason that should remain in the reason column",
				},
				{
					Name:      "Tiny",
					Direction: "S",
					Distance:  1,
					Status:    ipc.StatusError,
					Reason:    "second very long reason that should also truncate cleanly",
				},
			},
		},
	}

	const width = 76
	lines := strings.Split(strings.TrimRight(m.renderList(width), "\n"), "\n")
	if got := len(lines); got != 4 {
		t.Fatalf("got %d lines, want 4:\n%s", got, strings.Join(lines, "\n"))
	}
	layout := listLayoutFor(width)
	statusStart := lipgloss.Width(listCursorPad) + layout.keyW + layout.nameW + layout.dirW + layout.typeW + 4*lipgloss.Width(listColumnGap)
	reasonStart := statusStart + layout.statusW + lipgloss.Width(listColumnGap)
	for row, line := range lines[:3] {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", row, got, width, line)
		}
		if lipgloss.Width(line) < reasonStart {
			t.Fatalf("line %d too short to contain reason column at %d: %q", row, reasonStart, line)
		}
	}
	for _, line := range lines[1:3] {
		statusCol := visualSlice(line, statusStart, statusStart+layout.statusW)
		reasonCol := visualSlice(line, reasonStart, width)
		if strings.TrimSpace(statusCol) == "" {
			t.Fatalf("status column empty in row %q", line)
		}
		if !strings.Contains(reasonCol, "...") {
			t.Fatalf("reason column should be truncated with three dots; got %q in row %q", reasonCol, line)
		}
	}
}

func TestRenderGridSkipsDisabledVerifier(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.8, Disabled: true},
			},
		},
	}
	out := m.renderGrid(41, 21)
	if strings.Contains(out, "architect") {
		t.Fatalf("disabled verifier should not render on grid:\n%s", out)
	}
	if strings.ContainsRune(out, verifierMarkerGlyph(0)) {
		t.Fatalf("disabled verifier marker should not render on grid:\n%s", out)
	}
}

func TestVerifierMarkerGlyphsAreDistinctSingleCellShapes(t *testing.T) {
	seen := map[rune]bool{}
	for i := 0; i < 8; i++ {
		glyph := verifierMarkerGlyph(i)
		if seen[glyph] {
			t.Fatalf("marker glyph %d repeats %q", i, glyph)
		}
		seen[glyph] = true
		if got := lipgloss.Width(string(glyph)); got != 1 {
			t.Fatalf("marker glyph %q has width %d, want 1", glyph, got)
		}
	}
}

func TestRenderGridSeparatesVerifierMarkerAndLabel(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.8},
			},
		},
	}
	const width, height = 41, 21
	col, row, ok := project("N", 0.8, width, height)
	if !ok {
		t.Fatal("project N failed")
	}
	out := m.renderGrid(width, height)
	lines := strings.Split(out, "\n")
	if got := len(lines); got != height {
		t.Fatalf("line count = %d, want %d:\n%s", got, height, out)
	}
	markerLine := []rune(lines[row])
	if markerLine[col] != verifierMarkerGlyph(0) {
		t.Fatalf("marker at projected cell = %q, want %q\n%s", markerLine[col], verifierMarkerGlyph(0), out)
	}
	if strings.Contains(lines[row], "architect") {
		t.Fatalf("label should be offset from marker row:\n%s", out)
	}
	if !strings.Contains(out, "architect") {
		t.Fatalf("lowercase verifier label missing:\n%s", out)
	}
}

func TestRenderGridUsesDistinctVerifierMarkers(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.9},
				{Name: "Test", Direction: "E", Distance: 0.9},
				{Name: "Security", Direction: "S", Distance: 0.9},
				{Name: "Deploy", Direction: "W", Distance: 0.9},
				{Name: "Bench", Direction: "NE", Distance: 0.9},
				{Name: "Lint", Direction: "NW", Distance: 0.9},
				{Name: "Docs", Direction: "SE", Distance: 0.9},
				{Name: "UX", Direction: "SW", Distance: 0.9},
			},
		},
	}
	out := m.renderGrid(61, 25)
	for i := 0; i < 8; i++ {
		glyph := verifierMarkerGlyph(i)
		if !strings.ContainsRune(out, glyph) {
			t.Fatalf("grid missing marker glyph %q for verifier %d:\n%s", glyph, i, out)
		}
	}
}

func TestRenderGridSmallLabelsStayInsideEdges(t *testing.T) {
	m := Model{
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "zzzzzzzzzz", Direction: "E", Distance: 1},
				{Name: "yyyyyyyyyy", Direction: "W", Distance: 1},
			},
		},
	}
	const width, height = 20, 9
	out := m.renderGrid(width, height)
	lines := strings.Split(out, "\n")
	if got := len(lines); got != height {
		t.Fatalf("line count = %d, want %d:\n%s", got, height, out)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("line %d width = %d, want %d: %q\n%s", row, got, width, line, out)
		}
		runes := []rune(line)
		for _, edge := range []int{0, len(runes) - 1} {
			switch runes[edge] {
			case 'z', 'y', '…':
				t.Fatalf("label clipped into edge at row %d col %d:\n%s", row, edge, out)
			}
		}
	}
	if strings.Contains(lines[0], "z") || strings.Contains(lines[height-1], "z") ||
		strings.Contains(lines[0], "y") || strings.Contains(lines[height-1], "y") {
		t.Fatalf("label clipped into wind-marker rows:\n%s", out)
	}
}

func TestRenderGridDistanceRingsPreserveDimensions(t *testing.T) {
	m := Model{}
	const width, height = 31, 15
	out := m.renderGrid(width, height)
	lines := strings.Split(out, "\n")
	if got := len(lines); got != height {
		t.Fatalf("line count = %d, want %d:\n%s", got, height, out)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("line %d width = %d, want %d: %q\n%s", row, got, width, line, out)
		}
	}
	axisDots := width + height - 1
	if got := strings.Count(out, "·"); got <= axisDots {
		t.Fatalf("expected ring fragments beyond axis dots, got %d axis baseline %d:\n%s", got, axisDots, out)
	}
}

func TestDrawDistanceRingsUsesThinOutlines(t *testing.T) {
	const width, height = 41, 21
	cells := make([][]rune, height)
	kinds := make([][]int, height)
	for r := range cells {
		cells[r] = make([]rune, width)
		kinds[r] = make([]int, width)
	}
	drawDistanceRings(cells, kinds, width, height, width/2, height/2)
	for r := 0; r < height-1; r++ {
		for c := 0; c < width-1; c++ {
			if kinds[r][c] == ringCell &&
				kinds[r+1][c] == ringCell &&
				kinds[r][c+1] == ringCell &&
				kinds[r+1][c+1] == ringCell {
				t.Fatalf("ring outline became a thick 2x2 band near row %d col %d", r, c)
			}
		}
	}
}

func TestStatusWizardShowsFullVerifierStatus(t *testing.T) {
	w := NewStatusWizard(ipc.VerifierStatus{
		Name:       "Architect",
		Direction:  "N",
		Distance:   0.42,
		Reason:     "full reason text",
		ComputedAt: time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC),
		Config: ipc.VerifierConfig{
			Type:     "agent",
			Agent:    "codex",
			Model:    "gpt-5.5",
			Thinking: "medium",
			Skill:    "./skills/architect/SKILL.md",
			Timeout:  "90s",
		},
	})
	w.width = 100
	w.height = 30
	out := w.View()
	for _, want := range []string{
		"HUD verifier status", "Architect", "direction:", "N", "distance:", "0.42", "computed:", "2026-05-09T12:34:56Z",
		"agent:", "codex", "model:", "gpt-5.5", "skill:", "./skills/architect/SKILL.md", "reason:", "full reason text",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status wizard missing %q in:\n%s", want, out)
		}
	}
}

func TestViewWrapsVerifierBrowserInWhiteBorder(t *testing.T) {
	m := Model{
		width:  100,
		height: 30,
		snapshot: ipc.StatusReply{
			Verifiers: []ipc.VerifierStatus{
				{Name: "Architect", Direction: "N", Distance: 0.1},
				{Name: "Test", Direction: "E", Distance: 0.2},
			},
		},
	}
	out := m.View()

	lines := strings.Split(out, "\n")
	headerIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "key") && strings.Contains(ln, "verifier") {
			headerIdx = i
			break
		}
	}
	if headerIdx < 1 {
		t.Fatalf("verifier browser header not found in:\n%s", out)
	}
	if !strings.Contains(lines[headerIdx-1], "╭") {
		t.Fatalf("expected top border immediately above verifier browser header, got %q", lines[headerIdx-1])
	}
	if !strings.Contains(lines[len(lines)-1], "╰") {
		t.Fatalf("expected bottom border on final line, got %q", lines[len(lines)-1])
	}

	want := lipgloss.NewStyle().BorderForeground(lipgloss.Color("15")).Border(lipgloss.RoundedBorder()).Render("")
	if got := styleListBorder.Render(""); got != want {
		t.Fatalf("styleListBorder must use a white rounded border: got %q want %q", got, want)
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
				{Name: "C", Direction: "S", Disabled: true},
			},
		},
	}
	out := m.renderHeader(80)
	for _, want := range []string{
		"version: ", "dev",
		"session: ", "active",
		"last socket: ", "12:34:56",
		"last mcp: ", "12:34:55",
		"verifiers: ", "2/3",
		"goal: ", "ship the header",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "keys: ") {
		t.Errorf("header should not render shortcut labels after moving them to the footer:\n%s", out)
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

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func visualSlice(s string, start, end int) string {
	s = ansiRE.ReplaceAllString(s, "")
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	var b strings.Builder
	pos := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		next := pos + w
		if next > start && pos < end {
			b.WriteRune(r)
		}
		pos = next
		if pos >= end {
			break
		}
	}
	return b.String()
}
