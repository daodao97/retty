package main

import (
	"image"
	"math"
	"net"
	"testing"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal"
)

// Check the rendered separator pixels, including nested splits and a
// resize, so a second outline beside the separator cannot regress.
func TestCompactFocusSharesSeparatorPixels(t *testing.T) {
	left, top, bottom := &Pane{ID: 1, content: &terminalPane{}}, &Pane{ID: 2, content: &terminalPane{}}, &Pane{ID: 3, content: &terminalPane{}}
	right := &Node{ID: 4, Vertical: true, Ratio: 0.61,
		A: &Node{ID: 5, Pane: top}, B: &Node{ID: 6, Pane: bottom}}
	tab := &Tab{Root: &Node{ID: 7, Ratio: 0.37, A: &Node{ID: 8, Pane: left}, B: right}, Focus: left}
	a := &App{}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(ui.DarkTheme())
		c.Root().Background(darkTerm.Background)
		ui.Column(c).Fill().Children(func() {
			ui.Box(c).Height(compactTitleH)
			a.tabContent(c, &darkColors, tab)
		})
	}, 1000, 620)
	for _, scale := range []float32{1, 2} {
		tt.SetScale(scale)
		for _, size := range [][2]int{{1000, 620}, {937, 577}} {
			tt.SetSize(size[0], size[1])
			for _, focus := range []*Pane{left, top, bottom} {
				tab.Focus = focus
				tt.Frame()
				x := (left.bounds.X + left.bounds.W + top.bounds.X) / 2
				y := (top.bounds.Y + top.bounds.H + bottom.bounds.Y) / 2
				for _, pane := range []*Pane{top, bottom} {
					color := darkColors.headerBorder
					if focus == left || focus == pane {
						color = darkColors.cardBorderFocused
					}
					assertCompactSeparator(t, tt.Image(), scale, x, pane.bounds.Y+40, true, color)
				}
				color := darkColors.headerBorder
				if focus != left {
					color = darkColors.cardBorderFocused
				}
				// Hover away from the grip: the full separator must remain.
				tt.Move(top.bounds.X+40, y)
				assertCompactSeparator(t, tt.Image(), scale, top.bounds.X+40, y, false, color)
				assertCompactSeparator(t, tt.Image(), scale, x+1.5, y, false, color)
				tt.Move(20, 100)
			}
		}
	}
}

func TestCompactBackgroundTouchesDivider(t *testing.T) {
	if err := terminal.Load(); err != nil {
		t.Fatal(err)
	}
	registerFonts()
	conn, peer := net.Pipe()
	defer peer.Close()
	term, err := terminal.New(terminal.Options{Conn: conn, Font: termFont, Theme: darkTerm, NoBlink: true})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	defer term.Close()
	left, right := &Pane{ID: 1, content: &terminalPane{term: term}}, &Pane{ID: 2, content: &terminalPane{}}
	tab := &Tab{Root: &Node{ID: 3, Ratio: 0.37, A: &Node{ID: 4, Pane: left}, B: &Node{ID: 5, Pane: right}}, Focus: left}
	a := &App{}
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(ui.DarkTheme())
		c.Root().Background(darkTerm.Background)
		ui.Column(c).Fill().Children(func() {
			ui.Box(c).Height(compactTitleH)
			a.tabContent(c, &darkColors, tab)
		})
	}, 937, 577)
	for _, scale := range []float32{1, 2} {
		tt.SetScale(scale)
		term.Feed([]byte("\x1b[?25l\x1b[H\x1b[48;2;80;90;100m\x1b[2J\x1b[0m"))
		tt.Frame()
		center := (left.bounds.X + left.bounds.W + right.bounds.X) / 2
		start := int(math.Round(float64((center - 0.5) * scale)))
		end := int(math.Round(float64((center + 0.5) * scale)))
		img := tt.Image()
		for x := start - int(4*scale); x < end+int(4*scale); x++ {
			want := darkTerm.Background
			if x < start {
				want = ui.RGB(80, 90, 100)
			} else if x < end {
				want = darkColors.cardBorderFocused
			}
			got := img.RGBAAt(x, int(100*scale))
			if got.R != want.R || got.G != want.G || got.B != want.B {
				t.Fatalf("gap beside divider at scale %g, pixel %d: %v, want %v", scale, x, got, want)
			}
		}
	}
	// The wider pointer target must win over the neighboring terminal.
	center := (left.bounds.X + left.bounds.W + right.bounds.X) / 2
	old := tab.Root.Ratio
	tt.Press(center+1.5, 100)
	tt.Move(center+31.5, 100)
	tt.Release(center+31.5, 100)
	if tab.Root.Ratio < old+0.02 {
		t.Fatal("dragging beside the one-point separator missed its wider pointer target")
	}
}

func assertCompactSeparator(t *testing.T, img *image.RGBA, scale, x, y float32, vertical bool, line ui.Color) {
	t.Helper()
	center := x
	if !vertical {
		center = y
	}
	start := int(math.Round(float64((center - 0.5) * scale)))
	end := int(math.Round(float64((center + 0.5) * scale)))
	for pos := start - int(3*scale); pos < end+int(3*scale); pos++ {
		px, py := pos, int(y*scale)
		if !vertical {
			px, py = int(x*scale), pos
		}
		want := darkTerm.Background
		if pos >= start && pos < end {
			want = line
		}
		got := img.RGBAAt(px, py)
		if got.R != want.R || got.G != want.G || got.B != want.B {
			t.Fatalf("separator at (%g, %g), scale %g: pixel (%d, %d) = %v, want %v", x, y, scale, px, py, got, want)
		}
	}
}
