package hud

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestProjectOrigin(t *testing.T) {
	col, row, ok := project("N", 0, 41, 21)
	if !ok || col != 20 || row != 10 {
		t.Fatalf("origin: got (%d,%d) ok=%v, want (20,10) true", col, row, ok)
	}
}

func TestProjectCardinals(t *testing.T) {
	cases := []struct {
		dir        string
		dCol, dRow int // expected delta from center
	}{
		{"E", +1, 0},
		{"W", -1, 0},
		{"N", 0, -1},
		{"S", 0, +1},
	}
	w, h := 41, 21
	cx, cy := w/2, h/2
	for _, c := range cases {
		col, row, ok := project(c.dir, 1.0, w, h)
		if !ok {
			t.Fatalf("%s: not ok", c.dir)
		}
		// at full distance the cardinal direction must be at the corresponding axis edge
		if c.dCol > 0 && col <= cx {
			t.Errorf("%s: col %d should be > center %d", c.dir, col, cx)
		}
		if c.dCol < 0 && col >= cx {
			t.Errorf("%s: col %d should be < center %d", c.dir, col, cx)
		}
		if c.dRow > 0 && row <= cy {
			t.Errorf("%s: row %d should be > center %d", c.dir, row, cy)
		}
		if c.dRow < 0 && row >= cy {
			t.Errorf("%s: row %d should be < center %d", c.dir, row, cy)
		}
	}
}

func TestProjectUnknownDirection(t *testing.T) {
	if _, _, ok := project("ZZ", 0.5, 40, 20); ok {
		t.Fatal("unknown direction should return ok=false")
	}
}

func TestOrbStyleBuckets(t *testing.T) {
	// Distinct color per bucket so the gradient is visually distinguishable.
	cases := []float64{0.0, 0.25, 0.26, 0.50, 0.51, 0.75, 0.76, 1.0}
	colors := make([]string, len(cases))
	for i, d := range cases {
		colors[i] = string(orbStyle(d).GetForeground().(lipgloss.Color))
	}
	want := []string{"10", "10", "11", "11", "208", "208", "9", "9"}
	for i, got := range colors {
		if got != want[i] {
			t.Errorf("orbStyle(%.2f): got %q, want %q", cases[i], got, want[i])
		}
	}
}
