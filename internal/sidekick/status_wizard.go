package sidekick

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/meloniteai/sidekick/internal/ipc"
)

var (
	statusValueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(brandBgColor)
	statusReasonStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(brandBgColor)
	statusErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(brandBgColor).Bold(true)
)

// StatusWizard renders the selected verifier's full last-known Sidekick status in
// the same centered modal chrome as the palette, session switcher, and git
// changes panel.
type StatusWizard struct {
	verifier string
	status   ipc.VerifierStatus
	global   bool
	errMsg   string
	notice   string
	width    int
	height   int
}

func NewStatusWizard(status ipc.VerifierStatus) StatusWizard {
	return StatusWizard{verifier: status.Name, status: status}
}

func (w StatusWizard) WithConfigPath(path string) StatusWizard {
	w.global = isGlobalConfig(path)
	return w
}

func (w StatusWizard) Update(msg tea.Msg) (StatusWizard, tea.Cmd, bool) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		w.width = size.Width
		w.height = size.Height
		return w, nil, false
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return w, nil, false
	}
	switch key.String() {
	case "esc", "q", "enter", "ctrl+c":
		return w, nil, true
	default:
		return w, nil, false
	}
}

func (w StatusWizard) View() string {
	innerW := paletteInnerWidth(w.width)
	body := w.renderBody(innerW)
	box := stylePaletteBorder.Width(innerW + stylePaletteBorder.GetHorizontalPadding()).Render(reanchorBrandBg(body))
	if w.width == 0 || w.height == 0 {
		return box
	}
	return lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, box)
}

func (w StatusWizard) renderBody(innerW int) string {
	var b strings.Builder
	b.WriteString(renderStatusTitleRow(innerW))
	b.WriteString("\n\n")
	b.WriteString(w.renderStatus(innerW))
	if w.notice != "" {
		b.WriteString("\n")
		b.WriteString(statusReasonStyle.Render(w.notice))
	}
	if w.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(statusErrorStyle.Render(w.errMsg))
	}
	b.WriteString("\n\n")
	b.WriteString(stylePaletteHelp.Render(w.helpText()))
	return b.String()
}

func (w StatusWizard) helpText() string {
	if w.global {
		return "p copy to project · enter close · esc close"
	}
	return "g copy to global · enter close · esc close"
}

func renderStatusTitleRow(innerW int) string {
	title := "Verifier status "
	return stylePaletteTitle.Render(title) + stylePaletteSlash.Render(strings.Repeat("/", max(innerW-lipgloss.Width(title), 0)))
}

func (w StatusWizard) renderStatus(width int) string {
	v := w.status
	status := fmt.Sprintf("d=%.2f", v.Distance)
	if v.Disabled {
		status = "off"
	} else if v.Running {
		status = "running"
	}

	rows := []string{
		kvLine("name", v.Name, width),
		kvLine("direction", v.Direction, width),
		kvLine("distance", fmt.Sprintf("%.2f", v.Distance), width),
		kvLine("status", status, width),
		kvLine("computed", formatFullTime(v.ComputedAt), width),
		kvLine("type", verifierType(v), width),
	}
	rows = append(rows, configLines(v.Config, width)...)
	rows = append(rows, styleHeaderLabel.Render("reason:"))
	rows = append(rows, wrapStatusText(v.Reason, width)...)
	return strings.Join(rows, "\n")
}

func configLines(cfg ipc.VerifierConfig, width int) []string {
	var rows []string
	if len(cfg.Command) > 0 {
		rows = append(rows, kvLine("command", strings.Join(cfg.Command, " "), width))
	}
	if cfg.Agent != "" {
		rows = append(rows, kvLine("agent", cfg.Agent, width))
	}
	if cfg.Model != "" {
		rows = append(rows, kvLine("model", cfg.Model, width))
	}
	if cfg.Thinking != "" {
		rows = append(rows, kvLine("thinking", cfg.Thinking, width))
	}
	if cfg.Skill != "" {
		rows = append(rows, kvLine("skill", cfg.Skill, width))
	}
	if cfg.Timeout != "" {
		rows = append(rows, kvLine("timeout", cfg.Timeout, width))
	}
	if cfg.PassReason != "" {
		rows = append(rows, kvLine("pass_reason", cfg.PassReason, width))
	}
	if cfg.FailReason != "" {
		rows = append(rows, kvLine("fail_reason", cfg.FailReason, width))
	}
	return rows
}

func kvLine(key, value string, width int) string {
	if value == "" {
		value = "(empty)"
	}
	prefix := styleHeaderLabel.Render(key + ": ")
	valueW := max(width-lipgloss.Width(key)-2, 1)
	return prefix + statusValueStyle.Render(truncate(value, valueW))
}

func formatFullTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}

func wrapStatusText(s string, width int) []string {
	if s == "" {
		return []string{statusReasonStyle.Render("(empty)")}
	}
	var rows []string
	for _, line := range strings.Split(s, "\n") {
		for lipgloss.Width(line) > width {
			head, tail := splitVisual(line, width)
			rows = append(rows, statusReasonStyle.Render(head))
			line = tail
		}
		rows = append(rows, statusReasonStyle.Render(line))
	}
	return rows
}

func splitVisual(s string, width int) (string, string) {
	if width <= 0 {
		width = 1
	}
	var b strings.Builder
	used := 0
	for i, r := range s {
		w := lipgloss.Width(string(r))
		if used > 0 && used+w > width {
			return b.String(), s[i:]
		}
		b.WriteRune(r)
		used += w
	}
	return b.String(), ""
}
