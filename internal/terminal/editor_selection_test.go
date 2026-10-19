package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEditorDragCopiesWithMouseTracking(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn, SelectOnDrag: true, SelectOnDragInAltScreen: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 500, 300)
	term.Feed([]byte("\x1b[?1049h\x1b[?1002h\x1b[?1006hhello editor"))
	tt.Frame()
	x0, y := cellCenter(term, 0, 0)
	x1, _ := cellCenter(term, 4, 0)
	tt.SetClipboard("previous")
	tt.Press(x0-2, y)
	tt.Move(x1+2, y)
	tt.Release(x1+2, y)
	if got := tt.Clipboard(); got != "hello" {
		t.Fatalf("editor drag copied %q", got)
	}
	if data := conn.take(1); data != "" {
		t.Fatalf("local selection reached editor: %q", data)
	}
	// Clicks and wheel input still operate the editor.
	xClick, yClick := cellCenter(term, 2, 3)
	tt.ClickAt(xClick, yClick)
	if got := conn.take(len("\x1b[<0;3;4M\x1b[<0;3;4m")); got != "\x1b[<0;3;4M\x1b[<0;3;4m" {
		t.Fatalf("editor click = %q", got)
	}
	tt.Wheel(x0, y, 0, 40)
	wheel := strings.Repeat("\x1b[<65;1;1M", 3)
	if got := conn.take(len(wheel)); got != wheel {
		t.Fatalf("editor wheel = %q", got)
	}
	// Option gives a complete drag back to the editor, including when released
	// after Option itself is released.
	v := term.v
	v.input(ui.InputEvent{Kind: ui.InputPointerDown, X: x0, Y: y, Mods: ui.Alt})
	v.input(ui.InputEvent{Kind: ui.InputPointerMove, X: x1, Y: y, Mods: ui.Alt})
	v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: x1, Y: y})
	if got := conn.take(len("\x1b[<8;1;1M\x1b[<40;5;1M\x1b[<0;5;1m")); got != "\x1b[<8;1;1M\x1b[<40;5;1M\x1b[<0;5;1m" {
		t.Fatalf("Option drag = %q", got)
	}
	// Returning to an application that owns history restores native dragging.
	term.SetSelectOnDragInAltScreen(false)
	tt.Press(x0, y)
	tt.Move(x1, y)
	tt.Release(x1, y)
	if got := conn.take(len("\x1b[<0;1;1M\x1b[<32;5;1M\x1b[<0;5;1m")); got != "\x1b[<0;1;1M\x1b[<32;5;1M\x1b[<0;5;1m" {
		t.Fatalf("restored native drag = %q", got)
	}
}

func TestVimMouseModeDragCopiesFileText(t *testing.T) {
	loadLib(t)
	vim, err := exec.LookPath("vim")
	if err != nil {
		t.Skip("vim is not installed")
	}
	file := filepath.Join(t.TempDir(), "selection.txt")
	if err := os.WriteFile(file, []byte("hello editor\nsecond line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	term := shell(t, Options{
		Command:      []string{vim, "-Nu", "NONE", "-i", "NONE", "-n", "-N", "-c", "set mouse=a", file},
		SelectOnDrag: true, SelectOnDragInAltScreen: true,
	})
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus() }, 500, 300)
	waitFor(t, tt, "Vim mouse mode", func() bool {
		term.mu.Lock()
		ready := term.term.AltScreen() && term.term.MouseTracking()
		term.mu.Unlock()
		return ready && strings.Contains(term.Text(), "hello editor")
	})
	x0, y := cellCenter(term, 0, 0)
	x1, _ := cellCenter(term, 4, 0)
	tt.SetClipboard("previous")
	tt.Press(x0-2, y)
	tt.Move(x1+2, y)
	tt.Release(x1+2, y)
	if got := tt.Clipboard(); got != "hello" {
		t.Fatalf("Vim drag copied %q", got)
	}
	// Mobile starts selection with a long press, independently of the desktop
	// override. Vim's mouse mode must still allow selecting and copying a word.
	term.SetSelectOnDragInAltScreen(false)
	xWord, _ := cellCenter(term, 8, 0)
	term.v.input(ui.InputEvent{Kind: ui.InputLongPress, X: xWord, Y: y})
	term.v.input(ui.InputEvent{Kind: ui.InputPointerUp, X: xWord, Y: y})
	tt.Frame()
	if got := tt.Clipboard(); got != "editor" {
		t.Fatalf("Vim long press copied %q", got)
	}
}
