package sidekick

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/meloniteai/sidekick/internal/ipc"
	"github.com/meloniteai/sidekick/internal/telemetry"
)

// telemetryRefreshTicks throttles how often the panel re-queries the SQLite
// store. Verifier runs and heartbeats land seconds apart, so a sub-second
// re-query would just burn cycles; at 133ms/tick this is ~1s — live enough to
// watch a sparkline grow without hammering the database every frame.
const telemetryRefreshTicks = 8

// sparkLevels are the eight block heights a distance is quantised to. Index 0
// (▁) is the goal floor; index 7 (█) is maximally far. A converging verifier's
// sparkline therefore descends left-to-right toward the floor.
var sparkLevels = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// telemetryPanel is the toggleable session-telemetry side panel. It owns a
// read-only handle to the per-repo telemetry database (the daemon in this same
// process is the WAL-mode writer) and caches the last Summary it read so the
// View can render without doing I/O on the render path.
//
// Presence of a non-nil *telemetryPanel on the Model is the "shown" toggle,
// mirroring gitPanel/browser; unlike those it is not a modal — it renders
// beside the compass like the event log and never captures input.
type telemetryPanel struct {
	store   *telemetry.Store
	dbPath  string
	openErr error // why the database couldn't be opened (surfaced in the panel)

	sessionID string            // episode currently scoped to (may change live)
	summary   telemetry.Summary // last successful read
	queryErr  error             // why the last LoadSummary failed, if any
	loaded    bool              // a summary has been read at least once
	lastFetch int               // tick of the last query
}

// openTelemetryPanel toggles the panel on, opening the read-only store for the
// displayed session's repo and doing an initial read so the first frame is
// populated. Called from the palette action and the bare hotkey.
func (m *Model) openTelemetryPanel() {
	tv := &telemetryPanel{}
	m.telemetryView = tv
	m.refreshTelemetryPanel(true)
}

// closeTelemetryPanel toggles the panel off and releases the read-only handle.
func (m *Model) closeTelemetryPanel() {
	if m.telemetryView == nil {
		return
	}
	if m.telemetryView.store != nil {
		_ = m.telemetryView.store.Close()
	}
	m.telemetryView = nil
}

// toggleTelemetryPanel flips the panel, mirroring toggleGitPanel.
func (m *Model) toggleTelemetryPanel() {
	if m.telemetryView != nil {
		m.closeTelemetryPanel()
		return
	}
	m.openTelemetryPanel()
}

// refreshTelemetryPanel re-resolves the active episode and re-queries the store,
// throttled to telemetryRefreshTicks unless force is set (panel just opened or a
// session switch happened). It (re)opens the database when the path changes or a
// previous open failed, so the panel recovers once the daemon creates the file
// after the first recorded event.
func (m *Model) refreshTelemetryPanel(force bool) {
	tv := m.telemetryView
	if tv == nil {
		return
	}
	if !force && tv.lastFetch != 0 && m.tick-tv.lastFetch < telemetryRefreshTicks {
		return
	}
	tv.lastFetch = m.tick
	if tv.lastFetch == 0 {
		tv.lastFetch = 1 // reserve 0 as the "never fetched" sentinel
	}

	state := m.currentState()
	if state == nil {
		tv.openErr = fmt.Errorf("no active session")
		return
	}
	tv.sessionID = state.TelemetrySessionID()

	path, err := ipc.TelemetryDBPath(state.SessionWorktree())
	if err != nil {
		tv.openErr = err
		return
	}
	// Reopen when the path changed (session switched to a different repo) or a
	// prior open failed and the file may now exist.
	if tv.store == nil || path != tv.dbPath {
		if tv.store != nil {
			_ = tv.store.Close()
			tv.store = nil
		}
		tv.dbPath = path
		tv.openErr = nil
		if _, statErr := os.Stat(path); statErr != nil {
			tv.openErr = fmt.Errorf("no telemetry database yet")
			return
		}
		store, openErr := telemetry.OpenReadOnly(path)
		if openErr != nil {
			tv.openErr = openErr
			return
		}
		tv.store = store
	}

	sum, qErr := telemetry.LoadSummary(tv.store.DB(), tv.sessionID)
	if qErr != nil {
		tv.queryErr = qErr
		return
	}
	tv.queryErr = nil
	tv.summary = sum
	tv.loaded = true
}

