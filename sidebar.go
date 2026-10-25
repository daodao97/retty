package main

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *App) sidebarLayout(c *ui.Context, k *colors, bar ui.TitleBar) {
	width, height := c.Size()
	sideWidth := float32(224)
	if width < 720 {
		sideWidth = 188
	}
	side, border := k.track, k.headerBorder
	if !c.Theme().Dark {
		side, border = ui.Hex("#f0f1f4"), k.panelBorder
	}
	ui.Row(c.Key("sidebar-layout")).Fill().Children(func() {
		if !prefs.SidebarCollapsed {
			ui.Column(c.Key("tab-sidebar")).Label("Tab sidebar").Width(sideWidth).Shrink(0).FillHeight().
				Background(side).BorderWidth(0, 1, 0, 0).BorderColor(border).Children(func() {
				left := float32(0)
				if bar.Left > 0 {
					left = bar.Left + 8
				}
				ui.Row(c).Height(compactTitleH).FillWidth().Padding(0, 8, 0, left).AlignItems(ui.Center).DragWindow().Children(func() {
					ui.Spacer(c)
					a.collapseSidebarButton(c, k)
				})
				tabs := ui.Scroll(c.Key("sidebar-tabs")).Label("Sidebar tabs").Role(ui.RoleTabList).
					MinHeight(0).FillWidth().Padding(10, 8).Gap(4)
				if a.agentHistory != nil && a.agentHistory.expanded {
					tabs.Height(min(float32(len(a.tabs)*44+16), max(96, height*0.4))).Shrink(0)
				} else {
					tabs.Grow(1).Basis(0)
				}
				tabs.Children(func() {
					for i, t := range slices.Clone(a.tabs) {
						a.sidebarTab(c, k, i, t)
					}
				})
				a.sidebarProjectSessions(c, k)
			})
			a.sidebarVisibleTab = a.tab()
		}
		ui.Column(c.Key("sidebar-main")).Grow(1).Basis(0).MinWidth(0).FillHeight().Children(func() {
			a.sidebarTitleBar(c, k, bar)
			a.desktopContent(c, k)
		})
	})
}

// The sidebar opens from the tab bar and closes from itself, so only one
// of the two buttons shows at a time; both use the sidebar symbol, as
// macOS apps do.
func (a *App) collapseSidebarButton(c *ui.Context, k *colors) {
	if iconButton(c.Key("collapse-tab-sidebar"), k, "sidebar-left", "收起侧边栏", 26, 15).
		Focusable().Shrink(0).Tooltip("收起侧边栏  ⌃⌘S").Clicked() {
		a.toggleTabSidebar()
		c.Invalidate()
	}
}

// expandSidebarButton opens the sidebar from either tab bar: in tab mode
// as Command-1 does, and in a collapsed sidebar as Control-Command-S does.
// Only the tab strip's buttons draw its bottom line; the sidebar's title
// bar has its own.
func (a *App) expandSidebarButton(c *ui.Context, k *colors) {
	b, shortcut := compactTitleButton(c.Key("expand-tab-sidebar"), k, "sidebar-left", "展开侧边栏", false), "⌘1"
	if prefs.DesktopLayout == "sidebar" {
		b, shortcut = b.BorderWidth(0), "⌃⌘S"
	}
	if b.Tooltip("展开侧边栏  " + shortcut).Clicked() {
		a.showTabSidebar()
		c.Invalidate()
	}
}

