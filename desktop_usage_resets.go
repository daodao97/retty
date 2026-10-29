package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

func resetExpiryText(r *agentusage.ResetCredits) (string, string) {
	if r == nil || r.Count == 0 {
		return "暂无可用重置卡", ""
	}
	if len(r.Credits) == 0 {
		return "到期时间暂不可用", ""
	}
	var details []string
	for i, card := range r.Credits {
		when := "无到期限制"
		if card.ExpiresAt != nil {
			when = time.Unix(*card.ExpiresAt, 0).Local().Format("2006-01-02 15:04") + " 到期"
		}
		details = append(details, fmt.Sprintf("第 %d 张：%s", i+1, when))
	}
	if len(r.Credits) != r.Count {
		return fmt.Sprintf("已提供 %d / %d 张卡的到期信息", len(r.Credits), r.Count), strings.Join(details, "\n")
	}
	text := "无到期限制"
	if at := r.Credits[0].ExpiresAt; at != nil {
		text = "最近到期 " + time.Unix(*at, 0).Local().Format("01-02 15:04")
	}
	return text, strings.Join(details, "\n")
}

func (a *App) usageResetSection(c *ui.Context, r agentusage.Reading, compact bool) {
	d := a.usage
	if d == nil || r.Resets == nil && d.resetMessage == "" {
		return
	}
	theme := c.Theme()
	ui.Column(c).FillWidth().Gap(spaceM).Children(func() {
		ui.Box(c).FillWidth().Height(0.5).Background(theme.Border)
		if r.Resets != nil {
			ui.Row(c).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon("rotate-ccw")).Size(13, 13).TextColor(theme.TextMuted)
				ui.Text(c, fmt.Sprintf("%d 张重置卡", r.Resets.Count)).FontSize(textSmall).FontWeight(500).TextColor(theme.Text).Grow(1)
				label := "使用一张…"
				retry := d.resetRetry && d.resetAttempt != nil && d.resetAttempt.AccountKey == r.AccountKey
				if retry {
					label = "重试用卡…"
				}
				if d.resetting {
					label = "正在使用…"
				}
				button := ui.Button(c, label).Label("Use Codex reset").Disabled(d.busy || r.Error != "" || r.AccountKey == "" || r.Home == "" || r.Resets.Count == 0 && !retry)
				if compact {
					button.FontSize(textCaption).Height(25).Padding(0, spaceM)
				}
				if button.Clicked() {
					a.beginUsageReset(r)
				}
			})
			text, details := resetExpiryText(r.Resets)
			ui.Text(c, text).FontSize(textCaption).TextColor(theme.TextMuted).Tooltip(details)
		}
		if d.resetMessage != "" {
			ui.Text(c, d.resetMessage).FillWidth().FontSize(textCaption).LineHeight(1.4).TextColor(theme.TextMuted)
		}
	})
}

func (a *App) beginUsageReset(r agentusage.Reading) {
	d := a.usage
	if d == nil || d.closed || d.busy {
		return
	}
	if !d.resetRetry || d.resetAttempt == nil || d.resetAttempt.AccountKey != r.AccountKey || d.resetAttempt.Home != r.Home {
		attempt, err := agentusage.NewResetAttempt(r)
		if err != nil {
			d.resetMessage = err.Error()
			a.invalidateUsageViews()
			return
		}
		d.resetAttempt = &attempt
		d.resetRetry = false
	}
	d.resetReading, d.resetOpen = r, true
	a.invalidateUsageViews()
}

func (a *App) usageResetConfirmation(c *ui.Context, _ agentusage.Reading) {
	d := a.usage
	if d == nil || d.resetAttempt == nil {
		return
	}
	r := d.resetReading
	theme := c.Theme()
	ui.Column(c).FillWidth().Gap(spaceL).Children(func() {
		title := "使用一张 Codex 重置卡？"
		if d.resetRetry {
			title = "重试上次用卡操作？"
		}
		ui.Text(c, title).FontSize(textTitle).FontWeight(600).TextColor(theme.Text)
		if r.Account != "" {
			ui.Text(c, r.Account).FillWidth().FontSize(textCaption).TextColor(theme.TextMuted).SingleLine().Ellipsis("…").Tooltip(r.Account)
		}
		body := "将消耗 1 张卡，立即重置符合条件的用量周期。此操作无法撤销。"
		if d.resetRetry {
			body = "上次请求的结果尚未确认。重试会复用同一次操作标识，已完成的用卡不会重复消耗。"
		}
		ui.Text(c, body).FillWidth().FontSize(textCaption).LineHeight(1.5).TextColor(theme.TextMuted)
		selection := "由 Codex 选择一张可用卡。"
		if d.resetRetry {
			selection = "仅重试上次确认的操作，不会另选一张卡。"
		} else if d.resetAttempt.CreditID != "" {
			selection = "优先使用最早到期的卡。"
			if text, _ := resetExpiryText(r.Resets); text != "" {
				selection += "\n" + text
			}
		}
		ui.Text(c, selection).FillWidth().FontSize(textCaption).LineHeight(1.4).TextColor(theme.TextMuted)
		if d.resetMessage != "" && d.resetRetry {
			ui.Text(c, d.resetMessage).FillWidth().FontSize(textCaption).TextColor(theme.TextMuted)
		}
		ui.Row(c).FillWidth().Gap(spaceM).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Label("Cancel Codex reset").Disabled(d.resetting).Clicked() {
				d.resetOpen = false
				a.invalidateUsageViews()
			}
			label := "确认使用"
			if d.resetRetry {
				label = "重试同次操作"
			}
			if d.resetting {
				label = "正在使用…"
			}
			if ui.Button(c, label).Label("Confirm Codex reset").Disabled(d.busy).Clicked() {
				a.consumeUsageReset()
			}
		})
	})
}

func (a *App) consumeUsageReset() {
	d := a.usage
	if d == nil || d.closed || d.busy || !d.resetOpen || d.resetAttempt == nil {
		return
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.busy, d.resetting = true, true
	attempt := *d.resetAttempt
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	a.invalidateUsageViews()
	go func() {
		defer cancel()
		result, err := agentusage.UseCodexReset(ctx, attempt)
		mygo.RunOnMain(func() {
			if d.closed {
				return
			}
			d.busy, d.resetting, d.cancel = false, false, nil
			if err != nil {
				d.resetRetry, d.resetMessage = true, err.Error()
			} else {
				d.resetRetry, d.resetOpen, d.resetAttempt = false, false, nil
				d.resetMessage = usageResetOutcome(result.Outcome)
				if result.Reading.Error != "" {
					d.resetMessage += "；额度读取失败，请刷新"
				}
				found := false
				for i := range d.readings {
					if d.readings[i].Agent == "codex" {
						d.readings[i], found = result.Reading, true
					}
				}
				if !found {
					d.readings = append(d.readings, result.Reading)
				}
				a.syncUsageTray()
			}
			d.timer = time.AfterFunc(3*time.Minute, func() { mygo.RunOnMain(a.refreshUsage) })
			a.invalidateUsageViews()
		})
	}()
}

func usageResetOutcome(outcome string) string {
	switch outcome {
	case "reset":
		return "已使用 1 张重置卡，额度已更新"
	case "alreadyRedeemed":
		return "上次用卡已完成，未重复消耗"
	case "nothingToReset":
		return "当前没有可重置的周期，未消耗卡片"
	case "noCredit":
		return "当前账户已无可用重置卡"
	}
	return "请刷新确认重置卡状态"
}