// renderTelemetryPanel draws the boxed panel at the same height as the compass
// so it aligns when shown side-by-side, mirroring renderEventLog's structure.
func (m Model) renderTelemetryPanel(width, contentH int) string {
	innerW := width - styleGrid.GetHorizontalFrameSize()
	if innerW < 12 {
		innerW = 12
	}
	if contentH < 3 {
		contentH = 3
	}

	rows := []string{
		padCell(styleHeader.Render("session telemetry"), innerW),
		padCell(styleAxis.Render(strings.Repeat("─", innerW)), innerW),
	}
	bodyCap := contentH - len(rows)
	if bodyCap < 0 {
		bodyCap = 0
	}
	rows = append(rows, m.telemetryBodyRows(innerW, bodyCap)...)

	for len(rows) < contentH {
		rows = append(rows, padCell("", innerW))
	}
	if len(rows) > contentH {
		rows = rows[:contentH]
	}
	return styleGrid.Render(reanchorBrandBg(strings.Join(rows, "\n")))
}

// telemetryBodyRows builds the panel body, never exceeding maxLines. It flags
// the absence of data explicitly — disabled/missing database, no active goal,
// query failure, or an unrecorded episode each get their own warning row rather
// than rendering a misleadingly empty summary.
func (m Model) telemetryBodyRows(innerW, maxLines int) []string {
	tv := m.telemetryView
	if tv == nil || maxLines <= 0 {
		return nil
	}
	switch {
	case tv.openErr != nil:
		return warnRows(innerW, maxLines, "telemetry unavailable", tv.openErr.Error(),
			"(set SIDEKICK_TELEMETRY=on and a goal)")
	case tv.sessionID == "":
		return warnRows(innerW, maxLines, "no telemetry session",
			"set a goal to begin recording this episode")
	case tv.queryErr != nil:
		return warnRows(innerW, maxLines, "query failed", tv.queryErr.Error())
	case tv.loaded && !tv.summary.SessionFound:
		return warnRows(innerW, maxLines, "episode not recorded yet",
			"waiting for the first verifier run")
	case !tv.loaded:
		return []string{padCell(styleReason.Render("loading…"), innerW)}
	}
	return m.telemetrySummaryRows(tv.summary, innerW, maxLines)
}

// telemetrySummaryRows renders the executive summary block followed by the
// per-verifier distance sparklines, clipped to maxLines with a "+N more" note
// when the panel is too short to show every verifier.
func (m Model) telemetrySummaryRows(sum telemetry.Summary, innerW, maxLines int) []string {
	var rows []string

	goal := sum.GoalText
	if goal == "" {
		goal = "(unset)"
	}
	rows = append(rows, padCell(styleGoalLbl.Render("goal: ")+truncate(goal, innerW-6), innerW))
	rows = append(rows, kvRow(innerW, "up", uptimeText(sum.StartedAt)+"  "+startedClock(sum.StartedAt)))
	rows = append(rows, kvRow(innerW, "work",
		fmt.Sprintf("%d edits · %d batches · %d runs", sum.EditCount, sum.BatchCount, sum.RunCount)))
	rows = append(rows, kvRow(innerW, "beats",
		fmt.Sprintf("%d  ", sum.HeartbeatCount)+overallDistanceText(sum.LastOverallDistance)))
	if cost := formatCost(sum.TotalCostUSD, sum.TotalTokens); cost != "" {
		rows = append(rows, kvRow(innerW, "cost", cost))
	}

	rows = append(rows, padCell(styleAxis.Render(strings.Repeat("─", innerW)), innerW))
	rows = append(rows, padCell(styleHeaderLabel.Render("distance × runs"), innerW))

	if len(sum.Verifiers) == 0 {
		rows = append(rows, padCell(styleReason.Render("(no scored runs yet)"), innerW))
		return clipRows(rows, innerW, maxLines)
	}

	nameW := verifierNameWidth(sum.Verifiers, innerW)
	const valueW = 4 // "0.00"
	sparkW := innerW - nameW - valueW - 2
	if sparkW < 1 {
		sparkW = 0
	}
	for _, v := range sum.Verifiers {
		rows = append(rows, verifierSparkRow(v, innerW, nameW, sparkW, valueW))
	}
	return clipRows(rows, innerW, maxLines)
}

