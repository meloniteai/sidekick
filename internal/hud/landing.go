package hud

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/uriahlevy/hud/internal/verifier"
)

// hudBanner is the ANSI-Shadow block-letter wordmark drawn at the top of the
// landing screen. Hand-drawn so we don't drag a figlet dependency in for one
// six-line glyph string. The leading space lines it up with the inner padding.
const hudBanner = ` ██╗  ██╗██╗   ██╗██████╗
 ██║  ██║██║   ██║██╔══██╗
 ███████║██║   ██║██║  ██║
 ██╔══██║██║   ██║██║  ██║
 ██║  ██║╚██████╔╝██████╔╝
 ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ `

// Landing colors echo the command palette (violet 99 chrome, magenta 141
// accent, dim 240/245 secondary text) so the two screens read as one family.
// Selection is the same solid violet bar the palette uses, and the OK/OFF
// bullets borrow green/grey from the existing status badges.
var (
	styleLandingBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("99")).Padding(1, 2)
	styleLandingBanner    = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	styleLandingVersion   = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	styleLandingLabel     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleLandingValue     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleLandingSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleLandingTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	styleLandingBulletOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("84"))
	styleLandingBulletOff = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleLandingDirection = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleLandingSelected  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("99")).Bold(true)
	styleLandingHelp      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleLandingError     = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

// Landing is the start-of-session screen: HUD wordmark + version pill, the
// session metadata (working dir, socket path), and a verifier multi-select.
// Confirming with enter quits the tea program with Confirmed()==true and
// Selection() populated; esc/ctrl+c quits with Aborted()==true.
//
// Lives in this package (not cmd/) so the tests can render it directly and
// so the visual styling stays adjacent to the palette it mirrors.
type Landing struct {
	verifiers     []verifier.Verifier
	enabled       []bool
	cursor        int
	width, height int
	version       string
	socketPath    string
	cwd           string
	err           string
	aborted       bool
	confirmed     bool
}

// NewLanding builds a Landing seeded from each verifier's Disabled flag, so
// the picker reflects the persisted hud.yaml state and a hit-enter user gets
// the same set they ran last session. Users can still toggle freely; on
// confirm the resulting toggle state is mirrored back to yaml by the caller.
func NewLanding(verifiers []verifier.Verifier, version, socketPath, cwd string) Landing {
	enabled := make([]bool, len(verifiers))
	for i, v := range verifiers {
		enabled[i] = !v.Disabled
	}
	return Landing{
		verifiers:  verifiers,
		enabled:    enabled,
		version:    version,
		socketPath: socketPath,
		cwd:        cwd,
	}
}

// Init satisfies tea.Model.
func (l Landing) Init() tea.Cmd { return nil }

// Update advances the landing screen state.
func (l Landing) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		l.width = m.Width
		l.height = m.Height
	case tea.KeyMsg:
		switch m.String() {
		case "esc", "ctrl+c":
			l.aborted = true
			return l, tea.Quit
		case "up", "k":
			if l.cursor > 0 {
				l.cursor--
			}
		case "down", "j":
			if l.cursor < len(l.verifiers)-1 {
				l.cursor++
			}
		case " ", "x":
			if l.cursor >= 0 && l.cursor < len(l.enabled) {
				l.enabled[l.cursor] = !l.enabled[l.cursor]
				l.err = ""
			}
		case "a":
			for i := range l.enabled {
				l.enabled[i] = true
			}
			l.err = ""
		case "enter":
			if l.selectedCount() < MinSelected {
				l.err = fmt.Sprintf("select at least %d verifier (currently %d)", MinSelected, l.selectedCount())
				return l, nil
			}
			l.confirmed = true
			return l, tea.Quit
		}
	}
	return l, nil
}

// Verifiers returns the full input set in input order, with each entry's
// Disabled flag rewritten to reflect the landing toggle state. Disabled rows
// are kept (not filtered) so the runner can still surface them in the HUD
// footer where the user can re-enable them mid-session without restarting.
// Empty when the user aborted.
func (l Landing) Verifiers() []verifier.Verifier {
	if l.aborted {
		return nil
	}
	out := make([]verifier.Verifier, 0, len(l.verifiers))
	for i, v := range l.verifiers {
		v.Disabled = !(i < len(l.enabled) && l.enabled[i])
		out = append(out, v)
	}
	return out
}

// EnabledCount reports how many landing rows are toggled on. Used by the
// caller to enforce the MinSelected floor without iterating Verifiers().
func (l Landing) EnabledCount() int { return l.selectedCount() }

