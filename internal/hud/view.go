package hud

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/ipc"
)

var (
	styleHeader        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleGrid          = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	styleGoal          = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleGoalDot       = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleAxis          = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleRing          = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	styleRunning       = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleVerifierLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("211")).Bold(true)
	styleReason        = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleGoalLbl       = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleArrowOutHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleArrowOutTrail = lipgloss.NewStyle().Foreground(lipgloss.Color("88"))
	styleArrowInHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleArrowInTrail  = lipgloss.NewStyle().Foreground(lipgloss.Color("28"))
	styleHeaderBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Foreground(lipgloss.Color("252"))
	styleListBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("15"))
	styleHeaderLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleSessionOn     = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleWind          = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true)
	styleDisabled      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleFooterKeys    = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238")).Bold(true).Padding(0, 1)
	styleErrorBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleUnknownBadge  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
	styleStaleBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stylePendingBadge  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleRemoteBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleCostBadge     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
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

const (
	ringCell = iota + 1
	axisCell
)

var verifierMarkerGlyphs = []rune{'▲', '◆', '■', '✚', '△', '◇', '□', '▽'}

func verifierMarkerGlyph(index int) rune {
	if index < 0 {
		index = 0
	}
	return verifierMarkerGlyphs[index%len(verifierMarkerGlyphs)]
}

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
	if m.status != nil {
		return m.status.View()
	}
	if m.editor != nil {
		return m.editor.View()
	}
	if m.width == 0 {
		return "initializing..."
	}
	header := m.renderHeader(m.width)
	headerLines := strings.Count(header, "\n") + 1
	listLines := m.listLineCount()

	totalW := m.width - styleGrid.GetHorizontalFrameSize()
	gridW := totalW
	logW := 0
	if m.showEventLog {
		// Split the available width between compass and log panel. The log
		// gets ~a third, capped so it stays scannable on wide terminals and
		// doesn't squeeze the compass on narrow ones.
		logW = m.width / 3
		if logW < 36 {
			logW = 36
		}
		if logW > 60 {
			logW = 60
		}
		if gridW-logW-2 < 24 {
			// Terminal too narrow to host both side-by-side; suppress the
			// panel for this frame rather than shrink the compass into a
			// useless smudge.
			logW = 0
		} else {
			gridW = gridW - logW - 2
		}
	}
	gridH := m.height - headerLines - 2 - listLines
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
	compass := styleGrid.Render(m.renderGrid(gridW, gridH))
	if logW > 0 {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, compass, "  ", m.renderEventLog(logW, gridH)))
	} else {
		b.WriteString(compass)
	}
	b.WriteString("\n")
	listInnerW := m.width - styleListBorder.GetHorizontalFrameSize()
	if listInnerW < 1 {
		listInnerW = 1
	}
	b.WriteString(styleListBorder.Render(m.renderList(listInnerW)))
	return b.String()
}

