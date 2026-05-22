package sidekick

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// brailleSpinner returns the next frame of a smooth braille spinner.
// 10 frames is just slow enough at 133ms/tick to look intentional.
var brailleFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func brailleSpinner(tick int) rune {
	return brailleFrames[((tick)%len(brailleFrames)+len(brailleFrames))%len(brailleFrames)]
}

// runningGlow returns the colour used to draw the running spinner.
func runningGlow(tick int) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(brandBgColor).Bold(true)
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
