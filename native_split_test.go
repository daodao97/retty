package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNativePaneSplitKeyboardResizeAndReset(t *testing.T) {
	for _, vertical := range []bool{false, true} {
		t.Run(map[bool]string{false: "columns", true: "rows"}[vertical], func(t *testing.T) {
			left, right := &Pane{ID: 1, content: &terminalPane{}}, &Pane{ID: 2, content: &terminalPane{}}
			n := &Node{ID: 3, Ratio: .37, Vertical: vertical, A: &Node{ID: 4, Pane: left}, B: &Node{ID: 5, Pane: right}}
			tab := &Tab{Root: n, Focus: left}
			a := &App{}
			tt := ui.NewTester(func(c *ui.Context) { a.tabContent(c, colorsOf(c), tab) }, 800, 600)
			tt.Frame()
			divider, ok := tt.Find("Divider")
			if !ok {
				t.Fatal("missing accessible divider")
			}
			tt.ClickAt(divider.X+divider.W/2, divider.Y+divider.H/2)
			key := ui.KeyRight
			if vertical {
				key = ui.KeyDown
			}
			tt.Key(0, key)
			if n.Ratio <= .37 || !a.saveDue {
				t.Fatal("keyboard resize did not update the saved ratio", n.Ratio)
			}
			ratio := n.Ratio
			tt.SetSize(100, 100)
			if left.bounds.W < 40 || left.bounds.H < 40 || right.bounds.W < 40 || right.bounds.H < 40 || n.Ratio != ratio {
				t.Fatal("small window lost a pane or overwrote the chosen ratio", left.bounds, right.bounds, n.Ratio)
			}
			tt.SetSize(800, 600)
			divider, _ = tt.Find("Divider")
			x, y := divider.X+divider.W/2, divider.Y+divider.H/2
			tt.Advance(time.Second)
			tt.ClickAt(x, y)
			tt.ClickAt(x, y)
			if n.Ratio != .5 {
				t.Fatal("double click did not equalize the split", n.Ratio)
			}
		})
	}
}

func TestDesktopIconButtonsUseKeyboardActivation(t *testing.T) {
	for _, toolbar := range []bool{false, true} {
		t.Run(map[bool]string{false: "icon", true: "titlebar"}[toolbar], func(t *testing.T) {
			activations := 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Row(c).Height(32).Children(func() {
					var button ui.Element
					if toolbar {
						button = compactTitleButton(c, colorsOf(c), "plus", "Action", false)
					} else {
						button = iconButton(c, colorsOf(c), "plus", "Action", 24, 14)
					}
					if button.Clicked() {
						activations++
					}
				})
			}, 200, 100)
			tt.Frame()
			tt.Key(0, ui.KeyTab)
			tt.Key(0, ui.KeyEnter)
			tt.Key(0, ui.KeySpace)
			if activations != 2 {
				t.Fatal("Enter and Space did not each activate the button once", activations)
			}
		})
	}
}
