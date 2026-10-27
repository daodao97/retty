package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPaletteFrameworkChoicesAndDismissal(t *testing.T) {
	for _, action := range []string{"enter", "click", "empty", "escape", "outside"} {
		t.Run(action, func(t *testing.T) {
			a, tt := newStaticTestApp(t)
			before := a.tab().Focus.bounds
			a.openPalette()
			tt.Frame()
			if !tt.Focused("Search commands") {
				t.Fatal("palette did not focus its input")
			}
			switch action {
			case "enter", "click":
				tt.Type("find")
				if action == "enter" {
					tt.Key(0, ui.KeyEnter)
				} else if err := tt.Click("Find…"); err != nil {
					t.Fatal(err)
				}
				if a.paletteOpen || !a.tab().Focus.terminal().find.open {
					t.Fatal("choice did not execute the filtered command")
				}
				a.closeFind(a.tab().Focus)
			case "empty":
				tt.Type("no-such-command-928")
				if !tt.HasText("No matches") {
					t.Fatal("empty result hidden")
				}
				tt.Key(0, ui.KeyEnter)
				if !a.paletteOpen || a.tab().Focus.terminal().find.open {
					t.Fatal("empty result executed an old command")
				}
				tt.Key(0, ui.KeyEscape)
			case "escape":
				tt.Key(0, ui.KeyEscape)
			case "outside":
				tt.ClickAt(20, 40)
			}
			tt.Frame()
			if a.paletteOpen || !tt.Focused("Terminal") {
				t.Fatal("closing did not restore terminal focus")
			}
			if a.tab().Focus.bounds != before {
				t.Fatal("palette changed terminal dimensions")
			}
		})
	}
}

func TestPaletteKeyboardSelectionAndFreeScroll(t *testing.T) {
	a, tt := newStaticTestApp(t)
	a.openPalette()
	tt.Frame()
	saveSettingsImage(t, tt, "palette-inline-light")
	tt.SetDark(true)
	saveSettingsImage(t, tt, "palette-inline-dark")
	first, ok := tt.Find("Settings…")
	if !ok {
		t.Fatal("command list missing")
	}
	for range 8 {
		tt.Scroll(first.X+30, first.Y+10, 0, 160)
	}
	last, ok := tt.Find("Find Previous")
	if !ok || last.H == 0 || last.Y > 500 {
		t.Fatalf("wheel did not stay at the end: %v", last)
	}
	tt.Type("find")
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyEnter)
	if a.paletteOpen || !a.tab().Focus.terminal().find.open {
		t.Fatal("framework navigation did not select the filtered command")
	}
}