// renderHeader builds the framed metadata box at the top of the screen. The
// box stretches to the same total width as the grid so it visually anchors
// over the compass below it.
func (m Model) renderHeader(totalW int) string {
	if totalW < 28 {
		totalW = 28
	}
	styleW := totalW - styleHeaderBox.GetHorizontalBorderSize()
	contentW := styleW - styleHeaderBox.GetHorizontalPadding()
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

	// Row 2 — telemetry: socket / mcp last-seen + verifier count + cumulative cost.
	enabled := 0
	var totalCostUSD float64
	var totalTokens int
	for _, v := range m.snapshot.Verifiers {
		if !v.Disabled {
			enabled++
		}
		if v.LastUsage != nil {
			totalCostUSD += v.LastUsage.CostUSD
			totalTokens += v.LastUsage.InputTokens + v.LastUsage.OutputTokens
		}
	}
	costSummary := ""
	if totalCostUSD > 0 || totalTokens > 0 {
		costSummary = "  " + styleHeaderLabel.Render("agent run: ") + styleCostBadge.Render(formatCost(totalCostUSD, totalTokens))
	}
	rows = append(rows,
		styleHeaderLabel.Render("last socket: ")+formatTimestamp(m.snapshot.LastSocketAt)+
			"  "+styleHeaderLabel.Render("last mcp: ")+formatTimestamp(m.snapshot.LastMCPAt)+
			"  "+styleHeaderLabel.Render("verifiers: ")+fmt.Sprintf("%d/%d", enabled, len(m.snapshot.Verifiers))+
			costSummary,
	)

	// Row 3 — goal summary, single line, truncated to fit.
	goal := m.snapshot.Goal
	goalRow := styleGoalLbl.Render("goal: ")
	if goal == "" {
		goalRow += styleReason.Render("(none — submit a prompt or run `hud goal ...`)")
	} else {
		goalRow += truncate(goal, contentW-len("goal: ")-2)
	}
	rows = append(rows, goalRow)

	return styleHeaderBox.Width(styleW).Render(strings.Join(rows, "\n"))
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
	if w <= 0 || h <= 0 {
		return ""
	}
	cells := make([][]rune, h)
	kinds := make([][]int, h)
	for r := range cells {
		cells[r] = make([]rune, w)
		kinds[r] = make([]int, w)
		for c := range cells[r] {
			cells[r][c] = ' '
		}
	}
	cx, cy := w/2, h/2

	drawDistanceRings(cells, kinds, w, h, cx, cy)

	// axis lines (subtle bearing reference)
	for c := 0; c < w; c++ {
		cells[cy][c] = '·'
		kinds[cy][c] = axisCell
	}
	for r := 0; r < h; r++ {
		cells[r][cx] = '·'
		kinds[r][cx] = axisCell
	}

	// One compact marker per verifier is projected from Direction + Distance.
	// The nearby label is offset and clamped inward so perimeter bearings stay
	// readable even when a verifier is at maximum distance.
	type placedGlyph struct {
		col, row int
		glyph    rune
		distance float64
		marker   bool
	}
	var placements []placedGlyph
	for i, v := range m.snapshot.Verifiers {
		if v.Disabled {
			continue
		}
		col, row, ok := project(v.Direction, v.Distance, w, h)
		if !ok {
			continue
		}
		name := strings.ToLower(v.Name)
		if name == "" {
			name = "?"
		}
		placements = append(placements, placedGlyph{col: col, row: row, glyph: verifierMarkerGlyph(i), distance: v.Distance, marker: true})

		name = truncate(name, labelMaxWidth(w))
		if name == "" {
			continue
		}
		runes := []rune(name)
		startCol, labelRow := labelPosition(col, row, v.Direction, len(runes), w, h)
		for i, ch := range runes {
			c := startCol + i
			if c < 0 || c >= w || labelRow < 0 || labelRow >= h {
				continue
			}
			placements = append(placements, placedGlyph{col: c, row: labelRow, glyph: ch, distance: v.Distance})
		}
	}

	// Wind-direction markers around the perimeter give the compass a frame of
	// reference even when no orb is sitting on a given axis.
	windCells := map[[2]int]rune{}
	addWind := func(text string, col, row int) {
		for i, ch := range text {
			c := col + i
			if c < 0 || c >= w || row < 0 || row >= h {
				continue
			}
			windCells[[2]int{c, row}] = ch
		}
	}
	addWind("N", cx, 0)
	addWind("S", cx, h-1)
	addWind("E", w-1, cy)
	addWind("W", 0, cy)
	addWind("NW", 0, 0)
	addWind("NE", w-2, 0)
	addWind("SW", 0, h-1)
	addWind("SE", w-2, h-1)

	// arrowAt indexes the active animation frame's head + trail by cell. The
	// head is intensity 0 (bright); each trailing cell increases intensity.
	// Markers still win the cell — the animation visualizes the path toward
	// the verifier, not the verifier itself.
	type arrowCell struct {
		glyph       rune
		intensity   int
		inward      bool
		calibrating bool
	}
	arrows := map[[2]int]arrowCell{}
	for _, v := range m.snapshot.Verifiers {
		if v.Disabled {
			continue
		}
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
			// Verifier markers are the top layer. A distance-0 marker is meant
			// to sit exactly on the reticle, signalling convergence.
			markerHere := false
			for _, p := range placements {
				if p.marker && p.col == c && p.row == r {
					sb.WriteString(orbStyle(p.distance).Render(string(p.glyph)))
					markerHere = true
					break
				}
			}
			if markerHere {
				continue
			}
			if c == cx && r == cy {
				sb.WriteString(styleGoal.Render(goalGlyph))
				continue
			}
			if a, ok := arrows[[2]int{c, r}]; ok {
				var style lipgloss.Style
				switch {
				case a.inward && a.intensity == 0:
					style = styleArrowInHead
				case a.inward:
					style = styleArrowInTrail
				case a.intensity == 0:
					style = styleArrowOutHead
				default:
					style = styleArrowOutTrail
				}
				glyph := a.glyph
				if a.intensity > 0 {
					glyph = '•'
				}
				sb.WriteString(style.Render(string(glyph)))
				continue
			}
			labelHere := false
			for _, p := range placements {
				if !p.marker && p.col == c && p.row == r {
					sb.WriteString(styleVerifierLabel.Render(string(p.glyph)))
					labelHere = true
					break
				}
			}
			if labelHere {
				continue
			}
			if glyph, ok := reticleGlyph(c, r, cx, cy, w, h); ok {
				sb.WriteString(styleGoalDot.Render(string(glyph)))
				continue
			}
			if wch, ok := windCells[[2]int{c, r}]; ok {
				sb.WriteString(styleWind.Render(string(wch)))
				continue
			}
			switch kinds[r][c] {
			case axisCell:
				sb.WriteString(styleAxis.Render("·"))
			case ringCell:
				sb.WriteString(styleRing.Render("·"))
			default:
				sb.WriteRune(cells[r][c])
			}
		}
		if r < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func drawDistanceRings(cells [][]rune, kinds [][]int, w, h, cx, cy int) {
	if w < 17 || h < 9 {
		return
	}
	maxRX := cx - 1
	maxRY := cy - 1
	if maxRX <= 0 || maxRY <= 0 {
		return
	}
	for _, band := range []float64{0.25, 0.50, 0.75} {
		rx := int(math.Round(float64(maxRX) * band))
		ry := int(math.Round(float64(maxRY) * band))
		if rx < 1 || ry < 1 {
			continue
		}
		samples := ringSampleCount(rx, ry)
		for i := 0; i < samples; i++ {
			theta := 2 * math.Pi * float64(i) / float64(samples)
			col := cx + int(math.Round(math.Cos(theta)*float64(rx)))
			row := cy - int(math.Round(math.Sin(theta)*float64(ry)))
			if col <= 0 || col >= w-1 || row <= 0 || row >= h-1 {
				continue
			}
			cells[row][col] = '·'
			kinds[row][col] = ringCell
		}
	}
}

func ringSampleCount(rx, ry int) int {
	// Ramanujan's first approximation is plenty for deciding how many terminal
	// cells to sample along a faint ring outline.
	a := float64(rx)
	b := float64(ry)
	circumference := math.Pi * (3*(a+b) - math.Sqrt((3*a+b)*(a+3*b)))
	samples := int(math.Ceil(circumference * 2))
	if samples < 16 {
		return 16
	}
	return samples
}

func reticleGlyph(c, r, cx, cy, w, h int) (rune, bool) {
	if c == cx && r == cy {
		return []rune(goalGlyph)[0], true
	}
	if w < 9 || h < 5 {
		return 0, false
	}
	if (c == cx && absInt(r-cy) == 1) || (r == cy && absInt(c-cx) == 2) {
		return '•', true
	}
	return 0, false
}

func labelMaxWidth(w int) int {
	if w <= 4 {
		return w
	}
	maxW := w - 4
	if maxW > 18 {
		return 18
	}
	return maxW
}

func labelPosition(col, row int, direction string, labelLen, w, h int) (int, int) {
	labelPad := 1
	if w < 8 || h < 5 {
		labelPad = 0
	}

	startCol := col - labelLen/2
	labelRow := row + 1
	switch direction {
	case "N":
		labelRow = row + 1
	case "S":
		labelRow = row - 1
	case "E":
		startCol = col - labelLen - 2
		labelRow = row + 1
	case "W":
		startCol = col + 2
		labelRow = row + 1
	case "NE":
		startCol = col - labelLen - 1
		labelRow = row + 1
	case "NW":
		startCol = col + 1
		labelRow = row + 1
	case "SE":
		startCol = col - labelLen - 1
		labelRow = row - 1
	case "SW":
		startCol = col + 1
		labelRow = row - 1
	}

	maxStart := w - labelLen - labelPad
	if maxStart < labelPad {
		maxStart = labelPad
	}
	startCol = clampInt(startCol, labelPad, maxStart)
	labelRow = clampInt(labelRow, labelPad, h-1-labelPad)
	return startCol, labelRow
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (m Model) listLineCount() int {
	border := styleListBorder.GetVerticalFrameSize()
	if len(m.snapshot.Verifiers) == 0 {
		return 3 + border
	}
	return len(m.snapshot.Verifiers) + 2 + border
}

func (m Model) renderList(maxWidth int) string {
	if len(m.snapshot.Verifiers) == 0 {
		return renderListHeader(maxWidth) + "\n" +
			styleReason.Render(truncate("(no verifiers configured)", maxWidth)) + "\n" +
			m.renderFooterHelp(maxWidth)
	}
	var b strings.Builder
	b.WriteString(renderListHeader(maxWidth))
	b.WriteString("\n")
	for i, v := range m.snapshot.Verifiers {
		b.WriteString(m.renderListRow(i, v, maxWidth))
		b.WriteString("\n")
	}
	b.WriteString(m.renderFooterHelp(maxWidth))
	return b.String()
}

func (m Model) renderFooterHelp(maxWidth int) string {
	text := "keys: up/down select | enter status | space toggle | r run one | t all | n new | e edit | l log | esc stop | q quit | 1-9/0 toggle"
	if m.footerNotice != "" && m.tick < m.footerNoticeUntil {
		text = m.footerNotice
	}
	innerW := maxWidth - styleFooterKeys.GetHorizontalPadding()
	if innerW < 1 {
		innerW = 1
	}
	return styleFooterKeys.Render(truncate(text, innerW))
}

// renderEventLog draws the toggleable side panel listing recent timestamped
// events (info + error) captured into State by the runner and callbacks.
// The box renders at the same height as the compass grid so the two visually
// align when shown side-by-side.
func (m Model) renderEventLog(width, contentH int) string {
	innerW := width - styleGrid.GetHorizontalFrameSize()
	if innerW < 12 {
		innerW = 12
	}
	if contentH < 3 {
		contentH = 3
	}

	rows := []string{
		padCell(styleHeader.Render("event log"), innerW),
		padCell(styleAxis.Render(strings.Repeat("─", innerW)), innerW),
	}
	bodyCap := contentH - len(rows)
	if bodyCap < 0 {
		bodyCap = 0
	}

	if len(m.events) == 0 {
		if bodyCap > 0 {
			rows = append(rows, padCell(styleReason.Render("(no events yet)"), innerW))
		}
	} else {
		// Walk newest-first so the latest event always wins room — it gets
		// truncated with "…" rather than dropped when it doesn't fully fit.
		var bodyRows []string
		remaining := bodyCap
		for i := len(m.events) - 1; i >= 0 && remaining > 0; i-- {
			evRows := renderEventRow(m.events[i], innerW, remaining)
			if len(evRows) == 0 {
				continue
			}
			bodyRows = append(evRows, bodyRows...)
			remaining -= len(evRows)
		}
		rows = append(rows, bodyRows...)
	}

	for len(rows) < contentH {
		rows = append(rows, padCell("", innerW))
	}

	return styleGrid.Render(strings.Join(rows, "\n"))
}

// renderEventRow wraps the message across multiple rows up to maxLines.
// Continuation lines are indented under the prefix; an overrun ends in "…".
func renderEventRow(e daemon.EventEntry, width, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	ts := e.At.Format("15:04:05")
	var level string
	switch e.Level {
	case daemon.EventError:
		level = styleErrorBadge.Render("ERR")
	case daemon.EventInfo:
		level = styleRemoteBadge.Render("INF")
	default:
		level = styleReason.Render(strings.ToUpper(string(e.Level)))
	}
	prefix := styleHeaderLabel.Render(ts) + " " + level + " "
	prefixW := lipgloss.Width(prefix)
	msgW := width - prefixW
	if msgW < 4 {
		return []string{padCell(prefix, width)}
	}

	lines := wrapVisualLines(e.Msg, msgW)
	if len(lines) == 0 {
		lines = []string{""}
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[len(lines)-1] = ellipsisTail(strings.TrimRight(lines[len(lines)-1], " "), msgW)
	}

	indent := strings.Repeat(" ", prefixW)
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			out = append(out, padCell(prefix+line, width))
		} else {
			out = append(out, padCell(indent+line, width))
		}
	}
	return out
}

