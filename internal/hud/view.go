package hud

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/uriahlevy/hud/internal/daemon"
	"github.com/uriahlevy/hud/internal/gitstats"
	"github.com/uriahlevy/hud/internal/ipc"
)

// brandBgColor is the lipgloss.Color for brandBg, hoisted into a var so
// every per-cell style can chain .Background(brandBgColor) without re-
// allocating the conversion. We bake the brand bg into *all* styles that
// render inside a framed surface because lipgloss embeds `\033[0m` reset
// codes around each inner style — and a reset wipes the parent box's bg,
// punching the terminal's real background through the rendered frame as
// black patches. Setting the bg explicitly on every inner cell means the
// trailing reset returns to the brand bg, not the terminal default.
var brandBgColor = lipgloss.Color(brandBg)

// brandBgSeq is the SGR sequence lipgloss emits when setting the brand
// background under the currently-active color profile. We capture it once
// and splice it back in after every embedded `\033[0m` reset inside framed
// surfaces (via reanchorBrandBg) — without this re-anchor, inter-cell
// gaps and trailing padding spaces show terminal-default black the moment
// any inner style closes itself.
var brandBgSeq = extractBgSeq(brandBgColor)

func extractBgSeq(c lipgloss.Color) string {
	const probe = "·"
	rendered := lipgloss.NewStyle().Background(c).Render(probe)
	idx := strings.Index(rendered, probe)
	if idx <= 0 {
		return ""
	}
	return rendered[:idx]
}

// reanchorBrandBg post-processes the inner content of a framed surface so
// every inline `\033[0m` reset is immediately followed by re-anchoring the
// brand bg. Apply it to the *inner* content (before passing to the box
// border's .Render) so the box's own final reset still fires last and the
// terminal returns to its default state at the box edge.
func reanchorBrandBg(s string) string {
	if brandBgSeq == "" {
		return s
	}
	return strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+brandBgSeq)
}

// styleSpacerBg paints brand-bg blocks that bridge seams between adjacent
// boxes — used for the 2-column gutter between the compass and the event
// log so the warm graphite carries across instead of dropping to terminal
// black.
var styleSpacerBg = lipgloss.NewStyle().Background(brandBgColor)

var (
	styleHeader        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Background(brandBgColor)
	styleGrid          = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brandCoral)).BorderBackground(brandBgColor).Background(brandBgColor).Padding(0, 1)
	styleGoalDot       = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Background(brandBgColor)
	styleAxis          = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(brandBgColor)
	styleRunning       = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Background(brandBgColor)
	styleVerifierLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("211")).Background(brandBgColor).Bold(true)
	styleReason        = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(brandBgColor)
	styleGoalLbl       = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Background(brandBgColor)
	styleArrowOutHead  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(brandBgColor).Bold(true)
	styleArrowOutTrail = lipgloss.NewStyle().Foreground(lipgloss.Color("88")).Background(brandBgColor)
	styleArrowInHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(brandBgColor).Bold(true)
	styleArrowInTrail  = lipgloss.NewStyle().Foreground(lipgloss.Color("28")).Background(brandBgColor)
	styleHeaderBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brandCoral)).BorderBackground(brandBgColor).Background(brandBgColor).Padding(0, 1).Foreground(lipgloss.Color("252"))
	// styleHeaderBrand mirrors styleLandingBanner from the splash: the
	// in-header ANSI-shadow "HUD" wordmark is rendered with the same
	// solid coral on warm graphite, bold, no animation — so the splash
	// and the main HUD share one wordmark identity.
	styleHeaderBrand = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoral)).Background(brandBgColor).Bold(true)
	styleListBorder  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brandCoral)).BorderBackground(brandBgColor).Background(brandBgColor)
	styleListTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(brandCoralSoft)).Background(brandBgColor)
	styleListSlash   = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoral)).Background(brandBgColor)
	// styleListSelected keeps its coral bg — that bar is the cursor.
	styleListSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color(brandCoral)).Bold(true)
	styleHeaderLabel  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(brandBgColor)
	styleSessionOn    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(brandBgColor).Bold(true)
	styleWind         = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(brandBgColor).Bold(true)
	styleDisabled     = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(brandBgColor)
	// styleFooterKeys keeps its own grey chip bg for intentional contrast.
	styleFooterKeys   = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238")).Bold(true).Padding(0, 1)
	styleErrorBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(brandBgColor).Bold(true)
	styleUnknownBadge = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(brandBgColor).Bold(true)
	styleStaleBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Background(brandBgColor)
	stylePendingBadge = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(brandBgColor)
	styleRemoteBadge  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Background(brandBgColor)
	styleCostBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(brandBgColor)
	styleDiffAdded    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(brandBgColor).Bold(true)
	styleDiffRemoved  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(brandBgColor).Bold(true)
	styleDiffBinary   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Background(brandBgColor)
	styleGitBranch    = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Background(brandBgColor).Bold(true)
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
	return lipgloss.NewStyle().Foreground(color).Background(brandBgColor).Bold(true)
}

