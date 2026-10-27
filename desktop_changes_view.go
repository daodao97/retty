package main

import (
	"github.com/egoist/mygo/ui"
)

func (a *App) desktopContentWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	if prefs.DesktopLayout == "sidebar" && !prefs.SidebarCollapsed {
		if w < 720 {
			w -= 188
		} else {
			w -= 224
		}
	}
	return w
}

func (a *App) fileBaseControl(c *ui.Context, k *colors, b *desktopFiles) {
	if b == nil || !b.diff {
		return
	}
	selected := 1
	if b.base == "turn" {
		selected = 0
	} else if b.base == "branch" {
		selected = 2
	}
	control := ui.SegmentedBase(c.Key("git-base"), &selected, 3)
	control.Track.Label("Git 比较范围").Padding(3).Gap(2).Radius(6).Background(k.hover).Children(func() {
		for i, label := range []string{"本轮", "未提交", "整个分支"} {
			segment := control.Segment(i).Label(label).Height(24).Padding(0, 10).Radius(4).Cursor(ui.CursorPointer)
			if i == selected {
				segment.Background(k.panel)
			}
			segment.Children(func() { ui.Text(c, label).FontSize(10.5).TextColor(k.textMuted) })
		}
	})
	if control.Track.Changed() {
		base := []string{"turn", "head", "branch"}[selected]
		a.later(c, func() { a.setFileBase(b, base) })
	}
}

func (a *App) setFileBase(b *desktopFiles, base string) {
	if !a.filesCurrent(b) || b.base == base || base != "turn" && base != "head" && base != "branch" {
		return
	}
	b.base, prefs.FilesDiffBase = base, base
	a.changed()
	b.turnRetry = 0
	saveSettings()
	a.loadFileDirectory(b, b.cwd)
}

func (a *App) refreshTurnDiff(c *ui.Context) {
	for _, p := range a.allPanes() {
		b := p.files()
		if b == nil || !a.filesCurrent(b) || b.loading || b.turnRetryAt.IsZero() {
			continue
		}
		if wait := b.turnRetryAt.Sub(c.Now()); wait > 0 {
			c.After(wait)
			continue
		}
		b.turnRetry++
		a.loadFileDirectory(b, b.cwd)
	}
}
