package hud

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleHeader      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleGrid        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	styleGoal        = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleAxis        = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleRunning     = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleSnakeOn     = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleSnakeOff    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleReason      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleGoalLbl     = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleArrowOutHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleArrowOutTrail = lipgloss.NewStyle().Foreground(lipgloss.Color("88"))
	styleArrowInHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleArrowInTrail  = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
	styleArrowCalHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	styleArrowCalTrail = lipgloss.NewStyle().Foreground(lipgloss.Color("54"))
	styleHeaderBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Foreground(lipgloss.Color("252"))
	styleHeaderLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleSessionOn   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
)

// directionArrow points outward along each compass axis (away from goal toward
// the orb) and is used when distance grew. directionArrowInward points the
// opposite way for when distance shrank.
var directionArrow = map[string]rune{
	"E":  '→',
	"W":  '←',
	"N":  '↑',
	"S":  '↓',
	"NE": '↗',
	"NW": '↖',
	"SE": '↘',
	"SW": '↙',
}

var directionArrowInward = map[string]rune{
	"E":  '←',
	"W":  '→',
	"N":  '↓',
	"S":  '↑',
	"NE": '↙',
	"NW": '↘',
	"SE": '↖',
	"SW": '↗',
}

// arrowTrailLen is the number of trailing cells drawn behind the head as the
// arrow climbs the axis. Two cells is enough to read motion at 5fps without
// crowding short axes.
const arrowTrailLen = 2

// goalGlyph is the target the orbs converge on at the grid center.
const goalGlyph = "◎"

// orbStyle returns the foreground style for a verifier orb at distance d
// (0 = on the goal circle, 1 = maximally far). The bucketed gradient lets
// the user perceive closeness at a glance even without reading the list.
func orbStyle(d float64) lipgloss.Style {
	var color lipgloss.Color
	switch {
	case d <= 0.25:
		color = lipgloss.Color("10") // bright green — on/near the goal
	case d <= 0.50:
		color = lipgloss.Color("11") // yellow
	case d <= 0.75:
		color = lipgloss.Color("208") // orange
	default:
		color = lipgloss.Color("9") // bright red — far
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

// View satisfies tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "initializing..."
	}
	header := m.renderHeader(m.width - 4)
	headerLines := strings.Count(header, "\n") + 1

	gridW := m.width - 4
	gridH := m.height - headerLines - 4 - len(m.snapshot.Verifiers)
	if gridH < 9 {
		gridH = 9
	}
	if gridW < 20 {
		gridW = 20
	}
	if gridH%2 == 0 {
		gridH-- // odd height keeps origin centered
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(styleGrid.Render(m.renderGrid(gridW, gridH)))
	b.WriteString("\n\n")
	b.WriteString(m.renderList())
	b.WriteString("\n")
	b.WriteString(styleReason.Render("press q to quit  ·  press t to trigger"))
	return b.String()
}

// renderHeader builds the framed metadata box at the top of the screen. The
// box stretches to the same width as the grid (innerW + 2 for the border) so
// it visually anchors over the compass below it.
func (m Model) renderHeader(innerW int) string {
	if innerW < 24 {
		innerW = 24
	}
	ver := m.snapshot.Version
	if ver == "" {
		ver = "dev"
	}

	var rows []string

	// Row 1 — identity + session indicator.
	rows = append(rows,
		styleHeader.Render("HUD")+
			"  "+styleHeaderLabel.Render("version: ")+ver+
			"  "+styleHeaderLabel.Render("session: ")+styleSessionOn.Render("active"),
	)

	// Row 2 — telemetry: socket / mcp last-seen + verifier count.
	rows = append(rows,
		styleHeaderLabel.Render("last socket: ")+formatTimestamp(m.snapshot.LastSocketAt)+
			"  "+styleHeaderLabel.Render("last mcp: ")+formatTimestamp(m.snapshot.LastMCPAt)+
			"  "+styleHeaderLabel.Render("verifiers: ")+fmt.Sprintf("%d", len(m.snapshot.Verifiers)),
	)

	// Row 3 — goal summary, single line, truncated to fit.
	goal := m.snapshot.Goal
	goalRow := styleGoalLbl.Render("goal: ")
	if goal == "" {
		goalRow += styleReason.Render("(none — submit a prompt or run `hud goal ...`)")
	} else {
		goalRow += truncate(goal, innerW-len("goal: ")-2)
	}
	rows = append(rows, goalRow)

	return styleHeaderBox.Width(innerW).Render(strings.Join(rows, "\n"))
}

// formatTimestamp renders a wall-clock HH:MM:SS for the header. Zero values
// (no traffic seen yet) are shown as a dim em-dash so the layout stays
// stable from the first frame.
func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return styleReason.Render("—")
	}
	return t.Format("15:04:05")
}

