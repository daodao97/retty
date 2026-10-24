package terminal

import (
	"bytes"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A retained row must match a freshly rendered terminal after its runs grow,
// shrink, change styles, become empty, and contain multi-rune or wide cells.
func TestRetainedRowBuffersMatchFreshFrames(t *testing.T) {
	loadLib(t)
	newTerminal := func() (*Terminal, *ui.Tester) {
		t.Helper()
		term, err := New(Options{Conn: newPipe(), Theme: DarkTheme(), NoBlink: true, FixedCols: 60, FixedRows: 6})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { term.Close() })
		tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 600, 160)
		tt.SetScale(2)
		tt.Frame()
		return term, tt
	}
	retained, screen := newTerminal()
	const clear = "\x1b[0m\x1b[?25l\x1b[H\x1b[2J"
	for i, text := range []string{
		"\x1b[31mred\x1b[32mgreen\x1b[1;34mbold blue\x1b[0m\r\n" + strings.Repeat("letters ", 7),
		"short\r\n\x1b[33m中文🙂e\u0301\x1b[0m",
		"",
		"\x1b[31ma\x1b[32mb\x1b[34mc\x1b[35md\x1b[36me\x1b[37mf\x1b[0m\r\n\x1b[3mitalic 中文\x1b[0m",
		strings.Repeat("W", 59) + "\r\n\x1b[4;9munderlined\x1b[0m",
		"final",
	} {
		retained.Feed([]byte(clear + text))
		screen.Frame()
		fresh, reference := newTerminal()
		fresh.Feed([]byte(clear + text))
		reference.Frame()
		if !bytes.Equal(screen.Image().Pix, reference.Image().Pix) {
			t.Fatalf("retained row buffers changed rendered pixels at transition %d", i)
		}
	}
}

func TestWideRowReleasesOversizedGlyphBuffersWhenShortened(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe(), Theme: DarkTheme(), NoBlink: true, FixedCols: 16000, FixedRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 600, 100)
	const clear = "\x1b[?25l\x1b[H\x1b[2J"
	term.Feed([]byte(clear + strings.Repeat("M", 12000)))
	tt.Frame()
	large := 0
	for _, line := range term.v.lines {
		for _, run := range line.runs {
			large += cap(run.glyphs)
		}
	}
	if large < 12000 {
		t.Fatalf("fixture did not build a wide row: capacity %d", large)
	}
	term.Feed([]byte(clear + "short"))
	tt.Frame()
	retained, retainedCols := 0, 0
	for _, line := range term.v.lines {
		for _, run := range line.runs {
			retained += cap(run.glyphs)
			retainedCols += cap(run.cols)
		}
	}
	if retained > 1024 || retainedCols > 1024 {
		t.Fatalf("shortened row retained %d glyph and %d column slots from the wide row", retained, retainedCols)
	}
}
