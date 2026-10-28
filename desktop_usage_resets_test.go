package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

func usageResetFixture(t *testing.T) agentusage.Reading {
	t.Helper()
	soon, later := time.Now().Add(2*time.Hour).Unix(), time.Now().Add(7*24*time.Hour).Unix()
	return agentusage.Reading{
		Agent: "codex", Account: "fixture@example.invalid", AccountKey: "id:fixture", Home: t.TempDir(), Plan: "pro", At: time.Now(),
		Windows: []agentusage.Window{{Name: "5 小时", Used: 17, Reset: "10-28 09:00"}, {Name: "7 天", Used: 42, Reset: "11-02 14:08"}},
		Resets: &agentusage.ResetCredits{Count: 3, Credits: []agentusage.ResetCard{
			{ID: "first", Type: "codexRateLimits", Status: "available", ExpiresAt: &soon},
			{ID: "later", Type: "codexRateLimits", Status: "available", ExpiresAt: &later},
			{ID: "never", Type: "codexRateLimits", Status: "available"},
		}},
	}
}

func TestUsageResetPanelRequiresConfirmationAndKeepsRetryIdentity(t *testing.T) {
	r := usageResetFixture(t)
	a := &App{usage: &desktopUsage{panelAgent: "codex", readings: []agentusage.Reading{r, {Agent: "claude"}}}}
	tt := ui.NewTester(a.usagePanelView, usagePanelWidth, a.usage.height())
	tt.SetScale(2)
	if !tt.HasText("3 张重置卡") || !tt.HasText("最近到期") || !strings.Contains(usageTrayTip(&r), "3 张重置卡") {
		t.Fatal("reset count or expiry missing from quota surfaces")
	}
	if a.usage.resetAttempt != nil || a.usage.busy {
		t.Fatal("rendering quota started a redemption")
	}
	assertUsagePanelControls(t, tt, a.usage.height())
	saveSettingsImage(t, tt, "usage-panel-resets-light")
	tt.SetDark(true)
	saveSettingsImage(t, tt, "usage-panel-resets-dark")
	if err := tt.Click("Use Codex reset"); err != nil {
		t.Fatal(err)
	}
	tt.SetSize(usagePanelWidth, a.usage.height())
	if !a.usage.resetOpen || a.usage.resetAttempt == nil || a.usage.busy || a.usage.resetAttempt.CreditID != "first" || !tt.HasText("此操作无法撤销") {
		t.Fatal("use did not stop at confirmation of the earliest card")
	}
	saveSettingsImage(t, tt, "usage-panel-reset-confirm")
	if err := tt.Click("Cancel Codex reset"); err != nil {
		t.Fatal(err)
	}
	if a.usage.resetOpen || a.usage.busy {
		t.Fatal("cancel started a redemption or retained confirmation")
	}
	// A lost reply leaves one operation pending. A later attempt to use the
	// button must offer that same operation, including after a refresh to zero.
	attempt := *a.usage.resetAttempt
	a.usage.resetRetry, a.usage.resetMessage = true, "用卡结果尚未确认"
	a.usage.readings[0].Resets = &agentusage.ResetCredits{Count: 0}
	tt.SetSize(usagePanelWidth, a.usage.height())
	tt.Click("Use Codex reset")
	tt.SetSize(usagePanelWidth, a.usage.height())
	if !a.usage.resetOpen || *a.usage.resetAttempt != attempt || !tt.HasText("不会重复消耗") {
		t.Fatal("ambiguous retry changed the operation or became unavailable at zero credits")
	}
	saveSettingsImage(t, tt, "usage-panel-reset-retry")
	tt.Click("Cancel Codex reset")
	a.usage.busy = true
	tt.Frame()
	tt.Click("Use Codex reset")
	if a.usage.resetOpen {
		t.Fatal("parallel usage request allowed a second reset")
	}
}

func TestUsageResetSettingsEscapeClosesOnlyConfirmation(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	prefs = settings{FontSize: defaultFontSize}
	a, tt := newStaticTestApp(t)
	a.usage = &desktopUsage{readings: []agentusage.Reading{usageResetFixture(t)}}
	a.openUsage()
	tt.Frame()
	if err := tt.Click("Use Codex reset"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.usage.resetOpen || !a.settingsOpen || a.usage.busy {
		t.Fatal("settings use bypassed confirmation")
	}
	tt.SetSize(560, 420)
	saveSettingsImage(t, tt, "settings-reset-confirm-small")
	tt.Key(0, ui.KeyEscape)
	if a.usage.resetOpen || !a.settingsOpen {
		t.Fatal("Escape closed settings instead of only the use confirmation")
	}
}

func TestUsageResetUnknownAndZeroDetailsStayDistinct(t *testing.T) {
	r := usageResetFixture(t)
	r.Resets = nil
	a := &App{usage: &desktopUsage{panelAgent: "codex", readings: []agentusage.Reading{r}}}
	tt := ui.NewTester(a.usagePanelView, usagePanelWidth, a.usage.height())
	if tt.HasText("张重置卡") {
		t.Fatal("unsupported CLI fabricated a zero count")
	}
	a.usage.readings[0].Resets = &agentusage.ResetCredits{Count: 0}
	tt.SetSize(usagePanelWidth, a.usage.height())
	tt.Click("Use Codex reset")
	if !tt.HasText("0 张重置卡") || a.usage.resetOpen {
		t.Fatal("known zero was hidden or allowed a new redemption")
	}
	partial := usageResetFixture(t).Resets
	partial.Count = 5
	text, details := resetExpiryText(partial)
	if !strings.Contains(text, "3 / 5") || !strings.Contains(details, "无到期限制") || strings.Contains(text, "最近到期") {
		t.Fatal("partial expiry list looked complete", text, details)
	}
}