// verifierSparkRow renders one verifier line: name, its distance sparkline
// (most-recent sparkW runs), and the latest distance coloured by closeness.
func verifierSparkRow(v telemetry.VerifierSeries, innerW, nameW, sparkW, valueW int) string {
	name := padCell(styleVerifierLabel.Render(truncate(v.Name, nameW)), nameW)
	value := orbStyle(v.Last).Render(fmt.Sprintf("%.2f", v.Last))

	var middle string
	if sparkW > 0 {
		middle = distanceSparkline(v.Points, sparkW)
	}
	left := padCell(name+" "+middle, innerW-valueW-1)
	row := left + " " + value
	return padCell(row, innerW) + ansi.ResetStyle
}

// distanceSparkline renders the trailing `cells` points as block glyphs, each
// coloured by its own distance (green near goal → red far). Distances are
// clamped to [0,1] — the same normalised domain the compass orbs use.
func distanceSparkline(points []telemetry.DistancePoint, cells int) string {
	if cells <= 0 || len(points) == 0 {
		return ""
	}
	if len(points) > cells {
		points = points[len(points)-cells:]
	}
	var b strings.Builder
	for _, p := range points {
		d := p.Distance
		if d < 0 {
			d = 0
		}
		if d > 1 {
			d = 1
		}
		idx := int(d*float64(len(sparkLevels)-1) + 0.5)
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkLevels) {
			idx = len(sparkLevels) - 1
		}
		b.WriteString(orbStyle(d).Render(string(sparkLevels[idx])))
	}
	return b.String()
}

// verifierNameWidth picks a name column wide enough for the longest verifier
// name, capped so the sparkline keeps room on a narrow panel.
func verifierNameWidth(vs []telemetry.VerifierSeries, innerW int) int {
	longest := 0
	for _, v := range vs {
		if w := lipgloss.Width(v.Name); w > longest {
			longest = w
		}
	}
	maxName := innerW / 2
	if maxName > 12 {
		maxName = 12
	}
	if longest > maxName {
		longest = maxName
	}
	if longest < 4 {
		longest = 4
	}
	return longest
}

// kvRow renders a "label value" row with the label dimmed, padded to innerW.
func kvRow(innerW int, label, value string) string {
	return padCell(styleHeaderLabel.Render(label+" ")+truncate(value, innerW-lipgloss.Width(label)-1), innerW)
}

// warnRows renders a highlighted heading plus dim explanatory lines, used to
// flag missing or unavailable telemetry rather than showing a blank panel.
func warnRows(innerW, maxLines int, heading string, notes ...string) []string {
	rows := []string{padCell(styleStaleBadge.Render(heading), innerW)}
	for _, n := range notes {
		rows = append(rows, wrapReasonRows(n, innerW)...)
	}
	return clipRows(rows, innerW, maxLines)
}

// wrapReasonRows hard-wraps dim text to innerW so a long explanation doesn't
// overflow the panel border.
func wrapReasonRows(text string, innerW int) []string {
	if innerW < 1 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(ansi.Hardwrap(text, innerW, true), "\n") {
		out = append(out, padCell(styleReason.Render(line), innerW))
	}
	return out
}

// clipRows bounds rows to maxLines, replacing the last visible row with a dim
// truncation marker when content was cut so nothing silently disappears.
func clipRows(rows []string, innerW, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(rows) <= maxLines {
		return rows
	}
	rows = rows[:maxLines]
	rows[maxLines-1] = padCell(styleReason.Render("… more (resize to see all)"), innerW)
	return rows
}

func uptimeText(started time.Time) string {
	if started.IsZero() {
		return "—"
	}
	d := time.Since(started)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func startedClock(started time.Time) string {
	if started.IsZero() {
		return ""
	}
	return styleReason.Render("(" + started.Local().Format("15:04:05") + ")")
}

// overallDistanceText renders the latest heartbeat's overall distance as
// "Ø 0.25", coloured by closeness, or "Ø —" when no heartbeat has landed yet.
func overallDistanceText(d sql.NullFloat64) string {
	label := styleHeaderLabel.Render("Ø ")
	if !d.Valid {
		return label + styleReason.Render("—")
	}
	return label + orbStyle(d.Float64).Render(fmt.Sprintf("%.2f", d.Float64))
}
