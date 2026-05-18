package sidekick

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/meloniteai/sidekick/internal/verifier"
)

// sidekickBanner is the ANSI-Shadow block-letter wordmark drawn at the top of the
// landing screen. Hand-drawn so we don't drag a figlet dependency in for one
// six-line glyph string. The leading space lines it up with the inner padding.
const sidekickBanner = ` ███████╗██╗██████╗ ███████╗██╗  ██╗██╗ ██████╗██╗  ██╗
 ██╔════╝██║██╔══██╗██╔════╝██║ ██╔╝██║██╔════╝██║ ██╔╝
 ███████╗██║██║  ██║█████╗  █████╔╝ ██║██║     █████╔╝
 ╚════██║██║██║  ██║██╔══╝  ██╔═██╗ ██║██║     ██╔═██╗
 ███████║██║██████╔╝███████╗██║  ██╗██║╚██████╗██║  ██╗
 ╚══════╝╚═╝╚═════╝ ╚══════╝╚═╝  ╚═╝╚═╝ ╚═════╝╚═╝  ╚═╝`

// brandCoral / brandCoralSoft form the Melonite accent family: the saturated
// coral from melonite.ai for chrome (border, selection bar, banner) and a
// slightly lighter shade for secondary accents (titles, version pill) so
// stacked elements still separate at a glance. brandBg is the warm graphite
// that fills the inside of every framed surface — picked to read as a
// deliberate "off-black" against the coral chrome on both the splash and the
// main Sidekick without competing with the user's terminal theme.
const (
	brandCoral     = "#E84B30"
	brandCoralSoft = "#FF7A55"
	brandBg        = "#1A1612"
)

// Landing colors are anchored to the Melonite coral palette so the splash
// reads as the same brand the marketing site uses. Dim 240/245 stay for
// secondary text and OK/OFF bullets keep their existing green/grey so
// state cues survive the recolor. Every inner style sets Background to
// brandBg so the embedded SGR resets don't punch through to terminal
// black inside the framed modal — see brandBgColor in view.go for the
// long-form note on why this is required.
var (
	styleLandingBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brandCoral)).BorderBackground(lipgloss.Color(brandBg)).Background(lipgloss.Color(brandBg)).Padding(1, 2)
	styleLandingBanner    = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoral)).Background(lipgloss.Color(brandBg)).Bold(true)
	styleLandingVersion   = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoralSoft)).Background(lipgloss.Color(brandBg))
	styleLandingLabel     = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(brandBg))
	styleLandingValue     = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color(brandBg))
	styleLandingSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(brandBg))
	styleLandingTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color(brandCoralSoft)).Background(lipgloss.Color(brandBg)).Bold(true)
	styleLandingBulletOn  = lipgloss.NewStyle().Foreground(lipgloss.Color("84")).Background(lipgloss.Color(brandBg))
	styleLandingBulletOff = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color(brandBg))
	styleLandingDirection = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(brandBg))
	// styleLandingSelected keeps its coral bg — that bar is the cursor.
	styleLandingSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color(brandCoral)).Bold(true)
	styleLandingHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color(brandBg))
	styleLandingError    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Background(lipgloss.Color(brandBg)).Bold(true)
)

// Landing is the start-of-session screen: Sidekick wordmark + version pill, the
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
// the picker reflects the persisted sidekick.yaml state and a hit-enter user gets
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
// are kept (not filtered) so the runner can still surface them in the Sidekick
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

	box := styleLandingBorder.Width(innerW + styleLandingBorder.GetHorizontalPadding()).Render(reanchorBrandBg(b.String()))
	if l.width == 0 || l.height == 0 {
		return box
	}
	return lipgloss.Place(l.width, l.height, lipgloss.Center, lipgloss.Center, box)
}

// renderLandingBanner draws the wordmark with the company name on the left
// and version pill on the right of a dedicated row above the banner. The
// pill used to sit on the trailing edge of banner line 1, but that made
// line 1 wider than the other 5 banner rows; on narrow terminals the extra
// cells wrapped and the pill spilled onto banner line 2, overlapping the
// wordmark. Keeping the metadata on a dedicated row means the 6 banner rows
// stay uniform width and clip together when the terminal is narrower than
// the wordmark.
func renderLandingBanner(innerW int, version string) string {
	_ = innerW
	lines := strings.Split(sidekickBanner, "\n")
	// Wordmark glyphs carried a single leading space so the banner sat one
	// column inside the box padding; that shoved "SIDEKICK" one cell right of
	// the metadata row above it, so the company name no longer lined up with
	// the "S". Drop the cosmetic space here and align the metadata row to the
	// wordmark's own width so "Melonite™" stacks above the "S" and "v…" stacks
	// above the trailing "╗" of the final "K".
	for i, ln := range lines {
		lines[i] = strings.TrimPrefix(ln, " ")
	}
	bannerW := lipgloss.Width(lines[0])

	var b strings.Builder
	company := "Melonite™"
	pill := ""
	if version != "" {
		pill = "v" + version
	}
	if company != "" || pill != "" {
		gap := max(bannerW-lipgloss.Width(company)-lipgloss.Width(pill), 1)
		b.WriteString(styleLandingVersion.Render(company))
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(styleLandingVersion.Render(pill))
		b.WriteString("\n")
	}
	for i, ln := range lines {
		b.WriteString(styleLandingBanner.Render(ln))
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
// generously so the "Sidekick" banner (~56 cells) doesn't wrap on typical
// terminals; floored at 84 to keep the banner framed comfortably and
// capped at 110 so the modal still feels framed on ultra-wide terminals.
func landingInnerWidth(termWidth int) int {
	target := min(max(termWidth*8/10, 84), 110)
	chrome := styleLandingBorder.GetHorizontalFrameSize()
	return max(target-chrome, 78)
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
