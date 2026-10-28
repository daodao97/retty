package main

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

const usageSettingsSection = 5

type desktopUsage struct {
	readings     []agentusage.Reading
	busy         bool
	closed       bool
	cancel       context.CancelFunc
	timer        *time.Timer
	tray         *mygo.Tray
	trayKey      string
	panel        *mygo.Window
	panelAgent   string
	panelHeight  int
	resetOpen    bool
	resetAttempt *agentusage.ResetAttempt
	resetMessage string
	resetting    bool
	resetRetry   bool
	resetReading agentusage.Reading
}

func (a *App) startUsage() {
	if a.usage != nil {
		return
	}
	a.usage = &desktopUsage{}
	a.syncUsageTray()
	a.refreshUsage()
}

func (a *App) stopUsage() {
	d := a.usage
	if d == nil {
		return
	}
	d.closed = true
	a.closeUsagePanel()
	if d.cancel != nil {
		d.cancel()
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	if d.tray != nil {
		d.tray.Destroy()
		d.tray = nil
	}
}

func (a *App) refreshUsage() {
	d := a.usage
	if d == nil || d.busy || d.closed {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.busy = true
	a.invalidateUsageViews()
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() {
		defer cancel()
		readings := make([]agentusage.Reading, 2)
		var wg sync.WaitGroup
		for i, agent := range []string{"codex", "claude"} {
			wg.Go(func() { readings[i] = agentusage.Read(ctx, agent) })
		}
		wg.Wait()
		mygo.RunOnMain(func() {
			if d.closed {
				return
			}
			d.busy, d.readings = false, readings
			a.syncUsageTray()
			a.invalidateUsageViews()
			d.timer = time.AfterFunc(3*time.Minute, func() { mygo.RunOnMain(a.refreshUsage) })
		})
	}()
}

func (a *App) openUsage() {
	a.openSettings()
	a.settingsSection, a.settingsModifiedOnly = usageSettingsSection, false
}

func (a *App) setUsageTray(hidden bool, agent string) {
	prefs.HideUsageTray, prefs.UsageTrayAgent = hidden, agent
	saveSettings()
	a.syncUsageTray()
}

func (a *App) setUsageTrayAgentIcon(enabled bool) {
	prefs.UsageTrayAgentIcon = enabled
	saveSettings()
	a.syncUsageTray()
}

func usageAgentName(agent string) string {
	if agent == "codex" {
		return "Codex"
	}
	return "Claude"
}

func (a *App) usagePage(c *ui.Context, theme *ui.Theme) {
	d := a.usage
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Text(c, "本机订阅额度 · 每 3 分钟更新").FontSize(12).TextColor(theme.TextMuted).Grow(1)
		label := "刷新"
		if d != nil && d.busy {
			label = "读取中…"
		}
		if ui.Button(c, label).Label("Refresh agent usage").Disabled(d == nil || d.busy).Clicked() {
			a.refreshUsage()
		}
	})
	ui.Text(c, "已用比例；账户与本机 CLI 一致。").FontSize(11).TextColor(theme.TextMuted).Margin(6, 0, 20, 0)
	for _, agent := range []string{"codex", "claude"} {
		r := agentusage.Reading{Agent: agent}
		if d != nil {
			for _, reading := range d.readings {
				if reading.Agent == agent {
					r = reading
				}
			}
		}
		ui.Column(c.Key("usage-"+agent)).FillWidth().Padding(18).Gap(12).Radius(8).
			Border(1, theme.Border).Margin(0, 0, 12, 0).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, usageAgentName(agent)).FontSize(14).FontWeight(600).TextColor(theme.Text).Grow(1)
				if r.Plan != "" {
					ui.Text(c, r.Plan).FontSize(11).TextColor(theme.TextMuted)
				}
			})
			if len(r.Windows) == 0 && r.Resets == nil {
				message := r.Error
				if message == "" {
					message = "正在读取…"
				}
				ui.Text(c, message).FontSize(12).TextColor(theme.TextMuted)
			}
			for _, w := range r.Windows {
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Row(c).FillWidth().Children(func() {
						ui.Text(c, w.Name).FontSize(12).TextColor(theme.Text).Grow(1)
						ui.Text(c, fmt.Sprintf("%.0f%% 已用", agentusage.Percent(w.Used))).FontSize(12).TextColor(theme.Text)
					})
					ui.Row(c).FillWidth().Height(4).Radius(2).Clip().Background(theme.Border).Children(func() {
						if used := agentusage.Percent(w.Used); used > 0 {
							ui.Box(c).WidthPercent(float32(used)).FillHeight().Radius(2).Background(theme.Accent)
						}
					})
					if w.Reset != "" {
						ui.Text(c, "重置 "+w.Reset).FontSize(11).TextColor(theme.TextMuted)
					}
				})
			}
			if agent == "codex" {
				a.usageResetSection(c, r, false)
			}
			if !r.At.IsZero() {
				ui.Text(c, "更新于 "+r.At.Format("15:04")).FontSize(10.5).TextColor(theme.TextMuted)
			}
		})
	}
	ui.Box(c).FillWidth().Height(1).Background(theme.Border).Margin(12, 0)
	for _, item := range a.preferenceItems() {
		if item.id != "usage-tray" && item.id != "usage-agent" && item.id != "usage-icon" {
			continue
		}
		ui.Row(c).FillWidth().Padding(10, 0).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, item.title).FontSize(12).TextColor(theme.Text).Grow(1)
			item.control(c)
		})
	}
}

