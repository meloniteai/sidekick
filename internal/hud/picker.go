package hud

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/verifier"
)

// MinSelected is the minimum number of verifiers a user must enable before
// the HUD can start. Four covers the cardinal compass points.
const MinSelected = 4

var (
	stylePickerHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	stylePickerCursor   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	stylePickerSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stylePickerError    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// PickerModel is a Bubble Tea model that lets the user pick which configured
// verifiers to run. Selections default to all-on so the common case (exactly
// four entries in hud.yaml) is just enter-to-continue.
type PickerModel struct {
	verifiers []verifier.Verifier
	selected  []bool
	cursor    int
	confirmed bool
	aborted   bool
	errMsg    string
}

// NewPicker returns a PickerModel pre-populated with the given verifiers, all
// selected by default.
func NewPicker(verifiers []verifier.Verifier) PickerModel {
	sel := make([]bool, len(verifiers))
	for i := range sel {
		sel[i] = true
	}
	return PickerModel{verifiers: verifiers, selected: sel}
}

// Init satisfies tea.Model.
func (m PickerModel) Init() tea.Cmd { return nil }

// Update satisfies tea.Model.
func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "q", "esc":
		m.aborted = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.verifiers)-1 {
			m.cursor++
		}
	case " ", "x":
		if m.cursor < len(m.selected) {
			m.selected[m.cursor] = !m.selected[m.cursor]
			m.errMsg = ""
		}
	case "a":
		anyOff := false
		for _, s := range m.selected {
			if !s {
				anyOff = true
				break
			}
		}
		for i := range m.selected {
			m.selected[i] = anyOff
		}
		m.errMsg = ""
	case "enter":
		if m.SelectedCount() < MinSelected {
			m.errMsg = fmt.Sprintf("select at least %d verifiers (currently %d)", MinSelected, m.SelectedCount())
			return m, nil
		}
		m.confirmed = true
		return m, tea.Quit
	}
	return m, nil
}

// View satisfies tea.Model.
func (m PickerModel) View() string {
	var b strings.Builder
	b.WriteString(styleHeader.Render("HUD — choose verifiers"))
	b.WriteString("\n")
	b.WriteString(stylePickerHelp.Render(
		fmt.Sprintf("pick at least %d. ↑/↓ move · space toggle · a toggle-all · enter start · q quit", MinSelected),
	))
	b.WriteString("\n\n")

	for i, v := range m.verifiers {
		cursor := "  "
		if i == m.cursor {
			cursor = stylePickerCursor.Render("> ")
		}
		box := "[ ]"
		if m.selected[i] {
			box = stylePickerSelected.Render("[x]")
		}
		line := fmt.Sprintf("%s%s  %-14s %s", cursor, box, v.Name, styleReason.Render(v.Direction))
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	count := m.SelectedCount()
	summary := fmt.Sprintf("%d/%d selected", count, len(m.verifiers))
	if count < MinSelected {
		summary += fmt.Sprintf("  (need %d)", MinSelected-count)
	}
	b.WriteString(stylePickerHelp.Render(summary))
	if m.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(stylePickerError.Render(m.errMsg))
	}
	b.WriteString("\n")
	return b.String()
}

// SelectedCount reports how many verifiers are currently checked.
func (m PickerModel) SelectedCount() int {
	n := 0
	for _, s := range m.selected {
		if s {
			n++
		}
	}
	return n
}

// Selection returns the verifiers the user enabled, in their original order.
// Only meaningful after the model has Quit; check Confirmed first.
func (m PickerModel) Selection() []verifier.Verifier {
	out := make([]verifier.Verifier, 0, len(m.verifiers))
	for i, v := range m.verifiers {
		if i < len(m.selected) && m.selected[i] {
			out = append(out, v)
		}
	}
	return out
}

// Confirmed reports whether the user accepted their selection (vs. aborted).
func (m PickerModel) Confirmed() bool { return m.confirmed }

// Aborted reports whether the user pressed q/ctrl+c to bail out.
func (m PickerModel) Aborted() bool { return m.aborted }
