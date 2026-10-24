package terminal

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSoftwareModifiersUseSessionEncoder(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn, OptionAsAlt: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	var mods ui.Modifiers
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus().InputModifiers(mods, nil) }, 400, 200)
	for _, c := range []struct {
		mods       ui.Modifiers
		text, want string
	}{
		{ui.Ctrl, "c", "\x03"}, {ui.Ctrl, "d", "\x04"}, {ui.Ctrl, "r", "\x12"}, {ui.Ctrl, "z", "\x1a"}, {ui.Alt, "b", "\x1bb"}, {ui.Alt, "f", "\x1bf"},
		{ui.Shift, "a", "A"}, {ui.Shift, "/", "?"},
	} {
		mods = c.mods
		tt.Frame()
		tt.Type(c.text)
		if got := conn.take(len(c.want)); got != c.want {
			t.Fatalf("%v + %s: %q, want %q", c.mods, c.text, got, c.want)
		}
	}
	term.Feed([]byte("\x1b[>1u"))
	tt.Frame()
	mods = ui.Ctrl
	tt.Frame()
	tt.Type("c")
	if got := conn.take(7); got != "\x1b[99;5u" {
		t.Fatal("software Ctrl bypassed Kitty encoding", got)
	}
	mods = ui.Super
	tt.Frame()
	tt.Type("k")
	if got := conn.take(8); got != "\x1b[107;9u" {
		t.Fatal("software Cmd bypassed Kitty encoding", got)
	}
	term.Feed([]byte("\x1b[<u\x1b[?1h"))
	tt.Frame()
	if !term.SendKey(ui.KeyUp, 0) || conn.take(3) != "\x1bOA" {
		t.Fatal("accessory arrow ignored application cursor mode")
	}
	if !term.SendKey(ui.KeyBackslash, ui.Shift) || conn.take(1) != "|" {
		t.Fatal("accessory symbol ignored Shift")
	}
	// The native Return/Delete keys also receive virtual modifiers, including
	// matching releases when Kitty event reporting is enabled.
	term.Feed([]byte("\x1b[>11u"))
	mods = ui.Ctrl
	tt.Frame()
	tt.Key(0, ui.KeyBackspace)
	want := "\x1b[127;5u\x1b[127;5:3u"
	if got := conn.take(len(want)); got != want {
		t.Fatalf("modified Delete lost its press/release: %q", got)
	}
}

func TestSoftwareCmdSelectionAndBracketedPaste(t *testing.T) {
	loadLib(t)
	conn := newPipe()
	term, err := New(Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill().AutoFocus().InputModifiers(ui.Super, nil) }, 400, 200)
	term.Feed([]byte("copy sample"))
	tt.Frame()
	tt.Type("a")
	tt.Type("c")
	if tt.Clipboard() != "copy sample" {
		t.Fatal("Cmd+A/C did not use terminal selection", tt.Clipboard())
	}
	term.Feed([]byte("\x1b[?2004h"))
	tt.Frame()
	tt.SetClipboard("pasted")
	tt.Type("v")
	if got := conn.take(18); got != "\x1b[200~pasted\x1b[201~" {
		t.Fatal("Cmd+V bypassed bracketed paste", got)
	}
}

func TestCtrlVImageHandlerSuppressesRemoteClipboardKey(t *testing.T) {
	for _, handled := range []bool{false, true} {
		name := "ordinary Ctrl+V"
		if handled {
			name = "image consumed"
		}
		t.Run(name, func(t *testing.T) {
			loadLib(t)
			conn := newPipe()
			calls := 0
			term, err := New(Options{Conn: conn, OnPaste: func(ui.Services) bool { calls++; return handled }})
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			v := &view{t: term}
			if !v.keyDown(ui.InputEvent{Mods: ui.Ctrl, Key: ui.KeyV}) {
				t.Fatal("Ctrl+V not handled")
			}
			if calls != 1 {
				t.Fatal("image hook not consulted")
			}
			if handled {
				if got := conn.take(1); got != "" {
					t.Fatal("handled image sent terminal bytes", got)
				}
			} else {
				if got := conn.take(1); got != "\x16" {
					t.Fatal("ordinary Ctrl+V changed", got)
				}
			}
		})
	}
}
