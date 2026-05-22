package sidekick

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/meloniteai/sidekick/internal/telemetry"
)

func TestDistanceSparklineLevelsAndWidth(t *testing.T) {
	pts := []telemetry.DistancePoint{{Distance: 0}, {Distance: 0.5}, {Distance: 1}}
	s := distanceSparkline(pts, 10)
	if w := lipgloss.Width(s); w != 3 {
		t.Fatalf("sparkline width = %d, want 3 (one cell per point)", w)
	}
	runes := []rune(ansi.Strip(s))
	if len(runes) != 3 {
		t.Fatalf("stripped sparkline = %q, want 3 glyphs", string(runes))
	}
	if runes[0] != sparkLevels[0] {
		t.Errorf("distance 0 → %q, want floor glyph %q", runes[0], sparkLevels[0])
	}
	if runes[2] != sparkLevels[len(sparkLevels)-1] {
		t.Errorf("distance 1 → %q, want top glyph %q", runes[2], sparkLevels[len(sparkLevels)-1])
	}
}

func TestDistanceSparklineKeepsMostRecent(t *testing.T) {
	pts := []telemetry.DistancePoint{{Distance: 1}, {Distance: 1}, {Distance: 0}}
	s := distanceSparkline(pts, 2)
	if w := lipgloss.Width(s); w != 2 {
		t.Fatalf("sparkline width = %d, want 2 (clamped to cell budget)", w)
	}
	runes := []rune(ansi.Strip(s))
	// The last two points (1, 0) survive — the oldest is dropped.
	if runes[len(runes)-1] != sparkLevels[0] {
		t.Errorf("most recent point (0) not rendered last: %q", string(runes))
	}
}

func TestTelemetryDistanceStyleZeroWhiteOtherwiseRed(t *testing.T) {
	cases := []struct {
		name string
		d    float64
		want string
	}{
		{name: "zero", d: 0, want: "231"},
		{name: "small", d: 0.01, want: "9"},
		{name: "one", d: 1, want: "9"},
	}
	for _, tc := range cases {
		got := string(telemetryDistanceStyle(tc.d).GetForeground().(lipgloss.Color))
		if got != tc.want {
			t.Errorf("%s: foreground = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderTelemetryPanelFlagsUnavailable(t *testing.T) {
	m := Model{telemetryView: &telemetryPanel{openErr: errors.New("no telemetry database yet")}}
	plain := ansi.Strip(m.renderTelemetryPanel(44, 12))
	if !strings.Contains(plain, "telemetry unavailable") {
		t.Errorf("missing-db panel should flag 'telemetry unavailable':\n%s", plain)
	}
	if !strings.Contains(plain, "no telemetry database yet") {
		t.Errorf("panel should surface the open error reason:\n%s", plain)
	}
}

func TestRenderTelemetryPanelFlagsNoSession(t *testing.T) {
	m := Model{telemetryView: &telemetryPanel{loaded: true}} // sessionID == ""
	plain := ansi.Strip(m.renderTelemetryPanel(44, 12))
	if !strings.Contains(plain, "no telemetry session") {
		t.Errorf("panel should flag the absence of a goal episode:\n%s", plain)
	}
	rows := m.telemetryBodyRows(44, 12)
	if len(rows) == 0 || !strings.Contains(rows[0], styleNoSession.Render("no telemetry session")) {
		t.Errorf("no-session heading should render in the no-session style:\n%q", rows)
	}
	if got := string(styleNoSession.GetForeground().(lipgloss.Color)); got != "231" {
		t.Errorf("no-session foreground = %q, want white (231)", got)
	}
}

func TestRenderTelemetryPanelSummary(t *testing.T) {
	sum := telemetry.Summary{
		SessionFound: true,
		GoalText:     "ship the panel",
		EditCount:    3, BatchCount: 1, RunCount: 5, HeartbeatCount: 2,
		Verifiers: []telemetry.VerifierSeries{
			{Name: "clarity", Points: []telemetry.DistancePoint{{Distance: 0.8}, {Distance: 0.2}}, Last: 0.2},
		},
	}
	m := Model{telemetryView: &telemetryPanel{loaded: true, sessionID: "abc", summary: sum}}

	const contentH = 16
	out := m.renderTelemetryPanel(48, contentH)
	plain := ansi.Strip(out)
	for _, want := range []string{"session telemetry", "ship the panel", "clarity", "edits", "beats", "distance × runs"} {
		if !strings.Contains(plain, want) {
			t.Errorf("summary panel missing %q:\n%s", want, plain)
		}
	}
	// Height must match the compass contract (contentH inner rows + 2 border)
	// so the side-by-side JoinHorizontal aligns.
	if h := lipgloss.Height(out); h != contentH+2 {
		t.Errorf("panel height = %d, want %d (contentH + border)", h, contentH+2)
	}
	if w := lipgloss.Width(out); w != 48 {
		t.Errorf("panel width = %d, want 48", w)
	}
}

func TestRenderSidePanelsStackPreservesCompassHeight(t *testing.T) {
	m := Model{
		showEventLog:  true,
		telemetryView: &telemetryPanel{loaded: true, sessionID: "abc", summary: telemetry.Summary{SessionFound: true}},
	}
	const gridH = 15
	out := m.renderSidePanels(40, gridH)
	// Two stacked boxes must total the same height as the compass (gridH + 2)
	// so the column lines up beside it.
	if h := lipgloss.Height(out); h != gridH+2 {
		t.Errorf("stacked side column height = %d, want %d", h, gridH+2)
	}
}
