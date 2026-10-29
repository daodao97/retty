package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

const (
	usagePanelWidth  = 304
	usagePanelHeader = 50
	usagePanelFooter = 38
)

func (d *desktopUsage) reading(agent string) agentusage.Reading {
	for _, r := range d.readings {
		if r.Agent == agent {
			return r
		}
	}
	return agentusage.Reading{Agent: agent}
}

func (d *desktopUsage) height() int {
	r := d.reading(d.panelAgent)
	if d.panelAgent == "codex" && d.resetOpen {
		return 316
	}
	windows := r.Windows
	if len(windows) == 0 {
		return 184
	}
	height := usagePanelHeader + usagePanelFooter + 24
	for i, w := range windows[:min(len(windows), 3)] {
		if i > 0 {
			height += 20
		}
		height += 39 // Value row, spacing and meter.
		if w.Reset != "" {
			height += 21
		}
	}
	if d.panelAgent == "codex" && (r.Resets != nil || d.resetMessage != "") {
		height += 88
	}
	return height
}

func (a *App) closeUsagePanel() {
	if d := a.usage; d != nil && d.panel != nil {
		d.panel.Close()
	}
}

func (a *App) toggleUsagePanel() {
	d := a.usage
	if d == nil || d.closed || d.tray == nil {
		return
	}
	if d.panel != nil {
		a.closeUsagePanel()
		return
	}
	if d.panelAgent == "" {
		d.panelAgent = prefs.UsageTrayAgent
		if r := selectedUsage(d.readings, prefs.UsageTrayAgent); r != nil {
			d.panelAgent = r.Agent
		}
		if d.panelAgent == "" {
			d.panelAgent = "codex"
		}
	}
	d.panelHeight = d.height()
	panel := mygo.NewFlyout(mygo.FlyoutOptions{
		Tray: d.tray, Placement: mygo.PlacementBottom, Gap: 6,
		Width: usagePanelWidth, Height: d.panelHeight,
		Focusable: true, Shadow: true, Popover: true, Hidden: true,
		Content: ui.View(a.usagePanelView),
	})
	d.panel = panel
	panel.SetTitle("Retty 用量")
	panel.OnClosed(func() {
		if d.panel == panel {
			d.panel = nil
			d.resetOpen = false
		}
	})
	panel.Show()
}

func (a *App) invalidateUsageViews() {
	if a.win != nil {
		a.win.Invalidate()
	}
	if d := a.usage; d != nil && d.panel != nil {
		if height := d.height(); height != d.panelHeight {
			d.panelHeight = height
			d.panel.SetContentSize(usagePanelWidth, height)
		}
		d.panel.Invalidate()
	}
}

func (a *App) openUsageSettingsFromPanel() {
	a.closeUsagePanel()
	if a.win == nil && a.openWindow != nil {
		a.openWindow()
	}
	if a.win != nil {
		a.openUsage()
		a.win.Show()
		a.win.Focus()
		a.win.Invalidate()
		mygo.App.Focus()
	}
}

