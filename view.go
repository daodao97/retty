package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	compactTitleH = 32
	compactGap    = 1.0 // visible separator; its pointer target is wider
	activeFor     = 1500 * time.Millisecond
)

func (a *App) view(c *ui.Context) {
	a.services = c.Services()
	a.runPosted()
	a.syncBrowsers()
	a.syncGitStatus(c)
	a.syncFiles()
	a.cancelHiddenImagePreviews(a.desktopContentWidth(c) < 720)
	a.refreshTurnDiff(c)
	k := colorsOf(c)
	a.focusedWin = a.win == nil || a.win.IsFocused()
	a.syncRemotePaneActivity()
	a.closeViewedPaneNotice()
	c.Root().Background(terminalBackground(c))
	ui.Column(c.Key("desktop-layout")).Fill().Children(func() {
		if prefs.DesktopLayout == "sidebar" {
			a.sidebarLayout(c, k, c.TitleBar())
		} else {
			a.compactTitleBar(c, k, c.TitleBar())
			a.desktopContent(c, k)
		}
	})
	a.syncCommandItems()
	a.saveFileWidth(c)
	if a.err != "" {
		a.errorBanner(c, k)
	}
	a.shortcuts(c)
	a.palette(c, k)
	a.settingsPage(c, k)
	if !a.settingsOpen && a.diagnostics != nil && a.diagnostics.busy {
		a.diagnostics.cancel()
		a.diagnostics.epoch++
		a.diagnostics.busy = false
	}
	a.desktopConnectionDialog(c, k)
	a.expandedFilePreview(c, k)
	a.fileDirectoryDialog(c, k)
	a.terminalHistory.view(c)
	a.remoteUpdateView(c, k)
	if a.saveDue && time.Since(a.lastSave) > time.Second {
		a.save()
	}
	// The window's title, which the Window menu and Mission Control show,
	// is the active tab's.
	if t := a.tab(); t != nil && a.win != nil {
		name, detail := t.label()
		if title := strings.TrimSpace(name + " " + detail); title != a.title {
			a.title = title
			a.win.SetTitle(title)
		}
	}
}

func (a *App) desktopContent(c *ui.Context, k *colors) {
	ui.Row(c.Key("desktop-content")).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
		ui.Column(c.Key("pane-content")).Grow(1).Basis(0).FillHeight().MinWidth(0).MinHeight(0).Children(func() {
			if t := a.tab(); t != nil {
				a.tabContent(c, k, t)
			}
		})
	})
}

// iconButton is a borderless button of an icon, with a face on hover;
// label names it for screen readers and its tooltip.
func iconButton(c *ui.Context, k *colors, name, label string, size, iconSize float32) ui.Element {
	b := ui.ButtonBase(c).Size(size, size).Center().Radius(size / 2.6).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(label)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() || b.FocusVisible() {
		b.Background(k.hover)
	}
	b.Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
	col := k.iconMuted
	if b.Hovered() || b.FocusVisible() {
		col = k.text
	}
	b.Children(func() {
		ui.Icon(c, icon(name)).Size(iconSize, iconSize).TextColor(col)
	})
	return b
}

// Keep toolbar actions clear of the hover control without shifting on hover.
func paneActionInset(p *Pane) float32 {
	if p.Tab != nil && p.Tab.Root != nil && p.Tab.Root.Pane == nil {
		return 32
	}
	return 0
}

func (a *App) paneCloseButton(c *ui.Context, k *colors, p *Pane) {
	b := ui.ButtonBase(c.Key("close-pane")).Size(24, 24).Absolute().Top(0).Right(0).
		Radius(12).Cursor(ui.CursorPointer).Role(ui.RoleButton).Label("关闭窗格").Tooltip("关闭窗格")
	col := k.iconMuted
	if b.Pressed() {
		b.Background(k.pressed)
		col = k.text
	} else if b.Hovered() || b.Focused() {
		b.Background(k.hover)
		col = k.text
	}
	b.Children(func() {
		ui.Box(c).Size(12, 12).Radius(6).Border(1, col).Center().PassThrough().Children(func() {
			ui.Icon(c, icon("x")).Size(8, 8).TextColor(col).PassThrough()
		})
	})
	if b.Clicked() {
		a.later(c, func() { a.closePane(p) })
	}
}

