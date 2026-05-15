package hud

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// paletteAction is the discriminator the Model uses to dispatch a chosen
// palette item to the right existing handler. Returned by Palette.Chosen()
// once Update reports done=true.
type paletteAction int

const (
	paletteActionNone paletteAction = iota
	paletteActionNewVerifier
	paletteActionEditVerifier
	paletteActionToggleGitPanel
	paletteActionToggleEventLog
)

type paletteItem struct {
	label    string
	shortcut string
	action   paletteAction
}

var paletteItems = []paletteItem{
	{label: "New Verifier", shortcut: "ctrl+n", action: paletteActionNewVerifier},
	{label: "Edit Verifier", shortcut: "ctrl+e", action: paletteActionEditVerifier},
	{label: "Toggle Git Changes", shortcut: "ctrl+g", action: paletteActionToggleGitPanel},
	{label: "Toggle Event Log", shortcut: "ctrl+l", action: paletteActionToggleEventLog},
}

// Palette colors share the KIKAITE coral accent family with the landing
// screen so the command palette and the splash read as one product. Chrome
// (border, slashes, selection bar) uses the saturated coral; titles and
// prompts use the softer coral; secondary text stays dim grey. Every inner
// style sets Background to brandBg so the embedded SGR resets don't punch
// through to terminal black inside the framed modal — see brandBgColor in
// view.go for the long-form note on why this is required.
var (
	stylePaletteBorder      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brandCoral)).BorderBackground(lipgloss.Color(brandBg)).Background(lipgloss.Color(brandBg)).Padding(1, 2)
	stylePaletteTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(brandCoralSoft)).Background(lipgloss.Color(brandBg))
	stylePaletteSlash       = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoral)).Background(lipgloss.Color(brandBg))
	stylePalettePrompt      = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoralSoft)).Background(lipgloss.Color(brandBg))
	stylePalettePlaceholder = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(brandBg))
	stylePaletteShortcut    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(brandBg))
	// stylePaletteSelected keeps its coral bg — that bar is the cursor.
	stylePaletteSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color(brandCoral)).Bold(true)
	stylePaletteHelp      = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(brandBg))
	stylePaletteSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(brandBg))
)

// Palette is the ctrl+p command palette: a centered overlay listing the four
// migrated commands (new verifier, edit verifier, toggle git panel, toggle
// event log) with arrow-key navigation, enter to dispatch, and substring
// filtering driven by a bubbles/textinput.
type Palette struct {
	filter        textinput.Model
	cursor        int
	width, height int
	chosen        paletteAction
}

// NewPalette returns an initialized palette ready to be embedded in the Model.
// The bubbles textinput begins focused so the user can start typing
// immediately — there's no other input target inside the modal.
func NewPalette() Palette {
	ti := textinput.New()
	ti.Placeholder = "Type to filter"
	ti.Prompt = ""
	ti.CharLimit = 64
	ti.Width = 64
	ti.Focus()
	ti.PromptStyle = stylePalettePrompt
	ti.PlaceholderStyle = stylePalettePlaceholder
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color(brandBg))
	// Cursor inherits the brand bg too — bubbles/textinput defaults to a
	// reverse-video block that would otherwise show as black-on-grey when
	// the box bg gets reset by the inner cursor render.
	ti.Cursor.Style = lipgloss.NewStyle().Background(lipgloss.Color(brandBg))
	return Palette{filter: ti}
}

// Chosen reports which item the user picked once Update returns done=true.
// Returns paletteActionNone when the palette was dismissed via esc.
func (p Palette) Chosen() paletteAction { return p.chosen }

// SetSize updates the dimensions the palette should center itself within. It
// is safe to call on every WindowSizeMsg; the modal width/height is computed
// at render time.
func (p *Palette) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// Update advances the palette state. done=true means the host Model should
// close the palette; Chosen() will report the selected action (or
// paletteActionNone if the user dismissed it).
func (p Palette) Update(msg tea.Msg) (Palette, tea.Cmd, bool) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil, false
	}
	switch key.String() {
	case "esc", "ctrl+c":
		p.chosen = paletteActionNone
		return p, nil, true
	case "enter":
		items := p.visibleItems()
		if len(items) == 0 {
			return p, nil, false
		}
		if p.cursor < 0 || p.cursor >= len(items) {
			p.cursor = 0
		}
		p.chosen = items[p.cursor].action
		return p, nil, true
	case "up":
		if p.cursor > 0 {
			p.cursor--
		}
		return p, nil, false
	case "down", "tab":
		items := p.visibleItems()
		if p.cursor < len(items)-1 {
			p.cursor++
		}
		return p, nil, false
	}
	// Everything else (printable chars, backspace, arrow-left/right) flows
	// into the filter textinput. Re-clamp the cursor afterwards in case the
	// filter shrank the visible list out from under us.
	prevValue := p.filter.Value()
	next, cmd := p.filter.Update(msg)
	p.filter = next
	if p.filter.Value() != prevValue {
		p.cursor = 0
	}
	items := p.visibleItems()
	if p.cursor >= len(items) {
		if len(items) == 0 {
			p.cursor = 0
		} else {
			p.cursor = len(items) - 1
		}
	}
	return p, cmd, false
}

