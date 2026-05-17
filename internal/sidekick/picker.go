package sidekick

import "github.com/charmbracelet/lipgloss"

// MinSelected is the minimum number of verifiers a user must enable before
// the Sidekick can start. One is enough — the compass renders any subset.
const MinSelected = 1

// stylePickerError renders inline error text in red. Used by the in-TUI
// editor and status wizards even though the picker itself is now a huh form.
var stylePickerError = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