// tabContent lays out the panes of a tab, or the one zoomed.
func (a *App) tabContent(c *ui.Context, k *colors, t *Tab) {
	if t.Zoom == nil && t.Focus.files() != nil && a.desktopContentWidth(c) < 720 {
		a.paneCard(c, k, t, t.Focus).Grow(1)
		return
	}
	if t.Zoom != nil {
		a.paneCard(c, k, t, t.Zoom).Grow(1)
		return
	}
	content := a.node(c, k, t, t.Root).Grow(1)
	if t.Root.Pane == nil {
		// Paint after every pane has recorded its current bounds, outside
		// the cards' clips, so the outline reaches the divider centerlines.
		content.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if t.Focus != nil {
				paintCompactFocus(p, r, t.Focus.bounds, k.cardBorderFocused)
			}
		})
	}
}

// node lays out a node of the tree of splits.
func (a *App) node(c *ui.Context, k *colors, t *Tab, n *Node) ui.Element {
	if n.Pane != nil {
		return a.paneCard(c, k, t, n.Pane)
	}
	// A child can change the tree while it builds (for example, splitting).
	first, second := n.A, n.B
	percent := n.Ratio * 100
	box := desktopSplit(c.Key(n.ID), k, &percent, n.Vertical, true, func() {
		n.Ratio = 0.5
		a.changed()
	}, func() ui.Element {
		return a.node(c, k, t, first)
	}, func() {
		a.node(c, k, t, second).Fill()
	})
	if box.Changed() {
		n.Ratio = percent / 100
		a.changed()
	}
	return box
}

// paneCard draws a container and delegates its body to the content.
func (a *App) paneCard(c *ui.Context, k *colors, t *Tab, p *Pane) ui.Element {
	card := ui.Column(c.Key(p.ID)).Clip().MinWidth(0).MinHeight(0).Background(terminalBackground(c))
	card.Transition(ui.ElementTransition{Colors: true, Duration: 160 * time.Millisecond})
	card.Children(func() {
		if p.content != nil {
			p.content.render(a, c, k, p)
		}

		if card.Hovered() && t.Root.Pane == nil && p.browser() == nil && p.files() == nil {
			a.paneCloseButton(c, k, p)
		}
	})
	// Bounds during view building are from the previous layout. Record
	// this frame's rectangle so focus navigation works just after unzoom.
	card.Draw(func(_ *ui.Painter, r ui.Rect) {
		p.bounds = r
	})
	if t.Focus != p && (card.Pressed() || card.FocusWithin()) {
		t.setFocus(p)
		if !card.FocusWithin() {
			a.focusReq = p
		}
		a.changed()
	}
	a.syncRemotePaneActivity()
	return card
}

// paneRunning tells that a pane's agent is at work, or that its program
// printed just now, rather than waiting, done or running quietly.
func paneRunning(c *ui.Context, p *Pane) bool {
	if p.terminal() == nil {
		return false
	}
	if s := paneAgentState(p); s.State != "" {
		return s.State == "running"
	}
	if p.attention || p.terminal().info.Idle || p.terminal().info.Program == "" || programOf(p.terminal().info.Program).Shell {
		return false
	}
	last := time.Unix(0, p.terminal().lastData.Load())
	if since := c.Now().Sub(last); since < activeFor {
		c.After(activeFor - since)
		return true
	}
	return false
}

// errorBanner shows what went wrong with the server.
func (a *App) errorBanner(c *ui.Context, k *colors) {
	ui.Overlay(c, func() {
		ui.Row(c).Absolute().Bottom(18).Left(0).Right(0).Justify(ui.Center).PassThrough().Children(func() {
			ui.Row(c).Gap(10).Padding(8, 10, 8, 14).Radius(12).Background(k.panel).Border(1, k.panelBorder).
				Shadow(0, 8, 24, 0, k.shadowFocused).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 8).Radius(4).Background(ui.Hex("#ef4444"))
				ui.Text(c, a.err).FontSize(12.5).TextColor(k.text).MaxWidth(520)
				if iconButton(c, k, "x", "Dismiss", 22, 14).Clicked() {
					a.err = ""
				}
			})
		})
	})
}

// Fast attachments do not flash a loading banner over a retained screen.
func (a *App) remotePaneStatus(c *ui.Context, p *Pane) string {
	if p.terminal().remoteYielded {
		return "此会话已在其他窗口打开"
	}
	if a.activeRemote != p {
		return ""
	}
	if p.terminal().remoteView.readReady() {
		return screenFreshness(c, p.terminal().remoteView)
	}
	if p.terminal().remoteView.hasTransport() {
		remaining := remoteLoadingDelay - c.Now().Sub(p.terminal().remoteLoadingSince)
		if remaining > 0 {
			c.After(remaining)
			return ""
		}
		return "正在载入会话…"
	}
	return ""
}
