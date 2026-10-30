package terminal

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Powerline's rounded caps span the cell as its triangles do: at a line
// height of 1.22 the font's caps fell short of the prompt's background
// (kero#210).
func TestPowerlineRoundedCaps(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme(), Font: Font{LineHeight: 1.6}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 400, 200)
	tt.Frame()
	term.Feed([]byte("\x1b[38;2;255;0;0m"))
	tt.Frame()
	v := term.v
	red := func(col, x, y int) bool {
		c := tt.Image().RGBAAt(v.ox+col*v.cellW+x, v.oy+y)
		return c.R > 200 && c.G < 100 && c.B < 100
	}
	w, h := v.cellW, v.cellH
	for _, c := range []struct {
		name   string
		col    int
		x, y   int
		filled bool
	}{
		{"E0B4 flat side, top", 0, 0, 1, true},
		{"E0B4 flat side, bottom", 0, 0, h - 2, true},
		{"E0B4 tip", 0, w - 1, h / 2, true},
		{"E0B4 corner past the curve", 0, w - 1, 1, false},
		{"E0B6 flat side, top", 1, w - 1, 1, true},
		{"E0B6 flat side, bottom", 1, w - 1, h - 2, true},
		{"E0B6 tip", 1, 0, h / 2, true},
		{"E0B6 corner past the curve", 1, 0, 1, false},
		{"E0B5 inside the outline", 2, w / 4, h / 2, false},
		{"E0B7 inside the outline", 3, w - 1 - w/4, h / 2, false},
	} {
		if got := red(c.col, c.x, c.y); got != c.filled {
			t.Errorf("%s (%d,%d in a %d×%d cell): drawn %v, want %v", c.name, c.x, c.y, w, h, got, c.filled)
		}
	}
}

// Sextants span the cell, two across and three down, sharing edges so a
// run of them has no seam. At a line height of 1.6 a font glyph stops
// short of that cell, so a pixel on its top and its bottom tells a drawn
// sextant from a glyph. U+1FB00 is the upper left block alone. U+1FB3B is
// the other five: its index sits past ▌, ▐ and █, so a wrong skip paints
// a different block. U+1FB3C starts the smooth mosaics, which stay with
// the font.
func TestSextants(t *testing.T) {
	if !drawn(0x1fb00) || !drawn(0x1fb3b) || drawn(0x1fb3c) {
		t.Fatalf("drawn U+1FB00 %v, U+1FB3B %v, U+1FB3C %v", drawn(0x1fb00), drawn(0x1fb3b), drawn(0x1fb3c))
	}
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: LightTheme(), Font: Font{LineHeight: 1.6}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 400, 200)
	tt.Frame()
	term.Feed([]byte("\x1b[38;2;255;0;0m\U0001FB00\U0001FB3B"))
	tt.Frame()
	v := term.v
	w, h := v.cellW, v.cellH
	if w < 4 || h < 6 {
		t.Fatalf("cell %d×%d is too small to resolve sextants", w, h)
	}
	// The same edges drawSextant rounds to. The pixel on a shared edge
	// belongs to the block on its right or below.
	edge := func(n, f float32) int { return int(math.Round(float64(n * f))) }
	xm := edge(float32(w), 0.5)
	ya := edge(float32(h), 1.0/3)
	yb := edge(float32(h), 2.0/3)
	// Bit 0 is the upper left, then upper right, middle left, middle
	// right, lower left, lower right.
	cells := []struct {
		r       rune
		pattern int
	}{{0x1fb00, 0b000001}, {0x1fb3b, 0b111110}}
	red := func(col, x, y int) bool {
		c := tt.Image().RGBAAt(v.ox+col*v.cellW+x, v.oy+y)
		return c.R > 200 && c.G < 100 && c.B < 100
	}
	shown := 0
	for col, cell := range cells {
		for y := range h {
			for x := range w {
				bit := 0
				if x >= xm {
					bit = 1
				}
				switch {
				case y >= yb:
					bit += 4
				case y >= ya:
					bit += 2
				}
				want := cell.pattern&(1<<bit) != 0
				if red(col, x, y) == want {
					continue
				}
				t.Errorf("U+%04X (%d,%d in a %d×%d cell): drawn %v, want %v", cell.r, x, y, w, h, !want, want)
				shown++
				if shown == 8 {
					t.Fatal("too many mismatched pixels")
				}
			}
		}
	}
}
