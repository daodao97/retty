package main

import (
	"slices"
	"testing"
)

func TestPaneCloseButtonTargetsItsPane(t *testing.T) {
	a, tt := newTestApp(t)
	tab := a.tab()
	left := tab.Focus
	tt.Frame()
	if _, ok := tt.Find("关闭窗格"); ok {
		t.Fatal("single pane has a close control")
	}
	a.split(false)
	top := tab.Focus
	a.split(true)
	bottom := tab.Focus
	tt.Frame()
	r := bottom.bounds
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	button, ok := tt.Find("关闭窗格")
	if !ok || button.Y < r.Y || button.X < r.X {
		t.Fatal("hovered pane has no close control, or another pane has one")
	}
	saveDesktopImage(t, tt, "pane-close-controls.png")
	tt.Move(10, 10)
	tt.Frame()
	if _, ok := tt.Find("关闭窗格"); ok {
		t.Fatal("close control remains when the pointer leaves the panes")
	}
	// Closing an unfocused pane must leave the active session alone and
	// collapse just that branch, even with its search bar open.
	tab.setFocus(top)
	a.openFind()
	tab.setFocus(bottom)
	a.focusReq = bottom
	tt.Frame()
	r = top.bounds
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	button, ok = tt.Find("关闭窗格")
	if !ok || button.Y >= bottom.bounds.Y || button.X < r.X {
		t.Fatal("hover did not move the close control to the unfocused pane")
	}
	saveDesktopImage(t, tt, "pane-close-with-search.png")
	x, y := r.X+r.W-12, r.Y+12
	tt.Press(x, y)
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	if !top.closed || left.closed || bottom.closed || len(tab.panes()) != 2 || tab.Focus != bottom {
		t.Fatal("close control did not close only its own pane")
	}
	if tab.Root.B.Pane != bottom || bottom.Node != tab.Root.B {
		t.Fatal("remaining nested pane did not take over its branch")
	}
	waitFor(t, tt, "closed pane session to end", func() bool {
		infos, err := a.client.List()
		if err != nil {
			t.Fatal(err)
		}
		for _, info := range infos {
			if info.ID == top.terminal().SID && !info.Exited {
				return false
			}
		}
		return true
	})
	// A zoomed pane still belongs to a split tab and can be closed.
	a.toggleZoom()
	tt.Frame()
	r = bottom.bounds
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if err := tt.Click("关闭窗格"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !bottom.closed || tab.Zoom != nil || tab.Focus != left || len(tab.panes()) != 1 {
		t.Fatal("closing the zoomed pane did not restore the surviving pane")
	}
	if _, ok := tt.Find("关闭窗格"); ok {
		t.Fatal("close control remains after returning to one pane")
	}
}

func TestTerminalContextMenuSplit(t *testing.T) {
	for _, direction := range []struct {
		label string
		down  bool
	}{
		{"Split Pane Vertically", false},
		{"Split Pane Horizontally", true},
	} {
		t.Run(direction.label, func(t *testing.T) {
			previous := prefs
			t.Cleanup(func() { prefs = previous })
			a, tt := newTestApp(t)
			tab := a.tab()
			target := tab.Focus
			a.split(false)
			other := tab.Focus
			tt.Frame()
			// Give the unfocused target its own directory. Right-clicking
			// it must split that pane and inherit its real shell directory.
			target.terminal().term.Send([]byte("cd /usr/bin\r"))
			waitFor(t, tt, "target directory", func() bool {
				refresh(a)
				return target.terminal().info.Dir == "/usr/bin"
			})
			tab.setFocus(other)
			a.focusReq = other
			tt.Frame()
			node, otherNode := target.Node, other.Node
			bounds := target.bounds
			tt.RightClickAt(bounds.X+bounds.W/2, bounds.Y+bounds.H/2)
			for _, label := range []string{"Copy", "Paste", "Clear Scrollback", "Split Pane Vertically", "Split Pane Horizontally"} {
				if !slices.Contains(tt.Menu(), label) {
					t.Fatalf("missing %q in menu %q", label, tt.Menu())
				}
			}
			if err := tt.ChooseMenuItem(direction.label); err != nil {
				t.Fatal(err)
			}
			if len(tab.panes()) != 3 || node.Pane != nil || node.Vertical != direction.down || node.A.Pane != target {
				t.Fatalf("wrong split: panes=%d, node=%+v", len(tab.panes()), node)
			}
			added := node.B.Pane
			if added == nil || added == target || added == other || added.terminal().SID == "" || tab.Focus != added {
				t.Fatal("new session did not receive focus")
			}
			if other.Node != otherNode || otherNode.Pane != other {
				t.Fatal("split changed the previously focused pane")
			}
			if added.terminal().startDir != "/usr/bin" {
				t.Fatalf("new pane directory = %q", added.terminal().startDir)
			}
			waitFor(t, tt, "new shell directory", func() bool {
				refresh(a)
				return added.terminal().info.Dir == "/usr/bin"
			})
		})
	}
}

// A native menu can outlive the pane that originally opened it.
func TestContextSplitIgnoresClosedPane(t *testing.T) {
	a, tt := newTestApp(t)
	tab := a.tab()
	closed := tab.Focus
	a.split(false)
	a.closePane(closed)
	tt.Frame()
	a.splitPane(closed, true)
	if len(tab.panes()) != 1 {
		t.Fatal("stale context menu split a surviving pane")
	}
}

// Restarting a pane ends its session as closing it does, and closing the
// new pane gives the focus back to the pane before, never the dead one.
func TestRestartedPaneLeavesFocusHistory(t *testing.T) {
	a, tt := newTestApp(t)
	tab := a.tab()
	left := tab.Focus
	a.split(false)
	old := tab.Focus
	a.restartFocused()
	restarted := tab.Focus
	tt.Frame()
	if restarted == old || !old.closed || !a.closedSessions[old.terminal().SID] || slices.Contains(tab.recent, old) {
		t.Fatal("restart kept the old pane in the tab's focus history or session list")
	}
	a.closePane(restarted)
	tt.Frame()
	if tab.Focus != left || len(tab.panes()) != 1 {
		t.Fatal("closing the restarted pane did not focus the pane before it")
	}
}
