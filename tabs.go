package main

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// tabStrip divides the title bar evenly between tabs and lets its gaps drag the window.
func (a *App) tabStrip(c *ui.Context, k *colors) {
	track := ui.Row(c.Key("tabs")).Grow(1).Basis(0).MinWidth(0).Height(compactTitleH).
		Background(k.track).AlignItems(ui.Center).ClipX().DragWindow().Role(ui.RoleTabList).Label("Tabs")
	track.Children(func() {
		for i, t := range slices.Clone(a.tabs) {
			a.tabItem(c, k, i, t)
		}
	})
}

// tabItem draws a flat tab with its label, agent status and shortcut.
func (a *App) tabItem(c *ui.Context, k *colors, i int, t *Tab) {
	active := i == a.active
	bottom := float32(1)
	bg := k.track
	e := ui.Row(c.Key(t.ID)).Grow(1).Basis(0).MinWidth(0).Height(compactTitleH).
		Padding(0, 8).Gap(6).AlignItems(ui.Center).Role(ui.RoleTab).Selected(active)
	name, detail := t.label()
	e.Label(name + " " + detail)
	if agentPane, agentState := tabAgentState(t); agentPane != nil {
		e.Tooltip(programOf(agentState.ID).Name + " · " + agentStateLabel(agentState))
	}
	if active {
		bottom, bg = 0, terminalBackground(c)
	} else if e.Hovered() {
		bg = k.hover
	}
	e.Background(bg).BorderWidth(0, 1, bottom, 0).BorderColor(k.headerBorder)
	e.Transition(ui.ElementTransition{Colors: true, Position: true, Duration: 160 * time.Millisecond})
	if e.Clicked() && !active {
		a.selectTab(i)
	}
	a.bindTabItem(k, e, i, t)
	hovered := e.Hovered()
	e.Children(func() { a.compactTabLabel(c, k, i, t, name, detail, hovered) })
}

// Both layouts use the same Tab objects and actions.
func (a *App) bindTabItem(k *colors, e ui.Element, i int, t *Tab) {
	e.Drag(t)
	if dragged, ok := ui.Drop[*Tab](e); ok && dragged != t {
		a.moveTab(dragged, i)
	}
	if _, over := ui.DragOver[*Tab](e); over {
		e.Border(1.5, k.busy.Alpha(0.6))
	}
	if e.Dragging() {
		e.Opacity(0.4)
	}
	if e.DoubleClicked() {
		a.selectTab(i)
		a.startRename()
	}
	e.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, t) })
}

// renameField edits the name of a tab in place: Enter or moving the focus
// away keeps it, Escape goes back, and an empty name follows the panes
// again.
func (a *App) renameField(c *ui.Context, t *Tab, name string) {
	in := ui.TextInput(c, &a.renameText).Placeholder(name).Grow(1).MinWidth(0).Height(22).FontSize(13).AutoFocus()
	done := func(keep bool) {
		if keep {
			t.Name = strings.TrimSpace(a.renameText)
			a.changed()
		}
		a.renaming, a.renameFocus = nil, false
		a.focusReq = t.Focus
	}
	switch {
	case in.Shortcut(0, ui.KeyEscape):
		done(false)
	case in.Submitted():
		done(true)
	case in.Focused():
		a.renameFocus = true
	case a.renameFocus:
		done(true) // the focus went elsewhere
	}
}

func (t *Tab) attention() bool {
	for _, p := range t.panes() {
		if p.attention {
			return true
		}
	}
	return false
}

func (a *App) tabBusy(c *ui.Context, t *Tab) bool {
	return slices.ContainsFunc(t.panes(), func(p *Pane) bool { return paneRunning(c, p) })
}

func (a *App) moveTab(t *Tab, to int) {
	from := slices.Index(a.tabs, t)
	if from < 0 || to == from {
		return
	}
	activeTab := a.tab()
	a.tabs = slices.Delete(a.tabs, from, from+1)
	a.tabs = slices.Insert(a.tabs, min(to, len(a.tabs)), t)
	a.active = slices.Index(a.tabs, activeTab)
	a.changed()
}

func (a *App) tabMenu(m *ui.Menu, t *Tab) {
	if m.Item("Rename Tab…").Chosen() {
		a.selectTab(slices.Index(a.tabs, t))
		a.startRename()
	}
	if t.Name != "" && m.Item("Reset Name").Chosen() {
		t.Name = ""
		a.changed()
	}
	m.Separator()
	i := slices.Index(a.tabs, t)
	if m.Item("Move Left").Disabled(i <= 0).Chosen() {
		a.moveTab(t, i-1)
	}
	if m.Item("Move Right").Disabled(i >= len(a.tabs)-1).Chosen() {
		a.moveTab(t, i+1)
	}
	m.Separator()
	if m.Item("New Tab").Shortcut(ui.Cmd, ui.KeyT).Chosen() {
		a.selectTab(slices.Index(a.tabs, t))
		a.newTab(a.currentDir())
	}
	if m.Item("Close Tab").Chosen() {
		a.laterFrom(a.services, func() { a.closeTab(t) })
	}
	if m.Item("Close Other Tabs").Disabled(len(a.tabs) < 2).Chosen() {
		a.laterFrom(a.services, func() {
			for _, o := range slices.Clone(a.tabs) {
				if o != t {
					a.closeTab(o)
				}
			}
		})
	}
}

func paneProgram(p *Pane) program {
	if p.browser() != nil {
		return program{Name: "浏览器", Glyph: "globe", IconColor: ui.Hex("#7ca8ef")}
	}
	if b := p.files(); b != nil {
		if b.diff {
			return program{Name: "Diff", Glyph: "diff", IconColor: ui.Hex("#82b99c")}
		}
		return program{Name: "文件", Glyph: "folder-open", IconColor: ui.Hex("#b69c71")}
	}
	if p.terminal() == nil {
		return program{Name: "窗格", Glyph: "layout-grid"}
	}
	return programOf(sessionProgramName(p.terminal().info))
}
