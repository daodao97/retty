package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"retty/internal/rex"
)

// newTestApp starts a session server of its own and an app on it, whose
// view runs without a window.
func newTestApp(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "retty")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("SHELL", "/bin/sh")
	go rex.Serve()
	var client *rex.Client
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(rex.SocketPath()); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, err = rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Shutdown()
		time.Sleep(100 * time.Millisecond)
		os.RemoveAll(dir)
	})
	registerFonts()
	a := &App{client: client}
	a.newTab("/tmp")
	tt := ui.NewTester(a.view, 1000, 620)
	return a, tt
}

// newStaticTestApp renders fed terminal fixtures without a shell that can
// overwrite them when the UI changes the terminal size (SIGWINCH).
func newStaticTestApp(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	registerFonts()
	p := &Pane{ID: 1, content: &terminalPane{}}
	tab := &Tab{ID: 1, Root: &Node{ID: 1, Pane: p}, Focus: p}
	p.Tab, p.Node = tab, tab.Root
	a := &App{tabs: []*Tab{tab}, nextID: 1}
	a.attach(p, 80, 24)
	if p.terminalView() == nil {
		t.Fatal("could not create fixture terminal")
	}
	t.Cleanup(func() { p.terminal().term.Close() })
	return a, ui.NewTester(a.view, 1000, 620)
}

// refresh asks the server what runs in the sessions, as the window does
// every half second.
func refresh(a *App) {
	infos, _ := a.client.List()
	byID := map[string]rex.SessionInfo{}
	for _, in := range infos {
		byID[in.ID] = in
	}
	a.apply(byID)
}

// waitFor runs frames until cond holds, or fails after a few seconds.
func waitFor(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; texts %q", what, tt.Texts())
		}
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
}

func TestPanesAndTabs(t *testing.T) {
	a, tt := newTestApp(t)
	tab := a.tab()
	p := tab.Focus
	waitFor(t, tt, "the shell", func() bool { return p.terminalView() != nil && strings.Contains(p.terminal().term.Text(), "$") })

	// The command palette splits the pane.
	a.openPalette()
	tt.Frame()
	tt.Type("split right")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if n := len(tab.panes()); n != 2 {
		t.Fatalf("%d panes after Split Right", n)
	}
	if a.paletteOpen {
		t.Error("the palette stays open")
	}
	right := tab.Focus
	if right == p {
		t.Fatal("the new pane does not have the focus")
	}

	// The palette also splits down and zooms without pane headers.
	a.openPalette()
	tt.Frame()
	tt.Type("split down")
	tt.Key(0, ui.KeyEnter)
	if n := len(tab.panes()); n != 3 || !tab.Root.B.Vertical {
		t.Fatalf("%d panes after Split Down", n)
	}
	a.openPalette()
	tt.Frame()
	tt.Type("zoom")
	tt.Key(0, ui.KeyEnter)
	if tab.Zoom == nil {
		t.Fatal("not zoomed")
	}
	a.toggleZoom()

	// Focus moves between panes by where they are.
	tt.Frame()
	a.moveFocus(-1, 0)
	if tab.Focus != p {
		t.Errorf("focus left went to pane %d, not %d", tab.Focus.ID, p.ID)
	}
	a.moveFocus(1, 0)
	if tab.Focus == p {
		t.Error("focus right stayed")
	}

	// Typing reaches the shell, and the pane label follows its directory.
	a.focusReq = p
	tab.setFocus(p)
	tt.Frame()
	tt.Type("cd /usr/bin && sleep 3")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "sleep in the pane label", func() bool {
		refresh(a)
		name, detail := p.label()
		return name == "sleep" && detail == "/usr/bin"
	})

	// A second tab, renamed, then closed with its pane.
	a.newTab("/tmp")
	if len(a.tabs) != 2 || a.active != 1 {
		t.Fatalf("%d tabs, active %d", len(a.tabs), a.active)
	}
	a.startRename()
	tt.Frame()
	tt.Type("logs")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.tabs[1].Name != "logs" {
		t.Errorf("tab named %q", a.tabs[1].Name)
	}
	a.closeFocused()
	if len(a.tabs) != 1 || a.active != 0 {
		t.Fatalf("%d tabs, active %d, after closing the last pane of a tab", len(a.tabs), a.active)
	}

	// A shell that exits closes its pane.
	a.selectTab(0)
	tab.setFocus(right)
	right.terminal().term.Send([]byte("exit\r"))
	waitFor(t, tt, "the pane to close", func() bool { return len(tab.panes()) == 2 })
}

