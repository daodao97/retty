package main

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

func TestUsageSettingsAndTraySelection(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	prefs = settings{FontSize: defaultFontSize}
	a, tt := newStaticTestApp(t)
	readings := []agentusage.Reading{
		{Agent: "codex", Plan: "pro", At: time.Now(), Windows: []agentusage.Window{{Name: "5 小时", Used: 17, Reset: "10-28 09:00"}, {Name: "7 天", Used: 42}}},
		{Agent: "claude", Error: "当前账户未提供订阅额度"},
	}
	a.usage = &desktopUsage{readings: readings}
	a.openUsage()
	tt.Frame()
	text := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"17% 已用", "42% 已用", "10-28 09:00", "当前账户未提供订阅额度"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if err := tt.Click("Usage tray"); err != nil {
		t.Fatal(err)
	}
	if !prefs.HideUsageTray || !readSettings().HideUsageTray {
		t.Fatal("tray visibility not persisted")
	}
	tt.SetDark(true)
	saveSettingsImage(t, tt, "settings-usage-dark")
	tt.SetDark(false)
	saveSettingsImage(t, tt, "settings-usage-light")
	tt.SetSize(560, 420)
	saveSettingsImage(t, tt, "settings-usage-small")
	if r := selectedUsage(readings, ""); r == nil || r.Agent != "codex" {
		t.Fatal("auto did not choose available account")
	}
	if r := selectedUsage(readings, "claude"); r != nil {
		t.Fatal("fixed missing account silently fell back")
	}
	if !strings.Contains(usageTrayTip(&readings[0]), "83% 剩余") || strings.Contains(usageTrayTip(nil), "0%") {
		t.Fatal("wrong tray tooltip")
	}
}

func TestUsagePanelSwitchesProvidersWithoutChangingTrayOrSessions(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	prefs = settings{UsageTrayAgent: "codex"}
	a := &App{usage: &desktopUsage{panelAgent: "codex", readings: []agentusage.Reading{
		{Agent: "codex", Plan: "pro", At: time.Now(), Windows: []agentusage.Window{{Name: "5 小时", Used: 17, Reset: "10-28 09:00"}, {Name: "7 天", Used: 42, Reset: "11-02 14:08"}}},
		{Agent: "claude", At: time.Now(), Error: "当前账户未提供订阅额度"},
	}}}
	tt := ui.NewTester(a.usagePanelView, usagePanelWidth, a.usage.height())
	text := strings.Join(tt.Texts(), " ")
	for _, want := range []string{"83% 剩余", "58% 剩余", "10-28 09:00", "PRO"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	for _, label := range []string{"Usage provider Codex", "Usage provider Claude", "Refresh usage panel", "Usage panel settings"} {
		if r, ok := tt.Find(label); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > usagePanelWidth || r.Y < 0 || r.Y+r.H > float32(a.usage.height()) {
			t.Fatalf("panel control %s not reachable: %+v", label, r)
		}
	}
	tt.SetScale(2)
	tt.SetDark(true)
	saveSettingsImage(t, tt, "usage-panel-dark")
	windows := a.usage.readings[0].Windows
	a.usage.readings[0].Windows = windows[:1]
	tt.SetSize(usagePanelWidth, a.usage.height())
	saveSettingsImage(t, tt, "usage-panel-single-dark")
	a.usage.readings[0].Windows = windows
	tt.SetSize(usagePanelWidth, a.usage.height())
	tt.SetDark(false)
	saveSettingsImage(t, tt, "usage-panel-light")
	if err := tt.Click("Usage provider Claude"); err != nil {
		t.Fatal(err)
	}
	tt.SetSize(usagePanelWidth, a.usage.height())
	text = strings.Join(tt.Texts(), " ")
	if !strings.Contains(text, "当前账户未提供订阅额度") || strings.Contains(text, "83% 剩余") {
		t.Fatalf("wrong provider contents: %s", text)
	}
	if !strings.Contains(text, "无法读取订阅额度") || !strings.Contains(text, "刚刚检查") || strings.Contains(text, "刚刚更新") {
		t.Fatalf("unavailable reading presented as a successful update: %s", text)
	}
	if prefs.UsageTrayAgent != "codex" || len(a.tabs) != 0 {
		t.Fatal("provider preview changed tray preference or opened sessions")
	}
	saveSettingsImage(t, tt, "usage-panel-unavailable")
	tt.Key(0, ui.KeyLeft)
	if a.usage.panelAgent != "codex" {
		t.Fatal("provider tabs do not support keyboard selection")
	}
	a.usage.busy = true
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), " "), "正在更新…") {
		t.Fatal("refresh state is invisible")
	}
}