// View satisfies tea.Model.
func (m Model) View() string {
	if m.palette != nil {
		return m.palette.View()
	}
	if m.switcher != nil {
		return m.switcher.View()
	}
	if m.gitPanel != nil {
		return m.gitPanel.View()
	}
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
	// 5 is the smallest compass that still places labels around all 8 wind
	// markers; below that the layout collapses. The min keeps the compass
	// usable on a 24-row terminal without pushing the view past the bottom
	// edge once the "Verifiers ////" banner is accounted for.
	if gridH < 5 {
		gridH = 5
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
	compass := styleGrid.Render(reanchorBrandBg(m.renderGrid(gridW, gridH)))
	if logW > 0 {
		// gutter is a 2-column brand-bg block sitting between compass and
		// event log. JoinHorizontal pads shorter inputs with empty cells
		// (terminal-default bg), so the gutter must be rendered at the same
		// height as the boxes it bridges — otherwise its single styled row
		// would paint just the top edge and leave the rest of the seam
		// dropping to terminal black.
		compassH := lipgloss.Height(compass)
		gutterCell := styleSpacerBg.Render("  ")
		gutter := strings.Repeat(gutterCell+"\n", compassH-1) + gutterCell
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, compass, gutter, m.renderEventLog(logW, gridH)))
	} else {
		b.WriteString(compass)
	}
	b.WriteString("\n")
	listInnerW := m.width - styleListBorder.GetHorizontalFrameSize()
	if listInnerW < 1 {
		listInnerW = 1
	}
	b.WriteString(styleListBorder.Render(reanchorBrandBg(m.renderList(listInnerW))))
	return b.String()
}

// renderGitFileRow formats one file row used by the git changes popup: path
// on the left, +added/-removed (or "bin") chips on the right.
func renderGitFileRow(f gitstats.FileStat, pathW, countW int) string {
	path := truncate(f.Path, pathW)
	path = padCell(path, pathW)
	var added, removed string
	if f.Binary {
		added = styleDiffBinary.Render(padCell("bin", countW))
		removed = styleDiffBinary.Render(padCell("", countW))
	} else {
		added = styleDiffAdded.Render(padCell(fmt.Sprintf("+%d", f.Added), countW))
		removed = styleDiffRemoved.Render(padCell(fmt.Sprintf("-%d", f.Removed), countW))
	}
	return path + "  " + added + removed
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// hudHeaderBanner is the compact ANSI-shadow "HUD" wordmark stamped on
// the right side of the main HUD header. Same chunky block aesthetic as
// the splash banner for brand continuity, but trimmed to the three-letter
// short form and compressed to 4 rows so it sits inside the existing
// header band without adding vertical space (and so the view still fits
// the classic 80×24 terminal). The splash keeps the full 6-row
// "KIKAITE HUD" since it owns its own screen.
const hudHeaderBanner = `██╗  ██╗██╗   ██╗██████╗
███████║██║   ██║██║  ██║
██║  ██║╚██████╔╝██████╔╝
╚═╝  ╚═╝ ╚═════╝ ╚═════╝ `

// renderHeader builds the framed metadata box at the top of the screen.
// The box stretches to the same total width as the grid so it visually
// anchors over the compass below. The right column hosts the ANSI-shadow
// "HUD" wordmark — same font as the splash banner, just the short form so
// it fits beside the telemetry without dominating. Sized to fit on
// 80-cell-or-wider terminals; on narrower widths lipgloss clips the
// banner from the right rather than spawning a separate fallback.
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

	bannerLines := strings.Split(hudHeaderBanner, "\n")
	brandW := 0
	for _, ln := range bannerLines {
		if w := lipgloss.Width(ln); w > brandW {
			brandW = w
		}
	}
	const gutter = 2

	leftW := max(contentW-brandW-gutter, 0)
	sessionText := "active"
	if m.snapshot.SessionCount > 1 {
		sessionText = fmt.Sprintf("%s (%d)", daemon.WorktreeLabel(m.snapshot.DisplayedWorktree), m.snapshot.SessionCount)
	} else if m.snapshot.Worktree != "" {
		sessionText = daemon.WorktreeLabel(m.snapshot.Worktree)
	}
	leftLines := append(
		[]string{styleHeaderLabel.Render("version: ") + ver +
			"  " + styleHeaderLabel.Render("session: ") + styleSessionOn.Render(sessionText)},
		m.headerTelemetryRows(leftW)...,
	)

	// Compose rows manually: pad each metadata line to leftW, lay down
	// the gutter, then stamp the banner line styled coral. Banner lines
	// are padded to brandW so trailing spaces stay inside the brand bg.
	rows := make([]string, len(bannerLines))
	for i, bl := range bannerLines {
		var left string
		if i < len(leftLines) {
			left = leftLines[i]
		}
		left = padToWidth(left, leftW)
		banner := styleHeaderBrand.Render(padBannerLine(bl, brandW))
		rows[i] = left + strings.Repeat(" ", gutter) + banner
	}
	return styleHeaderBox.Width(styleW).Render(reanchorBrandBg(strings.Join(rows, "\n")))
}

