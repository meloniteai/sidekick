package hud

// Exported aliases for the brand color tokens that drive the in-HUD
// command palette. They are re-exported so palette-styled surfaces that
// live outside this package (e.g. the standalone `hud verifier add
// --local` wizard) can build their own lipgloss styles against the same
// values without re-inventing the palette.
const (
	BrandCoral     = brandCoral
	BrandCoralSoft = brandCoralSoft
	BrandBg        = brandBg
)

// PaletteInnerWidth returns the inner content width (in cells, exclusive
// of border + padding) for a palette-styled modal sized to the given
// terminal width. Re-exposed so external modals match the palette's
// sizing exactly.
func PaletteInnerWidth(termWidth int) int { return paletteInnerWidth(termWidth) }

// ReanchorBrandBg re-inserts the brand background SGR after every
// embedded `\x1b[0m` reset in s so inter-cell gaps inside a framed modal
// don't punch through to terminal black. Apply to the *inner* content
// before passing it to the box border's Render — see view.go for the
// long-form note on why this is needed.
func ReanchorBrandBg(s string) string { return reanchorBrandBg(s) }
