package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAgentPaneSelectionOwnershipAndListMarkers(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	for _, agent := range []string{"codex", "claude", "opencode"} {
		p.terminal().info.Program, p.terminal().info.Idle, p.terminal().info.Args = agent, false, nil
		p.terminal().term.Feed([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[H\x1b[2J4. example"))
		tt.Frame()
		r, ok := tt.Find("Terminal")
		if !ok {
			t.Fatal("missing terminal")
		}
		cols, rows := p.terminal().term.Size()
		cw, ch := r.W/float32(cols), r.H/float32(rows)
		y, left, right := r.Y+ch/2, r.X+cw/4, r.X+9.75*cw
		tt.SetClipboard("previous")
		// Tracked alternate-screen programs own their selection regardless of
		// Agent identity. Primary-screen drags still select complete cells.
		tt.Press(right, y)
		tt.Move(left, y)
		tt.Release(left, y)
		if tt.Clipboard() != "previous" {
			t.Fatalf("%s full-screen drag was intercepted by the terminal", agent)
		}
		p.terminal().term.Feed([]byte("\x1b[?1049l\x1b[H\x1b[2J4. example"))
		tt.Frame()
		tt.Press(right, y)
		tt.Move(left, y)
		tt.Release(left, y)
		if got := tt.Clipboard(); got != "4. example" {
			t.Fatalf("%s selection = %q", agent, got)
		}
		// Copy commands continue to use the same terminal selection.
		tt.SetClipboard("previous")
		tt.Key(ui.Cmd, ui.KeyC)
		if tt.Clipboard() != "4. example" {
			t.Fatal("keyboard copy lost the Agent's selected marker")
		}
	}
}

func TestEditorPaneDragSelectionAndProgramChanges(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	for _, editor := range []string{"vim", "nvim", "vi", "view"} {
		t.Run(editor, func(t *testing.T) {
			a, tt := newStaticTestApp(t)
			p := a.tab().Focus
			p.terminal().info.Program, p.terminal().info.Idle, p.terminal().info.Args = editor, false, nil
			p.terminal().term.Feed([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[H\x1b[2Jhello editor"))
			tt.Frame()
			r, ok := tt.Find("Terminal")
			if !ok {
				t.Fatal("missing terminal")
			}
			cols, rows := p.terminal().term.Size()
			cw, ch := r.W/float32(cols), r.H/float32(rows)
			y, left, right := r.Y+ch/2, r.X+cw/4, r.X+4.75*cw
			tt.SetClipboard("previous")
			tt.Press(left, y)
			tt.Move(right, y)
			tt.Release(right, y)
			if got := tt.Clipboard(); got != "hello" {
				t.Fatalf("%s drag copied %q", editor, got)
			}
			tt.SetClipboard("previous")
			tt.Key(ui.Cmd, ui.KeyC)
			if tt.Clipboard() != "hello" {
				t.Fatalf("%s keyboard copy lost selection", editor)
			}
			// The same pane can switch to an Agent that selects its own history.
			p.terminal().info.Program = "codex"
			tt.Frame()
			tt.SetClipboard("keep")
			tt.Press(left, y)
			tt.Move(right, y)
			tt.Release(right, y)
			if tt.Clipboard() != "keep" {
				t.Fatal("editor override remained active after foreground program changed")
			}
		})
	}
}
