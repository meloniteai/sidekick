// Package sidekick renders the daemon's state as a Bubble Tea TUI: a compass-style
// 2D map of verifier distances plus a detail list.
package sidekick

import "math"

// Direction names map to angles measured counter-clockwise from East, in
// radians. North is up on screen (negative y in screen coords).
var directionAngle = map[string]float64{
	"E":  0,
	"NE": math.Pi / 4,
	"N":  math.Pi / 2,
	"NW": 3 * math.Pi / 4,
	"W":  math.Pi,
	"SW": 5 * math.Pi / 4,
	"S":  3 * math.Pi / 2,
	"SE": 7 * math.Pi / 4,
}

// project returns terminal-cell coordinates (col, row) for a verifier at the
// given direction and normalized distance d ∈ [0, 1], on a grid of width w by
// height h with origin at the center. Terminal cells are roughly twice as
// tall as they are wide, so x is scaled by 2 to keep the compass round-ish.
func project(direction string, d float64, w, h int) (col, row int, ok bool) {
	θ, ok := directionAngle[direction]
	if !ok {
		return 0, 0, false
	}
	if d < 0 {
		d = 0
	}
	if d > 1 {
		d = 1
	}
	col, row = projectFromAngle(θ, d, w, h)
	return col, row, true
}

// projectXY returns terminal-cell coordinates for a verifier whose normalized
// position is already expressed in Cartesian coords (x, y) ∈ [-1, 1]². Used
// by the orb spring renderer, which interpolates in this space so that a
// changing direction produces a curved glide rather than a jump.
func projectXY(x, y float64, w, h int) (col, row int, ok bool) {
	cx, cy := w/2, h/2
	rx := float64(cx - 1)
	ry := float64(cy - 1)
	col = cx + int(math.Round(x*rx))
	row = cy + int(math.Round(y*ry))
	if col < 0 {
		col = 0
	}
	if col >= w {
		col = w - 1
	}
	if row < 0 {
		row = 0
	}
	if row >= h {
		row = h - 1
	}
	return col, row, true
}

// projectFromAngle is the shared math between project and the orb spring's
// target computation: given a polar (θ, d) in normalized space, returns the
// rounded grid cell. Kept separate so callers needing the float-space target
// for spring updates don't pay an int-round trip.
func projectFromAngle(θ, d float64, w, h int) (col, row int) {
	col, row, _ = projectXY(math.Cos(θ)*d, -math.Sin(θ)*d, w, h)
	return col, row
}