func (a *App) usagePanelView(c *ui.Context) {
	d := a.usage
	if d == nil {
		return
	}
	original := c.Theme()
	theme := desktopConnectionTheme(original)
	c.SetTheme(&theme)
	defer c.SetTheme(original)
	c.Root().Background(ui.Transparent)
	box := ui.Column(c).Fill().Label("Agent usage panel")
	if !c.Vibrancy() {
		box.Background(theme.Background)
		if runtime.GOOS != "windows" {
			box.Radius(radiusL).Border(1, theme.Border)
		}
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Height(usagePanelHeader).Padding(spaceM, spaceL).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
			agents, selected := []string{"codex", "claude"}, 0
			if d.panelAgent == "claude" {
				selected = 1
			}
			tabs := ui.TabsBase(c, &selected, 2)
			tabs.List.Gap(space2XS).Children(func() {
				for i, agent := range agents {
					tab := tabs.Tab(i).Label("Usage provider "+usageAgentName(agent)).Height(30).Padding(0, spaceM).Gap(spaceS).Radius(radiusM).AlignItems(ui.Center)
					ink := theme.TextMuted
					if i == selected {
						tab.Background(theme.SurfaceHover)
						ink = theme.Text
					} else if tab.Hovered() {
						tab.Background(theme.SurfaceHover)
					}
					tab.Children(func() {
						programIcon(c, "agent:"+agent).Size(14, 14).TextColor(ink)
						ui.Text(c, usageAgentName(agent)).FontSize(textSmall).FontWeight(500).TextColor(ink)
					})
				}
			})
			if tabs.List.Changed() {
				d.panelAgent = agents[selected]
				a.invalidateUsageViews()
			}
			ui.Spacer(c)
			if desktopConnectionIconAction(c, "Usage panel settings", "settings-2").Size(26, 26).Tooltip("用量设置").Clicked() {
				a.openUsageSettingsFromPanel()
			}
		})
		r := d.reading(d.panelAgent)
		ui.Scroll(c.Key("usage-panel-"+d.panelAgent)).FillWidth().Grow(1).MinHeight(0).Padding(spaceM, spaceXL, spaceL, spaceXL).Gap(spaceXL).Children(func() {
			if d.panelAgent == "codex" && d.resetOpen {
				a.usageResetConfirmation(c, r)
				return
			}
			if len(r.Windows) == 0 && r.Resets == nil {
				usagePanelEmpty(c, r, d.busy)
			}
			for _, w := range r.Windows {
				color := colorsOf(c).meter
				ink := theme.Text
				if w.Used >= 80 {
					color = colorsOf(c).attention
					ink = color
				}
				ui.Column(c).FillWidth().Gap(spaceS).Children(func() {
					ui.Row(c).FillWidth().Height(28).AlignItems(ui.Center).Children(func() {
						ui.Text(c, w.Name).FontSize(textSmall).TextColor(theme.TextMuted).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").Tooltip(w.Name)
						ui.Text(c, fmt.Sprintf("%d%%", remainingUsage(w.Used))).Label(fmt.Sprintf("%d%% 剩余", remainingUsage(w.Used))).FontSize(textDisplay).FontWeight(600).TextColor(ink).Shrink(0)
						ui.Text(c, "剩余").FontSize(textCaption).TextColor(theme.TextMuted).Margin(0, 0, 0, spaceXS).Shrink(0)
					})
					usagePanelMeter(c, 100-agentusage.Percent(w.Used), color, 4)
					if w.Reset != "" {
						ui.Text(c, "重置于 "+w.Reset).Height(14).FontSize(textCaption).TextColor(theme.TextMuted).SingleLine().Ellipsis("…").Tooltip("重置于 " + w.Reset)
					}
				})
			}
			if d.panelAgent == "codex" {
				a.usageResetSection(c, r, true)
			}
		})
		usagePanelDivider(c)
		ui.Row(c).FillWidth().Height(usagePanelFooter).Padding(0, spaceXL).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			if r.Plan != "" {
				ui.Text(c, strings.ToUpper(r.Plan)).FontSize(textMicro).FontWeight(600).TextColor(theme.TextMuted).
					Padding(space2XS, spaceS).Radius(radiusS).Background(theme.SurfaceHover).MaxWidth(84).SingleLine().Ellipsis("…").Tooltip(r.Plan)
			}
			ui.Text(c, usagePanelStatus(r, d.busy, c.Now())).FontSize(textCaption).TextColor(theme.TextMuted).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			if desktopConnectionIconAction(c, "Refresh usage panel", "rotate-ccw").Size(26, 26).Tooltip("刷新用量").Disabled(d.busy).Clicked() {
				a.refreshUsage()
			}
		})
	})
}

func usagePanelEmpty(c *ui.Context, r agentusage.Reading, busy bool) {
	theme := c.Theme()
	title, message := "暂无额度数据", "点击右下角刷新，读取本机账户的订阅额度。"
	if r.Error != "" {
		title, message = "无法读取订阅额度", r.Error
	}
	if busy {
		title, message = "正在读取额度", "正在查询 "+usageAgentName(r.Agent)+" 本机账户，请稍候…"
	}
	ui.Column(c).FillWidth().Gap(spaceM).Margin(spaceM, 0).Children(func() {
		ui.Row(c).FillWidth().Gap(spaceS).AlignItems(ui.Center).Children(func() {
			if busy {
				ui.Spinner(c).Size(14, 14).TextColor(theme.TextMuted)
			} else {
				ui.Icon(c, icon("info")).Size(14, 14).TextColor(theme.TextMuted)
			}
			ui.Text(c, title).FontSize(textSmall).FontWeight(500).TextColor(theme.Text)
		})
		ui.Text(c, message).FillWidth().FontSize(textSmall).LineHeight(1.5).TextColor(theme.TextMuted)
	})
}

func usagePanelStatus(r agentusage.Reading, busy bool, now time.Time) string {
	if busy {
		return "正在更新…"
	}
	if r.At.IsZero() {
		return "尚未更新"
	}
	action := "更新"
	if r.Error != "" || len(r.Windows) == 0 {
		action = "检查"
	}
	if now.Sub(r.At) < time.Minute {
		return "刚刚" + action
	}
	format := "15:04"
	if y, m, d := r.At.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		format = "01-02 15:04"
	}
	return r.At.Format(format) + " " + action
}

func usagePanelMeter(c *ui.Context, remaining float64, color ui.Color, height float32) {
	ui.Row(c).FillWidth().Height(height).Radius(height / 2).Clip().Background(c.Theme().Border).Children(func() {
		if remaining = agentusage.Percent(remaining); remaining > 0 {
			ui.Box(c).WidthPercent(float32(remaining)).FillHeight().Radius(height / 2).Background(color)
		}
	})
}

func usagePanelDivider(c *ui.Context) {
	ui.Row(c).FillWidth().Height(0.5).Padding(0, spaceXL).Children(func() {
		ui.Box(c).FillWidth().Height(0.5).Background(c.Theme().Border)
	})
}