func TestRestore(t *testing.T) {
	a, tt := newTestApp(t)
	a.split(false)
	a.tab().Root.Ratio = 0.3
	a.newTab("/usr")
	a.tabs[1].Name = "second"
	tt.Frame()
	a.saveNow()
	sids := map[string]bool{}
	for _, tab := range a.tabs {
		for _, p := range tab.panes() {
			sids[p.terminal().SID] = true
			p.terminal().term.Close() // detach, as when the app quits
		}
	}
	// Another app, as the next launch, finds them all.
	b := &App{client: a.client}
	if !b.restore() {
		t.Fatal("nothing restored")
	}
	if len(b.tabs) != 2 || b.tabs[1].Name != "second" || b.active != 1 {
		t.Fatalf("restored %d tabs, active %d", len(b.tabs), b.active)
	}
	if r := b.tabs[0].Root; r.Pane != nil || r.Ratio != 0.3 {
		t.Errorf("first tab's root %+v", r)
	}
	for _, tab := range b.tabs {
		for _, p := range tab.panes() {
			if !sids[p.terminal().SID] || p.terminal().restored {
				t.Errorf("pane of session %q not attached again", p.terminal().SID)
			}
		}
	}
}

func TestLabels(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := &Pane{content: &terminalPane{info: rex.SessionInfo{Shell: "zsh", Program: "zsh", Idle: true, Dir: home + "/Sites/rex-snake"}}}
	if n, d := p.label(); n != "zsh" || d != "~/Sites/rex-snake" {
		t.Errorf("shell label %q %q", n, d)
	}
	p.terminal().info = rex.SessionInfo{Shell: "fish", Program: "lazygit", Dir: home + "/Sites/rex-snake"}
	p.terminal().title = "lazygit ~/Sites/rex-snake"
	if n, d := p.label(); n != "Git Changes" || d != "~/Sites/rex-snake" {
		t.Errorf("lazygit label %q %q", n, d)
	}
	p.terminal().info.Program = "codex"
	p.terminal().title = "Stress-test Snake demo | rex-snake"
	if n, d := p.label(); n != p.terminal().title || d != "" {
		t.Errorf("codex label %q %q", n, d)
	}
	p.terminal().info, p.terminal().title = rex.SessionInfo{Shell: "zsh", Program: "ssh", Args: []string{"ssh", "-p", "22", "box.local"}}, ""
	if n, d := p.label(); n != "SSH" || d != "box.local" {
		t.Errorf("ssh label %q %q", n, d)
	}
	if d := shortDir("/private/tmp/a/b/c/d/e"); d != "…/d/e" {
		t.Errorf("short dir %q", d)
	}
}

// TestCloseButtons presses and releases the tab close buttons that show while
// the pointer is over a tab, with a frame between: pressing one
// must not hide it.
func TestCloseButtons(t *testing.T) {
	a, tt := newTestApp(t)
	first := a.tabs[0]
	a.newTab("/tmp")
	tt.Frame()
	track, ok := tt.Find("Tabs")
	if !ok {
		t.Fatal("no tab bar")
	}
	// The pointer over the first tab shows its close button.
	tt.Move(track.X+40, track.Y+track.H/2)
	tt.Frame()
	x, ok := tt.Find("Close Tab")
	if !ok {
		t.Fatalf("no close button over the tab; texts %q", tt.Texts())
	}
	if x.X < track.X+track.W/2-32 {
		t.Fatalf("close button is not at the right edge of the tab: %+v", x)
	}
	tt.Press(x.X+x.W/2, x.Y+x.H/2)
	tt.Frame()
	tt.Release(x.X+x.W/2, x.Y+x.H/2)
	tt.Frame()
	if len(a.tabs) != 1 || slices.Contains(a.tabs, first) {
		t.Fatalf("%d tabs after clicking the first one's close button", len(a.tabs))
	}
}