// Aborted reports whether the user dismissed the landing screen without
// confirming a selection.
func (l Landing) Aborted() bool { return l.aborted }

// Confirmed reports whether the user pressed enter on a valid selection.
func (l Landing) Confirmed() bool { return l.confirmed }

func (l Landing) selectedCount() int {
	n := 0
	for _, b := range l.enabled {
		if b {
			n++
		}
	}
	return n
}

// View renders the landing screen sized to fit the host terminal. When the
// window size is unknown (tests, headless render) the modal is returned
// un-centered so callers can dimension it themselves.
func (l Landing) View() string {
	innerW := landingInnerWidth(l.width)

	var b strings.Builder

	b.WriteString(renderLandingBanner(innerW, l.version))
	b.WriteString("\n\n")

	if l.cwd != "" {
		b.WriteString(styleLandingValue.Render(prettyPath(l.cwd)))
		b.WriteString("\n\n")
	}

	if l.socketPath != "" {
		b.WriteString(styleLandingLabel.Render("Socket  "))
		b.WriteString(styleLandingValue.Render(prettyPath(l.socketPath)))
		b.WriteString("\n\n")
	}

	b.WriteString(styleLandingSeparator.Render(strings.Repeat("─", innerW)))
	b.WriteString("\n\n")

	b.WriteString(styleLandingTitle.Render("Verifiers"))
	b.WriteString("\n\n")

	nameWidth := 0
	for _, v := range l.verifiers {
		if n := lipgloss.Width(v.Name); n > nameWidth {
			nameWidth = n
		}
	}

	for i, v := range l.verifiers {
		b.WriteString(renderLandingVerifierRow(v, l.enabled[i], i == l.cursor, nameWidth, innerW))
		b.WriteString("\n")
	}

	if l.err != "" {
		b.WriteString("\n")
		b.WriteString(styleLandingError.Render(l.err))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleLandingHelp.Render("↑/↓ navigate · space toggle · a select all · enter start · esc abort"))

	box := styleLandingBorder.Width(innerW + styleLandingBorder.GetHorizontalPadding()).Render(b.String())
	if l.width == 0 || l.height == 0 {
		return box
	}
	return lipgloss.Place(l.width, l.height, lipgloss.Center, lipgloss.Center, box)
}

// renderLandingBanner draws the wordmark with the version pill anchored on the
// first line's trailing edge — same visual idea as the Crush reference, just
// without the slash decoration the user explicitly ruled out.
func renderLandingBanner(innerW int, version string) string {
	lines := strings.Split(hudBanner, "\n")
	pill := ""
	if version != "" {
		pill = "v" + version
	}
	var b strings.Builder
	for i, ln := range lines {
		styled := styleLandingBanner.Render(ln)
		if i == 0 && pill != "" {
			pad := max(innerW-lipgloss.Width(ln)-lipgloss.Width(pill), 1)
			styled += strings.Repeat(" ", pad) + styleLandingVersion.Render(pill)
		}
		b.WriteString(styled)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderLandingVerifierRow draws one row of the verifier list. The full row
// is painted on a violet bar when it's the cursor target (mirrors the
// palette's selection style); otherwise the bullet picks up the on/off
// colour and the direction sits in a dim accent.
func renderLandingVerifierRow(v verifier.Verifier, enabled, selected bool, nameWidth, innerW int) string {
	bullet := "○"
	if enabled {
		bullet = "●"
	}
	name := fmt.Sprintf("%-*s", nameWidth, v.Name)
	plain := fmt.Sprintf(" %s  %s  %s", bullet, name, v.Direction)

	if selected {
		pad := max(innerW-lipgloss.Width(plain), 0)
		return styleLandingSelected.Render(plain + strings.Repeat(" ", pad))
	}

	bulletStyle := styleLandingBulletOff
	if enabled {
		bulletStyle = styleLandingBulletOn
	}
	return " " + bulletStyle.Render(bullet) + "  " + styleLandingValue.Render(name) + "  " + styleLandingDirection.Render(v.Direction)
}

// landingInnerWidth picks the content width for the landing modal. Sized
// generously (up to 100 cells) so the socket path and banner don't wrap on
// typical terminals; floors at 48 to keep the layout legible in narrow ones.
func landingInnerWidth(termWidth int) int {
	target := min(max(termWidth*8/10, 56), 100)
	chrome := styleLandingBorder.GetHorizontalFrameSize()
	return max(target-chrome, 48)
}

// prettyPath collapses $HOME to "~" for compactness in user-facing paths.
// Falls back to the original path on any error.
func prettyPath(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
