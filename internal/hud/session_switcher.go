package hud

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/ipc"
)

// SessionSwitcher is the ctrl+w modal for explicit TUI session selection.
type SessionSwitcher struct {
	sessions      []ipc.SessionSummary
	cursor        int
	width, height int
	chosen        string
}

func NewSessionSwitcher(sessions []ipc.SessionSummary) SessionSwitcher {
	s := SessionSwitcher{sessions: append([]ipc.SessionSummary(nil), sessions...)}
	for i, row := range s.sessions {
		if row.Displayed {
			s.cursor = i
			break
		}
	}
	return s
}

func (s SessionSwitcher) Chosen() string { return s.chosen }

func (s SessionSwitcher) Update(msg tea.Msg) (SessionSwitcher, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil, false
	}
	switch key.String() {
	case "esc", "ctrl+c":
		return s, nil, true
	case "enter":
		if len(s.sessions) == 0 {
			return s, nil, true
		}
		if s.cursor < 0 || s.cursor >= len(s.sessions) {
			s.cursor = 0
		}
		s.chosen = s.sessions[s.cursor].Worktree
		return s, nil, true
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j", "tab":
		if s.cursor < len(s.sessions)-1 {
			s.cursor++
		}
	}
	return s, nil, false
}

func (s SessionSwitcher) View() string {
	innerW := paletteInnerWidth(s.width)
	var b strings.Builder
	b.WriteString(stylePaletteTitle.Render("Sessions "))
	b.WriteString(stylePaletteSlash.Render(strings.Repeat("/", max(innerW-len("Sessions "), 0))))
	b.WriteString("\n\n")
	if len(s.sessions) == 0 {
		b.WriteString(stylePalettePlaceholder.Render("(no sessions)"))
		b.WriteString("\n")
	} else {
		for i, row := range s.sessions {
			b.WriteString(renderSessionRow(row, innerW, i == s.cursor))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(stylePaletteHelp.Render("↑/↓ choose · enter switch · esc cancel"))

	box := stylePaletteBorder.Width(innerW + stylePaletteBorder.GetHorizontalPadding()).Render(reanchorBrandBg(b.String()))
	if s.width == 0 || s.height == 0 {
		return box
	}
	return lipgloss.Place(s.width, s.height, lipgloss.Center, lipgloss.Center, box)
}

func renderSessionRow(row ipc.SessionSummary, width int, selected bool) string {
	label := row.Label
	if label == "" {
		label = row.Worktree
	}
	if label == "" {
		label = "default"
	}
	state := "idle"
	if row.AnyRunning {
		state = "running"
	}
	prefix := "  "
	if row.Displayed {
		prefix = "• "
	}
	goal := strings.TrimSpace(row.Goal)
	if goal == "" {
		goal = "(no goal)"
	}
	line := fmt.Sprintf("%s%s  %s  %s  %s", prefix, label, state, since(row.LastActivity), goal)
	line = truncate(line, width)
	if selected {
		return stylePaletteSelected.Width(width).Render(line)
	}
	return padCell(styleReason.Render(line), width)
}

func since(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Second)
	if d < time.Second {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
