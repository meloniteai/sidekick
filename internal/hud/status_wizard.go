package hud

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/ipc"
)

// StatusWizard renders the selected verifier's full last-known HUD status as
// a focused, full-screen view.
type StatusWizard struct {
	verifier string
	status   ipc.VerifierStatus
	errMsg   string
	width    int
	height   int
}

func NewStatusWizard(status ipc.VerifierStatus) StatusWizard {
	return StatusWizard{verifier: status.Name, status: status}
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
	width := w.width
	if width < 60 {
		width = 60
	}
	contentW := width - styleEditBox.GetHorizontalFrameSize() - styleEditBox.GetHorizontalPadding()
	if contentW < 20 {
		contentW = 20
	}

	var b strings.Builder
	b.WriteString(styleEditTitle.Render("HUD verifier status"))
	b.WriteString("\n")
	b.WriteString(styleEditHelp.Render("enter/esc return"))
	b.WriteString("\n\n")
	b.WriteString(w.renderStatus(contentW))
	if w.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(stylePickerError.Render(w.errMsg))
	}
	return styleEditBox.Width(width - styleEditBox.GetHorizontalFrameSize()).Render(b.String())
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
	rows = append(rows, "reason:")
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
	return prefix + truncate(value, width-lipgloss.Width(key)-2)
}

func formatFullTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}

func wrapStatusText(s string, width int) []string {
	if s == "" {
		return []string{styleReason.Render("(empty)")}
	}
	var rows []string
	for _, line := range strings.Split(s, "\n") {
		for lipgloss.Width(line) > width {
			head, tail := splitVisual(line, width)
			rows = append(rows, styleReason.Render(head))
			line = tail
		}
		rows = append(rows, styleReason.Render(line))
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
