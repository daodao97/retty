package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// paintCompactFocus shares the separators' centerlines. At window edges
// the one-point outline stays inside the content instead of being clipped.
func paintCompactFocus(p *ui.Painter, content, pane ui.Rect, color ui.Color) {
	if pane.W <= 0 || pane.H <= 0 || content.W < 1 || content.H < 1 {
		return
	}
	halfGap := float32(compactGap) / 2
	x0 := max(content.X, pane.X-halfGap-0.5)
	y0 := max(content.Y, pane.Y-halfGap-0.5)
	x1 := min(content.X+content.W-1, pane.X+pane.W+halfGap-0.5)
	y1 := min(content.Y+content.H-1, pane.Y+pane.H+halfGap-0.5)
	p.Fill(ui.Rect{X: x0, Y: y0, W: x1 - x0 + 1, H: 1}, color, 0)
	p.Fill(ui.Rect{X: x0, Y: y1, W: x1 - x0 + 1, H: 1}, color, 0)
	p.Fill(ui.Rect{X: x0, Y: y0, W: 1, H: y1 - y0 + 1}, color, 0)
	p.Fill(ui.Rect{X: x1, Y: y0, W: 1, H: y1 - y0 + 1}, color, 0)
}

// compactTitleBar uses a flat tab strip across the space beside the native
// window controls. Menus and keyboard shortcuts remain available.
func (a *App) compactTitleBar(c *ui.Context, k *colors, bar ui.TitleBar) {
	left := float32(0)
	if bar.Left > 0 {
		left = bar.Left + 8
	}
	ui.Row(c).Height(compactTitleH).Padding(0, bar.Right, 0, left).Gap(0).
		Background(k.track).AlignItems(ui.Center).DragWindow().Children(func() {
		a.expandSidebarButton(c, k)
		a.tabStrip(c, k)
		b := compactTitleButton(c.Key("compact-new-tab"), k, "plus", "New Tab", false).Tooltip("New Tab  ⌘T")
		if b.Clicked() {
			a.newTab(a.currentDir())
		}
		a.automationsToggle(c, k)
		a.filesToggle(c, k)
		a.browserButton(c, k)
		if compactTitleButton(c.Key("desktop-connections"), k, "plug-connected", "连接", a.hasDesktopConnections()).Tooltip("连接").Clicked() {
			a.showDesktopConnections(nil)
		}
	})
}

func compactTitleButton(c *ui.Context, k *colors, name, label string, highlighted bool) ui.Element {
	b := ui.ButtonBase(c).Width(28).FillHeight().Shrink(0).
		Center().Role(ui.RoleButton).Label(label).Focusable().Cursor(ui.CursorPointer).
		BorderWidth(0, 0, 1, 0).BorderColor(k.headerBorder)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() || b.FocusVisible() {
		b.Background(k.hover)
	}
	col := k.iconMuted
	if highlighted {
		col = k.busy
	}
	b.Children(func() { ui.Icon(c, icon(name)).Size(14, 14).TextColor(col) })
	return b
}

func (a *App) compactTabLabel(c *ui.Context, k *colors, i int, t *Tab, name, detail string, hovered bool) {
	if t.Host != nil {
		ui.Icon(c, icon("monitor")).Size(12, 12).Shrink(0).TextColor(k.iconMuted).Tooltip(t.Host.name()).Label("远端电脑 " + t.Host.name())
		ui.Text(c, t.Host.name()).MaxWidth(82).MinWidth(0).FontSize(10).TextColor(k.textFaint).SingleLine().Ellipsis("…").Shrink(1)
	}
	if t.Focus != nil {
		if prog := paneProgram(t.Focus); prog.Agent {
			programIcon(c, prog.Glyph).Size(15, 15).TextColor(programIconColor(c, prog)).
				Shrink(0).Role(ui.RoleImage).Label(prog.Name + " icon").Tooltip(prog.Name)
		}
	}
	if a.renaming == t {
		a.renameField(c, t, name)
		return
	}
	col := k.textMuted
	if i == a.active {
		col = k.text
	}
	ui.Text(c, strings.TrimSpace(name+" "+detail)).Grow(1).MinWidth(0).
		FontSize(11.5).FontWeight(500).TextColor(col).SingleLine().Ellipsis("…")
	if p, s := tabAgentState(t); p != nil {
		a.agentIndicator(c, k, p, s)
	} else if t.attention() {
		ui.Box(c).Size(6, 6).Radius(3).Background(k.attention).Shrink(0)
	} else if a.tabBusy(c, t) {
		ui.Box(c).Size(6, 6).Radius(3).Background(k.busy).Shrink(0)
	}
	if hovered && len(a.tabs) > 1 {
		if iconButton(c, k, "x", "Close Tab", 16, 11).Shrink(0).Tooltip("Close Tab").Clicked() {
			a.later(c, func() { a.closeTab(t) })
		}
	}
}
