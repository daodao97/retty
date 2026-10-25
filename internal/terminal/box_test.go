package terminal

import (
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