func (m Model) renderGrid(w, h int) string {
	cells := make([][]rune, h)
	for r := range cells {
		cells[r] = make([]rune, w)
		for c := range cells[r] {
			cells[r][c] = ' '
		}
	}
	cx, cy := w/2, h/2
	// axis lines (subtle bearing reference)
	for c := 0; c < w; c++ {
		cells[cy][c] = '·'
	}
	for r := 0; r < h; r++ {
		cells[r][cx] = '·'
	}

	// One orb per verifier, projected to (direction, distance). project() reads
	// v.Distance from the live snapshot, so the orb position reflects the most
	// recent verifier run (which the file-write hook triggers).
	type placed struct {
		col, row int
		label    rune
		distance float64
	}
	var placements []placed
	for i, v := range m.snapshot.Verifiers {
		col, row, ok := project(v.Direction, v.Distance, w, h)
		if !ok {
			continue
		}
		label := rune('A' + i%26)
		if len(v.Name) > 0 {
			label = rune(v.Name[0])
		}
		placements = append(placements, placed{col, row, label, v.Distance})
	}

	// arrowAt indexes the active animation frame's head + trail by cell. The
	// head is intensity 0 (bright); each trailing cell increases intensity.
	// Orbs still win the cell — the animation visualizes the path toward the
	// orb, not the orb itself.
	type arrowCell struct {
		glyph       rune
		intensity   int
		inward      bool
		calibrating bool
	}
	arrows := map[[2]int]arrowCell{}
	for _, v := range m.snapshot.Verifiers {
		frame, active, inward, calibrating := m.animInfo(v.Name, v.Running)
		if !active || v.Distance <= 0 {
			continue
		}
		glyphMap := directionArrow
		if inward {
			glyphMap = directionArrowInward
		}
		glyph, ok := glyphMap[v.Direction]
		if !ok {
			continue
		}
		// Outward: head starts near center and moves to orb (progress 0→1).
		// Inward: head starts near orb and moves to center (progress 1→0).
		for t := 0; t <= arrowTrailLen; t++ {
			step := frame + 1 - t
			if step <= 0 {
				break
			}
			var progress float64
			if inward {
				progress = 1.0 - float64(step)/float64(arrowAnimFrames)
			} else {
				progress = float64(step) / float64(arrowAnimFrames)
			}
			col, row, ok := project(v.Direction, v.Distance*progress, w, h)
			if !ok || (col == cx && row == cy) {
				continue
			}
			key := [2]int{col, row}
			if existing, exists := arrows[key]; exists && existing.intensity <= t {
				continue
			}
			arrows[key] = arrowCell{glyph: glyph, intensity: t, inward: inward, calibrating: calibrating}
		}
	}

	var sb strings.Builder
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			// Orbs draw on top of the goal circle and axes — a verifier with
			// distance 0 is meant to overlap the goal, signalling convergence.
			placedHere := false
			for _, p := range placements {
				if p.col == c && p.row == r {
					sb.WriteString(orbStyle(p.distance).Render(string(p.label)))
					placedHere = true
					break
				}
			}
			if placedHere {
				continue
			}
			if a, ok := arrows[[2]int{c, r}]; ok {
				var style lipgloss.Style
				switch {
				case a.calibrating && a.intensity == 0:
					style = styleArrowCalHead
				case a.calibrating:
					style = styleArrowCalTrail
				case a.inward && a.intensity == 0:
					style = styleArrowInHead
				case a.inward:
					style = styleArrowInTrail
				case a.intensity == 0:
					style = styleArrowOutHead
				default:
					style = styleArrowOutTrail
				}
				sb.WriteString(style.Render(string(a.glyph)))
				continue
			}
			if c == cx && r == cy {
				sb.WriteString(styleGoal.Render(goalGlyph))
				continue
			}
			if cells[r][c] == '·' {
				sb.WriteString(styleAxis.Render("·"))
			} else {
				sb.WriteRune(cells[r][c])
			}
		}
		if r < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m Model) renderList() string {
	if len(m.snapshot.Verifiers) == 0 {
		return styleReason.Render("(no verifiers configured)")
	}
	var b strings.Builder
	for _, v := range m.snapshot.Verifiers {
		label := fmt.Sprintf("%c", first(v.Name))
		head := fmt.Sprintf("[%s] %-12s %s  ", label, v.Name, v.Direction) +
			orbStyle(v.Distance).Render(fmt.Sprintf("d=%.2f", v.Distance))
		if v.Running {
			head += "  " + styleRunning.Render("running") + " " + renderSnake(m.tick)
		}
		b.WriteString(head)
		if v.Reason != "" {
			b.WriteString("  ")
			b.WriteString(styleReason.Render(truncate(v.Reason, 80)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// snakeTrack is the total number of positions in the inline snake marquee;
// snakeBody is the number of lit body segments. The snake head advances one
// position per tick, wrapping around.
const (
	snakeTrack = 8
	snakeBody  = 3
)

// renderSnake renders a bracketed inline spinner: a snake of snakeBody lit
// segments (█) travels along a snakeTrack-cell track, with empty positions
// shown as░. The result is always "[" + snakeTrack cells + "]".
func renderSnake(tick int) string {
	head := ((tick % snakeTrack) + snakeTrack) % snakeTrack
	var lit [snakeTrack]bool
	for i := 0; i < snakeBody; i++ {
		lit[(head-i+snakeTrack)%snakeTrack] = true
	}
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < snakeTrack; i++ {
		if lit[i] {
			sb.WriteString(styleSnakeOn.Render("█"))
		} else {
			sb.WriteString(styleSnakeOff.Render("░"))
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

func first(s string) byte {
	if s == "" {
		return '?'
	}
	return s[0]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
