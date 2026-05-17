package sidekick

import (
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// HuhTheme returns the huh theme that matches the rest of the Sidekick compass:
// blue (12) for titles and selectors, green (10) for selected options,
// red (9) for validation errors, and grey (245) for help/description text.
// Built from huh.ThemeBase rather than ThemeCharm so we don't inherit the
// fuchsia/indigo palette.
func HuhTheme() *huh.Theme {
	t := huh.ThemeBase()

	var (
		blue  = lipgloss.Color("12")
		green = lipgloss.Color("10")
		red   = lipgloss.Color("9")
		help  = lipgloss.Color("245")
		body  = lipgloss.Color("252")
	)

	t.Focused.Base = t.Focused.Base.BorderForeground(blue)
	t.Focused.Card = t.Focused.Base
	t.Focused.Title = t.Focused.Title.Foreground(blue).Bold(true)
	t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(blue).Bold(true).MarginBottom(1)
	t.Focused.Description = t.Focused.Description.Foreground(help)
	t.Focused.Directory = t.Focused.Directory.Foreground(blue)

	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(red)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(red)

	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(blue)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(blue)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(blue)
	t.Focused.Option = t.Focused.Option.Foreground(body)

	t.Focused.MultiSelectSelector = t.Focused.MultiSelectSelector.Foreground(blue)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(green)
	t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(green).SetString("[x] ")
	t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(help).SetString("[ ] ")
	t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(body)

	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(lipgloss.Color("0")).Background(blue)
	t.Focused.Next = t.Focused.FocusedButton
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(body).Background(lipgloss.Color("238"))

	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(blue)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(help)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(blue)

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")

	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}
