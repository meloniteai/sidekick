package hud

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// flarePeriod is how many ticks a full hue sweep takes. At tickInterval=133ms
// this is roughly 4 seconds — slow enough not to seizure, fast enough to feel
// alive when the user is staring at the compass.
const flarePeriod = 30

// hueAt returns an RGB hex string by walking the HSL colour wheel. The sat/lit
// pair is dialed for a vivid-but-readable terminal palette: high enough to pop
// against the dim grid, not so high that the orbs lose their distance-based
// reds and greens.
func hueAt(hue float64) string {
	c := colorful.Hsl(math.Mod(math.Mod(hue, 360)+360, 360), 0.82, 0.62)
	return c.Hex()
}

// flareBrand renders the HUD wordmark with a per-character hue sweep that
// advances with the model tick. Each char gets its own hue offset so the
// gradient appears to scroll horizontally across the text.
func flareBrand(tick int, text string) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	step := 360.0 / flarePeriod
	var b strings.Builder
	for i, r := range runes {
		hue := step*float64(tick) + float64(i)*22
		style := lipgloss.NewStyle().
			Foreground(lipgloss.Color(hueAt(hue))).
			Bold(true)
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}

// pulseStyle returns a style whose foreground breathes between two colours
// over flarePeriod ticks. Used for the centre goal glyph so it visibly
// "lives" even on an otherwise static frame.
func pulseStyle(tick int) lipgloss.Style {
	phase := float64(tick%flarePeriod) / flarePeriod
	// triangle wave 0→1→0 over the period — feels more like a heartbeat than
	// a sine because the brightest moment is briefer.
	t := 1 - math.Abs(2*phase-1)
	cold, _ := colorful.Hex("#4ad6ff")
	warm, _ := colorful.Hex("#ff7ff0")
	mixed := cold.BlendLab(warm, t).Clamped()
	return lipgloss.NewStyle().Foreground(lipgloss.Color(mixed.Hex())).Bold(true)
}

// goalGlyphAt returns the glyph to draw at the centre reticle. The glyph
// cycles through a few "iris" shapes so the bullseye visibly rotates — a
// strong, low-cost cue that the daemon is alive and ticking.
func goalGlyphAt(tick int) string {
	frames := []string{"◎", "◉", "◎", "○"}
	return frames[((tick/2)%len(frames)+len(frames))%len(frames)]
}

// sparkleGlyph returns one of a small set of sparkle runes, cycling with
// tick so a "converged" orb appears to twinkle.
func sparkleGlyph(tick int) rune {
	frames := []rune{'✦', '✧', '✶', '✷'}
	return frames[((tick)%len(frames)+len(frames))%len(frames)]
}

// orbStyleFlare extends orbStyle with a tick-driven shimmer for orbs that
// are very close to the goal. The base distance colour still wins overall
// hue (you can tell green from red); we just oscillate the lightness so the
// orb visibly throbs as it converges.
func orbStyleFlare(d float64, tick int) lipgloss.Style {
	switch {
	case d <= 0.12:
		// shimmering bright green→white
		phase := float64(tick%flarePeriod) / flarePeriod
		t := 0.5 + 0.5*math.Sin(2*math.Pi*phase)
		base, _ := colorful.Hex("#5fff5f")
		hot, _ := colorful.Hex("#ffffff")
		mix := base.BlendLab(hot, t).Clamped()
		return lipgloss.NewStyle().Foreground(lipgloss.Color(mix.Hex())).Bold(true)
	case d <= 0.25:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	case d <= 0.50:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	case d <= 0.75:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	}
}

// brailleSpinner returns the next frame of a smooth braille spinner.
// 10 frames is just slow enough at 133ms/tick to look intentional.
var brailleFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func brailleSpinner(tick int) rune {
	return brailleFrames[((tick)%len(brailleFrames)+len(brailleFrames))%len(brailleFrames)]
}

// runningGlow returns the colour used to draw the running spinner. Cycles
// through a magenta→cyan band so multiple verifiers running at once read as
// "the system is working" without looking like errors.
func runningGlow(tick int) lipgloss.Style {
	hue := 200 + 100*math.Sin(2*math.Pi*float64(tick%flarePeriod)/flarePeriod)
	c := colorful.Hsl(hue, 0.75, 0.68)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Bold(true)
}

// truecolorHex formats r,g,b ints into a #RRGGBB string. Kept as a helper
// for tests / future callers that prefer integer triplets to HSL.
func truecolorHex(r, g, b int) string {
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return v
	}
	return fmt.Sprintf("#%02x%02x%02x", clamp(r), clamp(g), clamp(b))
}
