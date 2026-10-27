package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Advance the UI's virtual clock rather than sleeping: a completed search must
// stop requesting frames, while new output and navigation still update results.
func TestCompletedFindStopsPollingAndAcceptsNewMatches(t *testing.T) {
	a, _ := newStaticTestApp(t)
	tab := a.tab()
	p := tab.Focus
	p.terminal().term.Feed([]byte("\x1b[?25l\r\nquiet-target one\r\nquiet-target two\r\n"))
	p.terminal().find.open, p.terminal().find.query = true, "quiet-target"
	if err := p.terminal().term.SetSearch(p.terminal().find.query); err != nil {
		t.Fatal(err)
	}
	frames := 0
	tt := ui.NewTester(func(c *ui.Context) {
		frames++
		a.findBar(c, colorsOf(c), tab, p)
	}, 600, 36)
	tt.Advance(time.Second)
	if s := p.terminal().term.SearchState(); s.Busy || s.Total != 2 {
		t.Fatalf("search did not finish: %+v", s)
	}
	settled := frames
	tt.Advance(2 * time.Second)
	if frames != settled {
		t.Fatalf("completed search requested %d idle frames", frames-settled)
	}
	p.terminal().term.Feed([]byte("quiet-target three\r\n"))
	tt.Frame()
	if s := p.terminal().term.SearchState(); s.Busy || s.Total != 3 {
		t.Fatalf("new output was not searched: %+v", s)
	}
	previous := p.terminal().term.SearchState().Current
	if err := tt.Click("Find Next"); err != nil {
		t.Fatal(err)
	}
	if s := p.terminal().term.SearchState(); s.Current == previous {
		t.Fatalf("navigation did not advance: %+v", s)
	}
}

func TestFindShortcutsAndPaneIsolation(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	waitFor(t, tt, "the shell", func() bool { return strings.Contains(p.terminal().term.Text(), "$") })
	p.terminal().term.Feed([]byte("\r\nfind-target one\r\nfind-target two\r\nfind-target three\r\n"))
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	if !p.terminal().find.open {
		t.Fatal("Cmd-F did not open search")
	}
	if _, ok := tt.Find("Find in Terminal"); !ok {
		t.Fatal("no search field")
	}
	before := p.terminal().term.Text()
	tt.Type("find-target")
	tt.Frame()
	if p.terminal().find.query != "find-target" {
		t.Fatalf("query %q", p.terminal().find.query)
	}
	if s := p.terminal().term.SearchState(); s.Total != 3 || s.Current != 1 {
		t.Fatalf("matches %+v", s)
	}
	if p.terminal().term.Text() != before {
		t.Fatal("search text was sent to shell")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if s := p.terminal().term.SearchState(); s.Current != 2 {
		t.Fatalf("Enter %+v", s)
	}
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Frame()
	if s := p.terminal().term.SearchState(); s.Current != 1 {
		t.Fatalf("Shift-Enter %+v", s)
	}
	tt.Key(ui.Cmd, ui.KeyG)
	tt.Frame()
	if s := p.terminal().term.SearchState(); s.Current != 2 {
		t.Fatalf("Cmd-G %+v", s)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyG)
	tt.Frame()
	if s := p.terminal().term.SearchState(); s.Current != 1 {
		t.Fatalf("Shift-Cmd-G %+v", s)
	}
	if err := tt.Click("Find Next"); err != nil {
		t.Fatal(err)
	}
	if s := p.terminal().term.SearchState(); s.Current != 2 {
		t.Fatalf("next button %+v", s)
	}
	// Escape from a navigation button should close search too.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if p.terminal().find.open {
		t.Fatal("Escape did not close search")
	}
	tt.Type("echo back-in-shell")
	waitFor(t, tt, "focus restored", func() bool { return strings.Contains(p.terminal().term.Text(), "echo back-in-shell") })
	tt.Key(ui.Ctrl, ui.KeyU)
	a.split(false)
	other := a.tab().Focus
	other.terminal().term.Feed([]byte("\r\nother-target\r\n"))
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	tt.Type("other-target")
	tt.Frame()
	if other.terminal().find.query != "other-target" || p.terminal().find.query != "find-target" {
		t.Fatal("search leaked across panes")
	}
	a.closeFind(other)
	a.tab().setFocus(p)
	a.openFind()
	tt.Frame()
	if p.terminal().find.query != "find-target" {
		t.Fatal("reopening search lost the query")
	}
	tt.Command("selectAll")
	tt.Type("unfindable")
	tt.Frame()
	if !tt.HasText("No matches") {
		t.Fatalf("no missing-match feedback: %q", tt.Texts())
	}
	tt.Command("selectAll")
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if p.terminal().find.query != "" || p.terminal().term.SearchState().Total != 0 {
		t.Fatal("clearing the query kept highlights")
	}
	a.closeFind(p)
	a.openPalette()
	tt.Frame()
	tt.Type("find…")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Type("find-target")
	tt.Frame()
	if !p.terminal().find.open || p.terminal().find.query != "find-target" {
		t.Fatal("Find from the command palette did not focus its field")
	}
}