// padToWidth right-pads s with spaces so it occupies exactly width cells.
// Truncates if s already exceeds width so the side-by-side layout never
// lets a long row push the banner column.
func padToWidth(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w > width {
		return truncate(s, width)
	}
	return s + strings.Repeat(" ", width-w)
}

// padBannerLine right-pads a banner row with spaces so each ANSI-shadow
// row has the same cell width before it's wrapped in the brand style;
// without this the trailing-space line and the wider rows render with
// different widths and trailing-space gaps break the brand bg.
func padBannerLine(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// headerTelemetryRows returns the non-version rows of the header: socket /
// mcp last-seen + verifier count + cumulative cost, the optional git
// workspace summary, and the goal row. Each row is truncated to colW so
// the side-by-side layout doesn't wrap the metadata column under the
// banner block. Pulled out so side-by-side and compact layouts share one
// source of truth for what the column contains.
func (m Model) headerTelemetryRows(colW int) []string {
	var rows []string

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
	telemetry := styleHeaderLabel.Render("last socket: ") + formatTimestamp(m.snapshot.LastSocketAt) +
		"  " + styleHeaderLabel.Render("last mcp: ") + formatTimestamp(m.snapshot.LastMCPAt) +
		"  " + styleHeaderLabel.Render("verifiers: ") + fmt.Sprintf("%d/%d", enabled, len(m.snapshot.Verifiers)) +
		costSummary
	rows = append(rows, truncate(telemetry, colW))

	if git := m.renderGitHeaderRow(colW); git != "" {
		rows = append(rows, git)
	}

	goal := m.snapshot.Goal
	goalRow := styleGoalLbl.Render("goal: ")
	if goal == "" {
		goalRow += styleReason.Render("(none — submit a prompt or run `hud goal ...`)")
	} else {
		goalRow += truncate(goal, colW-len("goal: ")-2)
	}
	rows = append(rows, goalRow)
	return rows
}

// renderGitHeaderRow renders the single-line git summary always shown in
// the header. Worktree and branch on the left; "+added -removed" diff
// summary on the right. When there is no git context (running outside a
// repo, no session base ref) it renders an em-dash placeholder so the
// header height stays stable.
func (m Model) renderGitHeaderRow(contentW int) string {
	ws := m.workspace
	if ws.WorktreeName == "" && ws.Branch == "" && len(ws.Files) == 0 {
		// Render nothing rather than a dim placeholder; the caller drops
		// the row entirely so the header doesn't lose vertical real estate
		// outside a git repo (or in tests that don't populate workspace).
		return ""
	}
	row := styleHeaderLabel.Render("git: ")
	if ws.WorktreeName != "" {
		row += ws.WorktreeName
	}
	if ws.Branch != "" {
		if ws.WorktreeName != "" {
			row += styleHeaderLabel.Render("/")
		}
		row += styleGitBranch.Render(ws.Branch)
	}
	row += "  " + renderDiffSummary(ws.TotalAdded, ws.TotalRemoved, len(ws.Files))
	if len(ws.Files) > 0 {
		row += "  " + styleHeaderLabel.Render("(g for details)")
	}
	return truncate(row, contentW)
}

// renderDiffSummary formats "+N -M" with the standard diff colors. When
// nothing has changed yet we render a dim "no changes" hint so the header
// still has stable width.
func renderDiffSummary(added, removed, fileCount int) string {
	if fileCount == 0 && added == 0 && removed == 0 {
		return styleReason.Render("(no changes since session start)")
	}
	return styleDiffAdded.Render(fmt.Sprintf("+%d", added)) +
		" " +
		styleDiffRemoved.Render(fmt.Sprintf("-%d", removed)) +
		"  " +
		styleHeaderLabel.Render(fmt.Sprintf("%d file%s", fileCount, plural(fileCount)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
	for r := range cells {
		cells[r] = make([]rune, w)
		for c := range cells[r] {
			cells[r][c] = ' '
		}
	}
	cx, cy := w/2, h/2

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
		// Use the smoothed orb position so the marker glides across cells
		// when distance/direction change. Labels still anchor to the marker's
		// current cell so they ride along with the orb.
		col, row, ok := m.orbPosition(v.Name, v.Direction, v.Distance, w, h)
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
					glyph := p.glyph
					// Very-near-goal orbs occasionally twinkle into a sparkle
					// glyph so the user perceives "convergence" without
					// having to read the distance number.
					if p.distance <= 0.08 && (m.tick/3)%2 == 0 {
						glyph = sparkleGlyph(m.tick)
					}
					sb.WriteString(orbStyleFlare(p.distance, m.tick).Render(string(glyph)))
					markerHere = true
					break
				}
			}
			if markerHere {
				continue
			}
			if c == cx && r == cy {
				sb.WriteString(pulseStyle(m.tick).Render(goalGlyphAt(m.tick)))
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
			// Halo sits behind labels and arrows but in front of the static
			// reticle/wind/needle layers. This is the "bigger strobe" — eight
			// cells around the goal throb in lockstep with the centre pulse so
			// the convergence cue is visible from peripheral vision.
			if dc, dr := c-cx, r-cy; absInt(dc) <= 1 && absInt(dr) <= 1 {
				sb.WriteString(haloCellStyle(m.tick, 1).Render(haloGlyph(dc, dr)))
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
			sb.WriteRune(cells[r][c])
		}
		if r < h-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
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
	// title + column header + body + footer help.
	if len(m.snapshot.Verifiers) == 0 {
		return 4 + border
	}
	return len(m.snapshot.Verifiers) + 3 + border
}

func (m Model) renderList(maxWidth int) string {
	if len(m.snapshot.Verifiers) == 0 {
		return renderListTitleRow(maxWidth) + "\n" +
			renderListHeader(maxWidth) + "\n" +
			styleReason.Render(truncate("(no verifiers configured)", maxWidth)) + "\n" +
			m.renderFooterHelp(maxWidth)
	}
	var b strings.Builder
	b.WriteString(renderListTitleRow(maxWidth))
	b.WriteString("\n")
	b.WriteString(renderListHeader(maxWidth))
	b.WriteString("\n")
	for i, v := range m.snapshot.Verifiers {
		b.WriteString(m.renderListRow(i, v, maxWidth))
		b.WriteString("\n")
	}
	b.WriteString(m.renderFooterHelp(maxWidth))
	return b.String()
}

// renderListTitleRow draws the "Verifiers /////" banner that anchors the
// browser panel to the same visual family as the ctrl+P command palette: the
// title word in soft coral, the trailing slashes filling the rest of the
// inner width in saturated coral.
func renderListTitleRow(innerW int) string {
	title := "Verifiers "
	slashCount := max(innerW-lipgloss.Width(title), 0)
	return styleListTitle.Render(title) + styleListSlash.Render(strings.Repeat("/", slashCount))
}

func (m Model) renderFooterHelp(maxWidth int) string {
	text := "keys: up/down select | enter status | space toggle | r run one | t all | esc stop | ctrl+w sessions | q quit | 1-9/0 toggle | ctrl+p commands"
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

	return styleGrid.Render(reanchorBrandBg(strings.Join(rows, "\n")))
}

// renderEventRow wraps the message across multiple rows up to maxLines.
// The styled "HH:MM:SS LVL message" line is produced by charmbracelet/log
// when the entry is created; this function only handles visual wrapping and
// continuation indent. Older or test-constructed entries without a Rendered
// payload fall back to the manual badge layout so callers don't have to go
// through State to make a presentable row.
func renderEventRow(e daemon.EventEntry, width, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if e.Rendered == "" {
		return renderEventRowFallback(e, width, maxLines)
	}

	// charm/log's TextFormatter always emits "HH:MM:SS LVL …" with single
	// spaces; that's 13 visible chars before the message column. Indenting
	// continuations under the message keeps multi-line entries readable on a
	// narrow panel without re-parsing the ANSI-escaped line.
	const prefixW = 13
	msgW := width - prefixW
	if msgW < 4 {
		// Panel is too narrow for an indented body — just clip the styled
		// line and let the user widen the terminal to see more.
		return []string{padCell(ellipsisTail(e.Rendered, width), width)}
	}

	lines := wrapVisualLines(e.Rendered, width)
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[len(lines)-1] = ellipsisTail(strings.TrimRight(lines[len(lines)-1], " "), width)
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// Seal each row with a reset so an SGR that happens to straddle the
		// wrap point (charm/log's level badge, a styled message segment) can't
		// leak its colour into the panel border one cell to the right.
		out = append(out, padCell(line, width)+ansi.ResetStyle)
	}
	return out
}

// renderEventRowFallback is the pre-charmlog rendering path. It exists so
// EventEntry values constructed by hand (e.g. unit tests, or any future
// non-Logger writer) still display correctly.
func renderEventRowFallback(e daemon.EventEntry, width, maxLines int) []string {
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

// wrapVisualLines hard-wraps s to width columns of *visible* output. It must
// be ANSI-aware because charm/log's rendered lines contain SGR escapes; a
// naive rune-by-rune split (e.g. relying on lipgloss.Width per rune) only
// treats the ESC byte itself as zero-width and counts the rest of the
// sequence as printable, which both undercounts the wrap budget and can
// guillotine an SGR mid-sequence — that's exactly what caused the event log
// panel to chop "INF" down to "I" and bleed cyan into the right border.
func wrapVisualLines(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	wrapped := ansi.Hardwrap(s, width, false)
	return strings.Split(wrapped, "\n")
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
	selected := i == m.selectedVerifier
	label := "[" + toggleLabel(i) + "]"

	if selected {
		// Mirror the ctrl+P palette: build the row in plain text and paint
		// the entire line in white-on-coral so the highlight reads as one
		// solid bar. Per-cell colors are intentionally dropped here because
		// the selected style overrides the row's foreground uniformly.
		row := ">" +
			tableCell(label, layout.keyW) + listColumnGap +
			tableCell(v.Name, layout.nameW) + listColumnGap +
			tableCell(v.Direction, layout.dirW) + listColumnGap +
			tableCell(verifierType(v), layout.typeW) + listColumnGap +
			tableCell(plainStatusText(v), layout.statusW)
		if layout.reasonW > 0 {
			row += listColumnGap + tableCell(v.Reason, layout.reasonW)
		}
		row = truncate(row, maxWidth)
		if pad := maxWidth - lipgloss.Width(row); pad > 0 {
			row += strings.Repeat(" ", pad)
		}
		return styleListSelected.Render(row)
	}

	name := styledTableCell(v.Name, layout.nameW, lipgloss.NewStyle())
	kind := styledTableCell(verifierType(v), layout.typeW, lipgloss.NewStyle())
	if v.Disabled {
		name = styledTableCell(v.Name, layout.nameW, styleDisabled)
		kind = styledTableCell(verifierType(v), layout.typeW, styleDisabled)
	}

	status := renderStatusCell(v, layout.statusW, m.tick)

	row := " " +
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

// plainStatusText returns the verifier's status cell as plain text (no SGR
// styling) for use inside the painted selection bar — applying status colors
// there would fight the uniform white-on-coral highlight.
func plainStatusText(v ipc.VerifierStatus) string {
	if v.Running {
		return "● run"
	}
	switch {
	case v.Disabled:
		return "off"
	case v.Status == ipc.StatusError:
		return "err  d=" + fmt.Sprintf("%.2f", v.Distance)
	case v.Status == ipc.StatusUnknown:
		return "?    d=" + fmt.Sprintf("%.2f", v.Distance)
	case v.Status == ipc.StatusStale:
		return "stale d=" + fmt.Sprintf("%.2f", v.Distance)
	case v.Status == ipc.StatusPending:
		return "-"
	default:
		return fmt.Sprintf("d=%.2f", v.Distance)
	}
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
		// Pair the legacy bracketed dot (kept so the layout & tests stay
		// stable) with a braille spinner glyph for a richer "working"
		// signal; the row's foreground hue glides through magenta/cyan.
		text := string(brailleSpinner(tick)) + " run"
		return styledTableCell(text, width, runningGlow(tick))
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