func (a *App) sidebarTitleBar(c *ui.Context, k *colors, bar ui.TitleBar) {
	left := float32(8)
	if prefs.SidebarCollapsed && bar.Left > 0 {
		left = bar.Left + 8
	}
	ui.Row(c).Height(compactTitleH).FillWidth().Padding(0, bar.Right, 0, left).Gap(6).
		Background(k.track).BorderWidth(0, 0, 1, 0).BorderColor(k.headerBorder).AlignItems(ui.Center).DragWindow().Children(func() {
		if prefs.SidebarCollapsed {
			a.expandSidebarButton(c, k)
		}
		ui.Row(c).Grow(1).MinWidth(0).Children(func() {
			if t := a.tab(); t != nil {
				name, detail := t.label()
				if prefs.SidebarCollapsed && a.renaming == t {
					a.renameField(c, t, name)
				} else {
					ui.Text(c, strings.TrimSpace(name+" "+detail)).Label("Current session").FontSize(11.5).
						TextColor(k.textMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				}
			}
		})
		if compactTitleButton(c.Key("sidebar-new-tab"), k, "plus", "New Tab", false).BorderWidth(0).Tooltip("New Tab  ⌘T").Clicked() {
			a.newTab(a.currentDir())
		}
		a.filesToggle(c, k)
		if compactTitleButton(c.Key("desktop-connections"), k, "plug-connected", "连接", a.hasDesktopConnections()).BorderWidth(0).Tooltip("连接").Clicked() {
			a.showDesktopConnections(nil)
		}
	})
}

func (a *App) sidebarTab(c *ui.Context, k *colors, i int, t *Tab) {
	active := i == a.active
	name, detail := t.label()
	row := ui.Row(c.Key(t.ID)).Label(strings.TrimSpace(name+" "+detail)).Role(ui.RoleTab).Selected(active).
		FillWidth().Height(40).Shrink(0).Padding(0, 9).Gap(8).Radius(7).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	if active {
		row.Background(k.panelSel)
		if a.sidebarVisibleTab != t {
			row.ScrollIntoView()
		}
	} else if row.Hovered() {
		row.Background(k.hover)
	}
	row.Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
	if row.Clicked() {
		a.selectTab(i)
	}
	a.bindTabItem(k, row, i, t)
	if p, state := tabAgentState(t); p != nil {
		row.Tooltip(programOf(state.ID).Name + " · " + agentStateLabel(state))
	}
	row.Children(func() {
		glyph, color := "square-terminal", k.iconMuted
		if t.Host != nil {
			glyph = "monitor"
		} else if t.Focus != nil {
			prog := paneProgram(t.Focus)
			glyph, color = prog.Glyph, programIconColor(c, prog)
			if prog.Shell || t.Focus.info.Program == "" {
				color = k.iconMuted
			}
		}
		programIcon(c, glyph).Size(17, 17).TextColor(color).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			if a.renaming == t {
				a.renameField(c, t, name)
			} else {
				ui.Text(c, name).FontSize(12).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…").FillWidth()
			}
			meta := detail
			if git := a.paneGit(t.Focus); git != nil && git.Branch != "" && git.Branch != git.Default {
				if meta != "" {
					meta += " · "
				}
				meta += git.Branch
			}
			if t.Host != nil {
				meta = t.Host.name()
				if detail != "" {
					meta += " · " + detail
				}
			}
			if meta != "" {
				ui.Text(c, meta).FontSize(10.5).TextColor(k.textFaint).SingleLine().Ellipsis("…").FillWidth()
			}
		})
		ui.Box(c.Key("tab-action")).Size(18, 18).Shrink(0).Center().Children(func() {
			if row.Hovered() && len(a.tabs) > 1 {
				if iconButton(c.Key("close-tab"), k, "x", "Close Tab", 18, 11).Shrink(0).Tooltip("Close Tab").Clicked() {
					a.later(c, func() { a.closeTab(t) })
				}
			} else if p, state := tabAgentState(t); p != nil {
				a.agentIndicator(c, k, p, state)
			} else if t.attention() {
				ui.Box(c).Size(5, 5).Radius(3).Background(k.attention).Shrink(0)
			} else if a.tabBusy(c, t) {
				ui.Box(c).Size(5, 5).Radius(3).Background(k.busy).Shrink(0)
			}
		})
	})
}