func wrapVisualLines(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var rows []string
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			rows = append(rows, "")
			continue
		}
		for lipgloss.Width(line) > width {
			head, tail := splitVisual(line, width)
			rows = append(rows, head)
			line = tail
		}
		rows = append(rows, line)
	}
	return rows
}

// ellipsisTail differs from truncate: it always appends "…" even when s
// already fits, to mark that more content was cut off after this line.
func ellipsisTail(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	if lipgloss.Width(s)+1 <= width {
		return s + "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w+1 > width {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

func renderListHeader(maxWidth int) string {
	layout := listLayoutFor(maxWidth)
	header := listCursorPad +
		tableCell("key", layout.keyW) + listColumnGap +
		tableCell("verifier", layout.nameW) + listColumnGap +
		tableCell("dir", layout.dirW) + listColumnGap +
		tableCell("type", layout.typeW) + listColumnGap +
		tableCell("status", layout.statusW)
	if layout.reasonW > 0 {
		header += listColumnGap + tableCell("reason", layout.reasonW)
	}
	return styleHeaderLabel.Render(truncate(header, maxWidth))
}

func (m Model) renderListRow(i int, v ipc.VerifierStatus, maxWidth int) string {
	layout := listLayoutFor(maxWidth)
	cursor := " "
	if i == m.selectedVerifier {
		cursor = styleEditCursor.Render(">")
	}
	label := "[" + toggleLabel(i) + "]"
	name := styledTableCell(v.Name, layout.nameW, lipgloss.NewStyle())
	kind := styledTableCell(verifierType(v), layout.typeW, lipgloss.NewStyle())
	if v.Disabled {
		name = styledTableCell(v.Name, layout.nameW, styleDisabled)
		kind = styledTableCell(verifierType(v), layout.typeW, styleDisabled)
	}

	status := renderStatusCell(v, layout.statusW, m.tick)

	row := cursor +
		tableCell(label, layout.keyW) + listColumnGap +
		name + listColumnGap +
		tableCell(v.Direction, layout.dirW) + listColumnGap +
		kind + listColumnGap +
		status
	if layout.reasonW > 0 {
		reason := styledTableCell(v.Reason, layout.reasonW, styleReason)
		if v.Disabled {
			reason = styledTableCell(v.Reason, layout.reasonW, styleDisabled)
		}
		row += listColumnGap + reason
	}
	return truncate(row, maxWidth)
}

const (
	listCursorPad = " "
	listColumnGap = "  "
)

type listLayout struct {
	keyW    int
	nameW   int
	dirW    int
	typeW   int
	statusW int
	reasonW int
}

func listLayoutFor(maxWidth int) listLayout {
	layout := listLayout{
		keyW:    3,
		nameW:   12,
		dirW:    3,
		typeW:   7,
		statusW: 15,
	}
	fixed := lipgloss.Width(listCursorPad) +
		layout.keyW + layout.nameW + layout.dirW + layout.typeW + layout.statusW +
		5*lipgloss.Width(listColumnGap)
	layout.reasonW = maxWidth - fixed
	if layout.reasonW >= 3 {
		return layout
	}

	need := 3 - layout.reasonW
	if shrink := minInt(need, layout.nameW-4); shrink > 0 {
		layout.nameW -= shrink
		need -= shrink
	}
	if shrink := minInt(need, layout.statusW-6); shrink > 0 {
		layout.statusW -= shrink
		need -= shrink
	}
	layout.reasonW = 3 - need
	if layout.reasonW < 0 {
		layout.reasonW = 0
	}
	return layout
}

func tableCell(s string, width int) string {
	return padCell(truncateWithSuffix(s, width, "..."), width)
}

func styledTableCell(s string, width int, style lipgloss.Style) string {
	return padCell(style.Render(truncateWithSuffix(s, width, "...")), width)
}

func verifierType(v ipc.VerifierStatus) string {
	switch v.Config.Type {
	case "llm":
		return "agent"
	case "":
		return "command"
	default:
		return v.Config.Type
	}
}

// renderStatusCell picks the right style for the verifier's outcome
// state. Distinct rendering for ok/error/unknown/stale/disabled/pending
// is the whole point of the Status enum — collapsing them onto a magic
// distance value was the v0 footgun this fixes.
func renderStatusCell(v ipc.VerifierStatus, width, tick int) string {
	if v.Running {
		return styledTableCell("running "+plainDot(tick), width, styleRunning)
	}
	var text string
	var style lipgloss.Style
	switch {
	case v.Disabled:
		text = "off"
		style = styleDisabled
	case v.Status == ipc.StatusError:
		text = "err  d=" + fmt.Sprintf("%.2f", v.Distance)
		style = styleErrorBadge
	case v.Status == ipc.StatusUnknown:
		text = "?    d=" + fmt.Sprintf("%.2f", v.Distance)
		style = styleUnknownBadge
	case v.Status == ipc.StatusStale:
		text = "stale d=" + fmt.Sprintf("%.2f", v.Distance)
		style = styleStaleBadge
	case v.Status == ipc.StatusPending:
		text = "-"
		style = stylePendingBadge
	default:
		text = fmt.Sprintf("d=%.2f", v.Distance)
		style = orbStyle(v.Distance)
	}
	return styledTableCell(text, width, style)
}

// formatCost renders a cost-and-tokens chip. Below $0.01 we show the raw
// token count instead of "$0.00" so users can still see *something*.
func formatCost(cost float64, tokens int) string {
	if cost >= 0.01 {
		return fmt.Sprintf("$%.3f / %d tok", cost, tokens)
	}
	if tokens > 0 {
		return fmt.Sprintf("%d tok", tokens)
	}
	return ""
}

func toggleLabel(i int) string {
	switch {
	case i >= 0 && i < 9:
		return fmt.Sprintf("%d", i+1)
	case i == 9:
		return "0"
	default:
		return " "
	}
}

// dotTrack is the number of positions in the ping-pong dot spinner. The dot
// bounces left-to-right and back, completing one full cycle every
// (dotTrack-1)*2 ticks.
const dotTrack = 5

// renderDot renders a bracketed ping-pong spinner: a single "." bounces
// across a dotTrack-wide field. Result is always "[" + dotTrack cells + "]".
func renderDot(tick int) string {
	pos := dotPosition(tick)
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < dotTrack; i++ {
		if i == pos {
			sb.WriteString(styleRunning.Render("."))
		} else {
			sb.WriteByte(' ')
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

func plainDot(tick int) string {
	pos := dotPosition(tick)
	var sb strings.Builder
	sb.WriteByte('[')
	for i := 0; i < dotTrack; i++ {
		if i == pos {
			sb.WriteByte('.')
		} else {
			sb.WriteByte(' ')
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

func dotPosition(tick int) int {
	period := (dotTrack - 1) * 2
	phase := ((tick % period) + period) % period
	pos := phase
	if pos >= dotTrack {
		pos = period - phase
	}
	return pos
}

func truncate(s string, n int) string {
	return truncateWithSuffix(s, n, "…")
}

func truncateWithSuffix(s string, n int, suffix string) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	suffixW := lipgloss.Width(suffix)
	if suffixW >= n {
		return truncateSuffix(suffix, n)
	}
	var b strings.Builder
	for _, r := range s {
		if lipgloss.Width(b.String()+string(r)+suffix) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + suffix
}

func truncateSuffix(suffix string, n int) string {
	var b strings.Builder
	for _, r := range suffix {
		if lipgloss.Width(b.String()+string(r)) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func padCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-lipgloss.Width(s))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
