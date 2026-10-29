package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
	"retty/internal/remote"
	"retty/internal/rex"
)

type mobileUsageHost struct {
	desktop desktopRecent
	value   agentusage.Snapshot
	error   string
	busy    bool
}
type mobileUsage struct {
	open, busy bool
	epoch      uint64
	cancel     context.CancelFunc
	hosts      []mobileUsageHost
}

func (m *mobileApp) cancelUsage() {
	m.usage.epoch++
	if m.usage.cancel != nil {
		m.usage.cancel()
		m.usage.cancel = nil
	}
	m.usage.busy = false
	for i := range m.usage.hosts {
		m.usage.hosts[i].busy = false
	}
}
func (m *mobileApp) openUsage() { m.usage.open = true; m.refreshUsage(false) }
func (m *mobileApp) refreshUsage(force bool) {
	m.cancelUsage()
	history := m.history
	if m.client != nil && m.link != "" {
		history = mergeDesktopHistory([]desktopRecent{m.connectedDevice()}, history)
	}
	previous := m.usage.hosts
	m.usage.hosts = nil
	for _, d := range history {
		h := mobileUsageHost{desktop: d, busy: true}
		for _, old := range previous {
			if old.desktop.Link == d.Link {
				h.value = old.value
			}
		}
		m.usage.hosts = append(m.usage.hosts, h)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	m.usage.cancel, m.usage.busy = cancel, len(history) > 0
	epoch, client, link := m.usage.epoch, m.client, m.link
	hosts := append([]mobileUsageHost(nil), m.usage.hosts...)
	go func() {
		defer cancel()
		var wg sync.WaitGroup
		gate := make(chan struct{}, 2)
		for i, h := range hosts {
			wg.Go(func() {
				var value agentusage.Snapshot
				var err error
				select {
				case gate <- struct{}{}:
					defer func() { <-gate }()
				case <-ctx.Done():
					err = ctx.Err()
				}
				if err == nil {
					work, done := context.WithTimeout(ctx, 27*time.Second)
					defer done()
					var active *rex.Client
					if h.desktop.Link == link && remote.ConnectionUsable(client) {
						active = client
					} else {
						var closeTunnel func()
						active, closeTunnel, err = remote.Connect(work, h.desktop.Link)
						if closeTunnel != nil {
							defer closeTunnel()
						}
						if active != nil {
							defer active.Close()
						}
					}
					if err == nil {
						value, err = remote.ReadUsage(work, active, force)
					}
				}
				mygo.RunOnMain(func() {
					if epoch != m.usage.epoch {
						return
					}
					row := &m.usage.hosts[i]
					row.busy = false
					row.error = ""
					if err != nil {
						row.error = "无法读取，请确认主机在线且已更新网关"
					} else {
						row.value = value
					}
					m.invalidate()
				})
			})
		}
		wg.Wait()
		mygo.RunOnMain(func() {
			if epoch == m.usage.epoch {
				m.usage.busy = false
				m.usage.cancel = nil
				m.invalidate()
			}
		})
	}()
	m.invalidate()
}

func (m *mobileApp) usageDialog(c *ui.Context) {
	ui.DialogBase(c, &m.usage.open, func(back, panel ui.Element) {
		back.Background(colorsOf(c).sheetScrim).Column().Justify(ui.End).AlignItems(ui.Center).Padding(spaceL)
		panel.Label("订阅用量汇总面板").FillWidth().MaxWidth(480).MaxHeight(c.Root().Bounds().H - 40).Padding(spaceXL).Radius(radiusSheet).Background(c.Theme().Surface).Column().Gap(spaceL).Clip()
		ui.Row(c).FillWidth().Height(44).Shrink(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "订阅用量").FontSize(mobileTitle).Bold().Grow(1)
			if mobileIconAction(c, "刷新订阅用量", "rotate-ccw").Disabled(m.usage.busy).Clicked() {
				m.refreshUsage(true)
			}
			if mobileIconAction(c, "关闭订阅用量", "x").Clicked() {
				m.usage.open = false
				m.cancelUsage()
			}
		})
		ui.Scroll(c.Key("mobile-usage-hosts")).FillWidth().Shrink(1).MinHeight(0).Gap(spaceL).Children(func() {
			ui.Text(c, fmt.Sprintf("%d 台主机 · 各账号额度独立计算", len(m.usage.hosts))).FontSize(textSmall).TextColor(c.Theme().TextMuted).FillWidth()
			if len(m.usage.hosts) == 0 {
				ui.Text(c, "连接一台主机后，即可在这里查看 Codex 和 Claude 用量。").FontSize(mobileSubhead).LineHeight(1.5).FillWidth()
			}
			for _, h := range m.usage.hosts {
				mobileUsageHostView(c, h)
			}
		})
	})
	if !m.usage.open && m.usage.busy {
		m.cancelUsage()
	}
}
func mobileUsageHostView(c *ui.Context, h mobileUsageHost) {
	ui.Column(c).FillWidth().Padding(spaceL).Radius(radiusL).Background(c.Theme().Background).Gap(spaceL).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(spaceM).Children(func() {
			ui.Icon(c, icon("monitor")).Size(16, 16).TextColor(c.Theme().TextMuted)
			ui.Text(c, h.desktop.displayName()).FontSize(mobileBody).Bold().Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if h.busy {
				ui.Spinner(c).Size(16, 16)
			}
		})
		if h.error != "" {
			ui.Text(c, h.error).FontSize(textSmall).TextColor(c.Theme().Danger).FillWidth()
		}
		if !h.value.At.IsZero() {
			ui.Text(c, "更新于 "+h.value.At.Local().Format("01-02 15:04")).FontSize(textCaption).TextColor(c.Theme().TextMuted)
		}
		for _, r := range h.value.Readings {
			mobileUsageReading(c, r)
		}
	})
}
func mobileUsageReading(c *ui.Context, r agentusage.PublicReading) {
	ui.Column(c).FillWidth().Gap(spaceS).Children(func() {
		name := r.Agent
		if name == "codex" {
			name = "Codex"
		}
		if name == "claude" {
			name = "Claude"
		}
		title := name
		if r.Plan != "" {
			title += " · " + r.Plan
		}
		ui.Text(c, title).FontSize(mobileSubhead).Bold().FillWidth()
		if r.Account != "" {
			ui.Text(c, r.Account).FontSize(textCaption).TextColor(c.Theme().TextMuted).FillWidth().SingleLine().Ellipsis("…")
		}
		if r.Error != "" {
			ui.Text(c, r.Error).FontSize(textSmall).LineHeight(1.4).TextColor(c.Theme().TextMuted).FillWidth()
		}
		for _, w := range r.Windows {
			ui.Row(c).FillWidth().Children(func() {
				ui.Text(c, w.Name).FontSize(textSmall).Grow(1)
				ui.Text(c, fmt.Sprintf("已用 %.0f%%", w.Used)).FontSize(textSmall)
			})
			ui.Box(c).FillWidth().Height(5).Radius(3).Background(c.Theme().Border).Children(func() {
				ui.Box(c).WidthPercent(float32(max(0, min(100, w.Used)))).Height(5).Radius(3).Background(c.Theme().Accent)
			})
			if w.Reset != "" {
				ui.Text(c, "重置 "+w.Reset).FontSize(textCaption).TextColor(c.Theme().TextMuted)
			}
		}
		if r.ResetCount != nil {
			ui.Text(c, fmt.Sprintf("Codex 重置卡 · %d 张", *r.ResetCount)).FontSize(textSmall).TextColor(c.Theme().Accent)
		}
	})
}
