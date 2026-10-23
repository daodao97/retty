package main

import (
	"fmt"
	"path/filepath"

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
	b.turnRetry = 0
	saveSettings()
	a.loadFileDirectory(b, b.cwd)
}

func (a *App) refreshTurnDiff(c *ui.Context) {
	b := a.files
	if b == nil || !a.filesCurrent(b) || b.loading || b.turnRetryAt.IsZero() {
		return
	}
	if wait := b.turnRetryAt.Sub(c.Now()); wait > 0 {
		c.After(wait)
		return
	}
	b.turnRetry++
	a.loadFileDirectory(b, b.cwd)
}

// A compact review panel leaves the terminal beside the selected immutable
// diff. Narrow windows show the panel alone; closing it restores the terminal.
func (a *App) changesSidePanel(c *ui.Context, k *colors) ui.Element {
	width := min(float32(520), max(float32(300), a.desktopContentWidth(c)-320))
	panel := ui.Column(c.Key("changes-side-panel")).Label("会话变更面板").Width(width).Shrink(0).FillHeight().MinHeight(0).
		Background(k.panel).BorderWidth(0, 0, 0, 1).BorderColor(k.headerBorder)
	panel.Children(func() {
		b := a.files
		ui.Row(c).FillWidth().Height(42).Padding(0, 10).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "变更").FontSize(12).FontWeight(600).TextColor(k.textMuted)
			if b != nil {
				ui.Text(c, filepath.Base(b.cwd)).FontSize(10.5).TextColor(k.textFaint).SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Tooltip(b.cwd)
			} else {
				ui.Spacer(c)
			}
			if iconButton(c, k, "rotate-ccw", "刷新变更", 24, 12).Disabled(b == nil || b.loading).Clicked() {
				a.later(c, func() { b.turnRetry = 0; a.loadFileDirectory(b, b.cwd) })
			}
			if iconButton(c, k, "x", "关闭变更面板", 24, 12).Clicked() {
				a.later(c, a.toggleFiles)
			}
		})
		if b == nil {
			a.fileMessage(c, k, "选择一个窗格以查看变更")
			return
		}
		ui.Row(c).FillWidth().Padding(0, 10, 8, 10).Children(func() { a.fileBaseControl(c, k, b) })
		if b.loading {
			ui.Box(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(18, 18) })
			return
		}
		if b.err != "" && b.preview == "" {
			a.fileMessage(c, k, b.err)
		}
		if b.truncated {
			a.fileMessage(c, k, "部分结果：过大文件、超出范围的文件未包含在比较中")
		}
		if len(b.entries) == 0 && b.err == "" {
			a.fileMessage(c, k, "此范围没有变更")
		}
		b.list.Key = func(i int) any { return b.entries[i].Name }
		ui.List(c.Key("changes-files"), &b.list, len(b.entries), func(i int) {
			e := b.entries[i]
			button := ui.ButtonBase(c.Key(e.Name)).Label("查看变更 "+e.Name).FillWidth().Height(29).Padding(0, 12).Gap(8).AlignItems(ui.Center).Cursor(ui.CursorPointer)
			path := filepath.Join(b.dir, e.Name)
			if b.preview == path || button.Hovered() {
				button.Background(k.hover)
			}
			button.Children(func() {
				ui.Text(c, e.Status).FontSize(10).TextColor(k.busy).Width(16).Shrink(0)
				ui.Text(c, e.Name).FontSize(11).TextColor(k.textMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Tooltip(e.Name)
			})
			if button.Clicked() {
				a.later(c, func() { a.previewFile(b, path, true, 256<<10) })
			}
		}).Label("变更文件列表").Height(min(float32(len(b.entries)*29), float32(145))).Shrink(0).FillWidth()
		ui.Column(c.Key("changes-preview")).Grow(1).MinHeight(0).Clip().Children(func() {
			if b.preview == "" {
				ui.Box(c).Grow(1).Center().Children(func() { ui.Text(c, "选择文件查看只读 Diff").FontSize(11).TextColor(k.textFaint) })
				return
			}
			a.filePreviewView(c, k, b)
		})
		ui.Row(c).Height(32).Padding(0, 12).Gap(8).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(k.headerBorder).Children(func() {
			ui.Text(c, fmt.Sprintf("%d 个变更文件", len(b.entries))).FontSize(10).TextColor(k.textFaint).Grow(1)
			button := ui.ButtonBase(c.Key("full-file-workspace")).Label("打开文件工作台").Height(25).Padding(0, 6).Cursor(ui.CursorPointer)
			button.Children(func() { ui.Text(c, "文件工作台").FontSize(10).TextColor(k.textMuted) })
			if button.Clicked() {
				a.later(c, func() { prefs.FilesSidePanel = false; saveSettings() })
			}
		})
	})
	return panel
}
