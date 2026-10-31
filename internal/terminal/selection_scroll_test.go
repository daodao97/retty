package terminal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSelectionDragScrollsAcrossPagesAtViewportEdges(t *testing.T) {
	loadLib(t)
	var history strings.Builder
	for i := range 100 {
		fmt.Fprintf(&history, "line %03d 中文\r\n", i)
	}
	// A mouse crosses the edge; a finger covers the rows it selects and
	// rests on the first or the last one, inset is how far inside it stops.
	devices := []struct {
		name   string
		opts   Options
		width  int
		scales []float32
		start  ui.InputKind
		inset  func(v *view) float32
	}{
		{"mouse", Options{SelectOnDrag: true}, 500, []float32{1, 2}, ui.InputPointerDown, func(v *view) float32 { return 1 / v.scale }},
		{"touch", Options{FixedCols: 80, FixedRows: 20, ReflowView: true}, 393, []float32{2, 3}, ui.InputLongPress, func(v *view) float32 { return 20 }},
	}
	for _, device := range devices {
		for _, scale := range device.scales {
			for _, tracking := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-scale%g-tracking%v", device.name, scale, tracking), func(t *testing.T) {
					conn := newPipe()
					opts := device.opts
					opts.Conn, opts.NoBlink = conn, true
					term, err := New(opts)
					if err != nil {
						t.Fatal(err)
					}
					defer term.Close()
					term.Feed([]byte(history.String()))
					if tracking {
						term.Feed([]byte("\x1b[?1002h\x1b[?1006h"))
					}
					tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, device.width, 300)
					tt.SetScale(scale)
					tt.Frame()
					v := term.v
					term.mu.Lock()
					v.screen().ScrollBy(-40)
					term.mu.Unlock()
					tt.Frame()
					before := v.screen().Scrollbar().Offset
					x, y := cellCenter(term, 2, v.rows/2)
					v.input(ui.InputEvent{Kind: device.start, X: x, Y: y})
					inset := device.inset(v)
					bottom := float32(v.oy+v.rows*v.cellH)/v.scale - inset
					v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x, Y: bottom})
					for range v.rows + 5 {
						tt.Frame()
					}
					down := v.screen().Scrollbar().Offset
					if down <= before+uint64(v.rows) {
						t.Fatalf("drag did not scroll down beyond one page: %d -> %d, rows %d", before, down, v.rows)
					}
					selected, ok := term.SelectedText()
					if !ok || strings.Count(selected, "\n") < v.rows {
						t.Fatalf("selection did not extend into newly visible rows: %q", selected)
					}
					v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x, Y: y})
					for range 3 {
						tt.Frame()
					}
					if v.ticking || v.screen().Scrollbar().Offset != down {
						t.Fatal("moving back inside did not pause scrolling")
					}
					top := float32(v.oy)/v.scale + inset
					v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x, Y: top})
					for range 2*v.rows + 10 {
						tt.Frame()
					}
					up := v.screen().Scrollbar().Offset
					if up >= before {
						t.Fatalf("drag did not reverse and scroll up: %d -> %d -> %d", before, down, up)
					}
					v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x, Y: top})
					tt.Frame()
					stopped := v.screen().Scrollbar().Offset
					for range 3 {
						tt.Frame()
					}
					if v.ticking || v.screen().Scrollbar().Offset != stopped || tt.Clipboard() == "" {
						t.Fatal("release did not stop scrolling and copy the expanded selection")
					}
					if data := conn.take(1); data != "" {
						t.Fatalf("local selection reached the mouse-aware program: %q", data)
					}
				})
			}
		}
	}
}