func selectedUsage(readings []agentusage.Reading, agent string) *agentusage.Reading {
	for i := range readings {
		r := &readings[i]
		if (agent == "" || r.Agent == agent) && r.Error == "" && len(r.Windows) > 0 {
			return r
		}
	}
	return nil
}

func usageTrayTip(r *agentusage.Reading) string {
	if r == nil {
		return "Retty · 订阅用量暂不可用\n在设置中查看详情"
	}
	lines := []string{"Retty · " + usageAgentName(r.Agent)}
	for _, w := range r.Windows {
		line := fmt.Sprintf("%s · %d%% 剩余", w.Name, remainingUsage(w.Used))
		if w.Reset != "" {
			line += " · 重置 " + w.Reset
		}
		lines = append(lines, line)
	}
	if r.Agent == "codex" && r.Resets != nil {
		lines = append(lines, fmt.Sprintf("%d 张重置卡", r.Resets.Count))
	}
	lines = append(lines, "更新于 "+r.At.Format("15:04"))
	return strings.Join(lines, "\n")
}

func remainingUsage(used float64) int {
	return int(math.Round(100 - agentusage.Percent(used)))
}

func (a *App) syncUsageTray() {
	d := a.usage
	if d == nil || d.closed || !mygo.App.IsReady() {
		return
	}
	if prefs.HideUsageTray {
		a.closeUsagePanel()
		if d.tray != nil {
			d.tray.Destroy()
			d.tray = nil
			d.trayKey = ""
		}
		return
	}
	reading := selectedUsage(d.readings, prefs.UsageTrayAgent)
	tip := usageTrayTip(reading)
	iconAgent := usageTrayIconAgent(reading, prefs.UsageTrayAgent, prefs.UsageTrayAgentIcon)
	key := iconAgent + "\x00" + tip
	if d.tray != nil && d.trayKey == key {
		return
	}
	png := usageTrayIconFor(iconAgent)
	title := usageTrayTitle(reading)
	if d.tray == nil {
		tray, err := mygo.NewTray(mygo.TrayOptions{ID: "retty.usage", Icon: png, IconIsTemplate: true, Title: title, ToolTip: tip})
		if err != nil {
			return
		}
		d.tray = tray
		menu := mygo.NewMenu([]*mygo.MenuItem{
			{Label: "用量概览…", Click: func(*mygo.MenuItem, *mygo.Window) { a.toggleUsagePanel() }},
			{Label: "用量设置…", Click: func(*mygo.MenuItem, *mygo.Window) { a.openUsageSettingsFromPanel() }},
			mygo.Separator(),
			{Label: "退出 Retty", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
		})
		if runtime.GOOS == "linux" {
			tray.SetMenu(menu)
		} else {
			tray.OnClick(a.toggleUsagePanel)
			tray.OnRightClick(func() {
				a.closeUsagePanel()
				tray.SetMenu(menu)
				defer tray.SetMenu(nil)
				tray.PopUpMenu()
			})
		}
	} else {
		_ = d.tray.SetIcon(png, true)
		d.tray.SetToolTip(tip)
		d.tray.SetTitle(title)
	}
	d.trayKey = key
}
