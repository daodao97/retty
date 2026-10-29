package main

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

func (a *App) finishDesktopRecovery(h *desktopHost) {
	if h.recoverySince.IsZero() {
		return
	}
	if h.quality != nil {
		h.quality.RecoveryFinished(time.Now())
	}
	h.recoverySince = time.Time{}
}

func (a *App) probeDesktopQuality(h *desktopHost, now time.Time) {
	if !h.connected() || h.quality == nil || h.qualityProbeBusy || now.Sub(h.qualityProbeAt) < 10*time.Second {
		return
	}
	h.qualityProbeBusy, h.qualityProbeAt = true, now
	quality, generation := h.quality, h.generation
	update := a.desktopDispatcher()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		quality.Probe(ctx)
		update(func() {
			if !a.quitting && h.generation == generation && h.quality == quality {
				h.qualityProbeBusy = false
			}
		})
	}()
}

func (a *App) desktopQualityTip(c *ui.Context, h *desktopHost) {
	button := desktopConnectionIconAction(c.Key("connection-quality"), "连接指标 "+h.name(), "info").Tooltip("连接指标")
	if button.Clicked() {
		if a.desktops.qualityHost == h {
			a.desktops.qualityHost = nil
		} else {
			a.desktops.qualityHost = h
		}
	}
	open := a.desktops.qualityHost == h
	popover := ui.PopoverBase(c.Key("connection-quality-popover"), button, &open, func(panel ui.Element) {
		panel.Width(252).Padding(spaceL).Gap(spaceM).Radius(radiusM).Background(c.Theme().Surface).
			Border(1, c.Theme().Border).Shadow(0, 4, 16, 0, colorsOf(c).popoverShadow).Label("连接指标弹层 " + h.name())
		a.probeDesktopQuality(h, c.Now())
		c.After(time.Second)
		s := remote.QualitySnapshot{}
		if h.quality != nil {
			s = h.quality.Snapshot(c.Now())
		}
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "连接指标").FontSize(textSmall).FontWeight(600).Grow(1)
			ui.Text(c, "最近 5 分钟").FontSize(textCaption).TextColor(c.Theme().TextMuted)
		})
		path, latency := "暂无", "暂无"
		if !s.Path.At.IsZero() && c.Now().Sub(s.Path.At) <= 45*time.Second && h.connected() {
			path = "中继"
			if s.Path.Direct {
				path = "直连"
			}
			latency = qualityDuration(s.Path.Latency)
		} else if h.qualityProbeBusy {
			path, latency = "测量中", "测量中"
		}
		request := "暂无"
		if s.Successes > 0 {
			request = fmt.Sprintf("P50 %s · P95 %s", qualityDuration(s.Median), qualityDuration(s.P95))
		}
		timeouts := "暂无"
		if s.Requests > 0 {
			timeouts = fmt.Sprintf("%d / %d 次", s.Timeouts, s.Requests)
		}
		recovery := "暂无"
		if s.Requests > 0 || s.Reconnects > 0 {
			recovery = fmt.Sprintf("%d 次", s.Reconnects)
		}
		if s.HasRecovery {
			recovery += " · " + qualityDuration(s.LastRecovery)
		}
		qualityMetricRow(c, "路径", path)
		qualityMetricRow(c, "链路延迟", latency)
		qualityMetricRow(c, "请求耗时", request)
		qualityMetricRow(c, "请求超时", timeouts)
		qualityMetricRow(c, "重新连接", recovery)
		if s.ScreenRecoveries > 0 {
			qualityMetricRow(c, "画面恢复", fmt.Sprintf("%d 次", s.ScreenRecoveries))
		}
		footnote := "请求耗时包含远端处理时间"
		if !h.connected() {
			footnote = "当前未连接 · 保留最近记录"
		}
		ui.Text(c, footnote).FontSize(textCaption).TextColor(c.Theme().TextMuted).Margin(space2XS, 0, 0, 0)
	})
	if popover.Dismissed() && a.desktops.qualityHost == h {
		a.desktops.qualityHost = nil
	}
}

func qualityMetricRow(c *ui.Context, name, value string) {
	ui.Row(c).AlignItems(ui.Center).Gap(spaceL).Children(func() {
		ui.Text(c, name).FontSize(textCaption).TextColor(c.Theme().TextMuted).Grow(1)
		ui.Text(c, value).FontSize(textCaption).SingleLine()
	})
}

func qualityDuration(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}
