package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsSelectionCopyAppliesAndPersists(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	first := a.tab().Focus
	a.split(false)
	second := a.tab().Focus
	panes := []*Pane{first, second}
	for _, pane := range panes {
		waitFor(t, tt, "shell", func() bool { return strings.Contains(pane.terminal().term.Text(), "$") })
	}

	tt.Key(ui.Cmd, ui.KeyComma)
	if err := tt.Click("Terminal settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("原始终端文本"); err != nil {
		t.Fatal(err)
	}
	if !prefs.CopyRawText || !readSettings().CopyRawText {
		t.Fatal("raw text choice did not apply and persist")
	}
	saveSettingsImage(t, tt, "selection-copy-settings")
	tt.Key(0, ui.KeyEscape)

	// Existing panes update immediately, and new sessions inherit the choice.
	third := a.newTab("/tmp").Focus
	panes = append(panes, third)
	tt.Frame()
	waitFor(t, tt, "new shell", func() bool { return strings.Contains(third.terminal().term.Text(), "$") })
	// Keep these disposable sessions quiet while switching tabs and resizing
	// splits. A shell can redraw its prompt after SIGWINCH over the fed fixture.
	for _, pane := range panes {
		pane.terminal().term.Send([]byte("exec /bin/cat >/dev/null\r"))
		waitFor(t, tt, "quiet copy fixture", func() bool {
			refresh(a)
			return pane.terminal().info.Program == "cat" && strings.Contains(pane.terminal().term.Text(), "exec /bin/cat")
		})
	}
	copyPane := func(p *Pane, want string) {
		t.Helper()
		for i, tab := range a.tabs {
			if tab == p.Tab {
				a.selectTab(i)
			}
		}
		p.Tab.setFocus(p)
		a.focusReq = p
		tt.Frame()
		p.terminal().term.Feed([]byte("\x1b[H\x1b[2J1. \x1b[8m**\x1b[28mvisible\x1b[8m**\x1b[0m"))
		tt.Frame()
		tt.Command("selectAll")
		tt.Command("copy")
		if got := tt.Clipboard(); got != want {
			t.Fatalf("pane %d copy = %q, want %q", p.ID, got, want)
		}
	}
	for _, pane := range panes {
		copyPane(pane, "1. **visible**")
	}
	// Restore default uses the same live update path and removes the persisted
	// override. None of the sessions should be replaced when switching modes.
	sids := []string{first.terminal().SID, second.terminal().SID, third.terminal().SID}
	tt.Key(ui.Cmd, ui.KeyComma)
	if err := tt.Click("Restore default Selection copy content"); err != nil {
		t.Fatal(err)
	}
	if prefs.CopyRawText || readSettings().CopyRawText {
		t.Fatal("visible text default did not apply and persist")
	}
	tt.Key(0, ui.KeyEscape)
	for i, pane := range panes {
		copyPane(pane, "1. visible")
		if pane.terminal().SID != sids[i] {
			t.Fatal("copy mode change replaced a session")
		}
	}
}
