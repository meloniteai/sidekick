package hud

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleHeader  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleGrid    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	styleGoal    = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleAxis    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleReason  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleGoalLbl = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

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
	gridW := m.width - 4
	gridH := m.height - 6 - 2 - len(m.snapshot.Verifiers) // header + list
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
	b.WriteString(styleHeader.Render("HUD"))
	b.WriteString("  ")
	b.WriteString(styleGoalLbl.Render("goal: "))
	if g := m.snapshot.Goal; g != "" {
		b.WriteString(g)
	} else {
		b.WriteString(styleReason.Render("(none — submit a prompt or run `hud goal ...`)"))
	}
	b.WriteString("\n\n")

	b.WriteString(styleGrid.Render(m.renderGrid(gridW, gridH)))
	b.WriteString("\n\n")
	b.WriteString(m.renderList())
	b.WriteString("\n")
	b.WriteString(styleReason.Render("press q to quit"))
	return b.String()
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
		head := fmt.Sprintf("[%s] %-12s %s  d=%.2f", label, v.Name, v.Direction, v.Distance)
		if v.Running {
			head += "  " + styleRunning.Render("(running…)")
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