func TestUsagePanelLoadingAndLongContent(t *testing.T) {
	a := &App{usage: &desktopUsage{panelAgent: "codex", busy: true}}
	tt := ui.NewTester(a.usagePanelView, usagePanelWidth, a.usage.height())
	tt.SetScale(2)
	text := strings.Join(tt.Texts(), " ")
	if !strings.Contains(text, "正在读取额度") || strings.Contains(text, "0%") {
		t.Fatalf("loading should not fabricate an empty quota: %s", text)
	}
	saveSettingsImage(t, tt, "usage-panel-loading")
	a.usage.busy = false
	a.usage.readings = []agentusage.Reading{{Agent: "codex", At: time.Now(), Error: "读取失败：本机账户尚未登录。请先在 Codex CLI 中完成登录，然后返回此处刷新订阅额度。如果已经登录，请确认 CLI 使用的配置目录与当前账户一致。此面板读取本机账户的订阅额度，不会将缺失的数据显示为零。"}}
	tt.Frame()
	saveSettingsImage(t, tt, "usage-panel-long-error")
	// Long failure details scroll inside the body, keeping recovery controls fixed.
	tt.Scroll(150, 110, 0, 200)
	assertUsagePanelControls(t, tt, a.usage.height())
	saveSettingsImage(t, tt, "usage-panel-long-error-scrolled")
	a.usage.readings = []agentusage.Reading{{Agent: "codex", Plan: "a very long subscription plan name", At: time.Now(), Windows: []agentusage.Window{
		{Name: "5 小时", Used: 92, Reset: "10-28 09:00"},
		{Name: "7 天", Used: 100, Reset: "11-02 14:08"},
		{Name: "额度名称较长的额外订阅周期", Used: 0},
		{Name: "额外周期", Used: 50, Reset: "11-03 14:08"},
	}}}
	tt.SetSize(usagePanelWidth, a.usage.height())
	tt.SetDark(true)
	saveSettingsImage(t, tt, "usage-panel-low-quota")
	tt.SetDark(false)
	saveSettingsImage(t, tt, "usage-panel-low-quota-light")
	tt.Scroll(150, 180, 0, 300)
	reset, ok := tt.Find("重置于 11-03 14:08")
	if !ok || reset.Y < usagePanelHeader || reset.Y+reset.H > float32(a.usage.height()-usagePanelFooter) {
		t.Fatalf("extra quota window cannot be scrolled into view: %+v", reset)
	}
	assertUsagePanelControls(t, tt, a.usage.height())
	saveSettingsImage(t, tt, "usage-panel-scrolled")
}

func assertUsagePanelControls(t *testing.T, tt *ui.Tester, height int) {
	t.Helper()
	for _, label := range []string{"Usage provider Codex", "Usage provider Claude", "Refresh usage panel", "Usage panel settings"} {
		r, ok := tt.Find(label)
		if !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.X+r.W > usagePanelWidth || r.Y < 0 || r.Y+r.H > float32(height) {
			t.Fatalf("panel control %s not reachable: %+v", label, r)
		}
	}
}

func TestUsagePanelStatusSeparatesAttemptsAndUpdates(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 5, 0, 0, time.Local)
	for _, tc := range []struct {
		name string
		r    agentusage.Reading
		busy bool
		want string
	}{
		{name: "not read", want: "尚未更新"},
		{name: "refreshing", busy: true, want: "正在更新…"},
		{name: "success", r: agentusage.Reading{At: now, Windows: []agentusage.Window{{Used: 17}}}, want: "刚刚更新"},
		{name: "failure", r: agentusage.Reading{At: now, Error: "读取失败"}, want: "刚刚检查"},
		{name: "no quota", r: agentusage.Reading{At: now}, want: "刚刚检查"},
		{name: "previous day", r: agentusage.Reading{At: now.Add(-10 * time.Minute), Error: "读取失败"}, want: "10-27 23:55 检查"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usagePanelStatus(tc.r, tc.busy, now); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUsageTrayNativeTitleDistinguishesUnavailableZeroAndFull(t *testing.T) {
	zero := &agentusage.Reading{Windows: []agentusage.Window{{Used: 0}, {Used: 0}}}
	full := &agentusage.Reading{Windows: []agentusage.Window{{Used: 100}, {Used: 100}}}
	if usageTrayTitle(nil) != "—" || usageTrayTitle(zero) != "100% · 100%" || usageTrayTitle(full) != "0% · 0%" {
		t.Fatal("native title does not distinguish missing and known readings")
	}
	im, err := png.Decode(bytes.NewReader(usageTrayIcon()))
	if err != nil || im.Bounds().Dx() != 36 || im.Bounds().Dy() != 36 {
		t.Fatalf("invalid tray PNG: %v", err)
	}
	if remainingUsage(0) != 100 || remainingUsage(100) != 0 || remainingUsage(19) != 81 {
		t.Fatal("remaining percentage is reversed")
	}
	if usageTrayTitle(&agentusage.Reading{Windows: []agentusage.Window{{Used: 19}}}) != "81%" {
		t.Fatal("single period should show only its remaining percentage")
	}
}