// visibleItems returns the items that match the current filter (case-
// insensitive substring on label). An empty filter yields the full list.
func (p Palette) visibleItems() []paletteItem {
	q := strings.ToLower(strings.TrimSpace(p.filter.Value()))
	if q == "" {
		return paletteItems
	}
	out := make([]paletteItem, 0, len(paletteItems))
	for _, it := range paletteItems {
		if strings.Contains(strings.ToLower(it.label), q) {
			out = append(out, it)
		}
	}
	return out
}

// View renders the palette as a string sized to fit the host terminal. The
// modal width is clamped between 48 and 80 columns so it stays readable on
// narrow terminals without sprawling on wide ones; the host Model centers
// the result with lipgloss.Place.
func (p Palette) View() string {
	innerW := paletteInnerWidth(p.width)

	var b strings.Builder
	b.WriteString(renderPaletteTitleRow(innerW))
	b.WriteString("\n\n")

	// The textinput.View() output is full of SGR escapes (placeholder colour,
	// cursor styling) and our generic truncate helper is not ANSI-aware
	// mid-escape — running it through truncate eats the placeholder after the
	// first character. The textinput is already width-bounded via ti.Width
	// when constructed, so we just emit the row directly.
	prompt := stylePalettePrompt.Render("> ")
	b.WriteString(prompt + p.filter.View())
	b.WriteString("\n")
	b.WriteString(stylePaletteSeparator.Render(strings.Repeat("─", innerW)))
	b.WriteString("\n")

	items := p.visibleItems()
	if len(items) == 0 {
		b.WriteString(stylePalettePlaceholder.Render("(no matches)"))
		b.WriteString("\n")
	} else {
		for i, it := range items {
			selected := i == p.cursor
			b.WriteString(renderPaletteRow(it, innerW, selected))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(stylePaletteHelp.Render("↑/↓ choose · enter confirm · esc cancel"))

	box := stylePaletteBorder.Width(innerW + stylePaletteBorder.GetHorizontalPadding()).Render(reanchorBrandBg(b.String()))
	if p.width == 0 || p.height == 0 {
		return box
	}
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

// paletteInnerWidth picks the width (in cells, exclusive of border + padding)
// that the modal body should target. It scales with the terminal width but
// stays inside a [40, 72] range so the slash banner reads well and the
// shortcuts on the right stay readable on small terminals.
func paletteInnerWidth(termWidth int) int {
	target := min(max(termWidth*7/10, 40), 72)
	chrome := stylePaletteBorder.GetHorizontalFrameSize()
	return max(target-chrome, 24)
}

// renderPaletteTitleRow draws "Commands //////////…" filling the inner width.
// The slashes are dimmer than the title word to mirror the Crush palette
// reference where the title pops and the decoration recedes.
func renderPaletteTitleRow(innerW int) string {
	title := "Commands "
	slashCount := max(innerW-lipgloss.Width(title), 0)
	return stylePaletteTitle.Render(title) + stylePaletteSlash.Render(strings.Repeat("/", slashCount))
}

// renderPaletteRow renders one menu item: label on the left, shortcut on the
// right, separated by enough spaces to push the shortcut to the right edge.
// When selected the whole row is rendered on a solid violet bar.
func renderPaletteRow(it paletteItem, innerW int, selected bool) string {
	labelMax := max(innerW-lipgloss.Width(it.shortcut)-2, 1)
	label := truncate(it.label, labelMax)
	pad := max(innerW-lipgloss.Width(label)-lipgloss.Width(it.shortcut), 1)
	if selected {
		row := label + strings.Repeat(" ", pad) + it.shortcut
		return stylePaletteSelected.Render(row)
	}
	return label + strings.Repeat(" ", pad) + stylePaletteShortcut.Render(it.shortcut)
}
