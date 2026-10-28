package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
	"retty/internal/rex"
)

func TestMobilePreviewAcceptsExplicitLoopbackAndKeepsPath(t *testing.T) {
	for _, raw := range []string{"localhost:3000/dev?q=1#preview", "http://127.0.0.1:8080/", "http://[::1]:5173"} {
		u, port, err := mobilePreviewURL(raw)
		if err != nil || port == 0 || u == nil {
			t.Fatal(raw, err)
		}
	}
	u, port, err := mobilePreviewURL("localhost:3000/dev?q=1#preview")
	if err != nil || port != 3000 || u.Path != "/dev" || u.RawQuery != "q=1" || u.Fragment != "preview" {
		t.Fatal("development route lost")
	}
	for _, raw := range []string{"https://example.com", "http://user:pass@localhost:3000", "file:///private/config", "http://localhost:70000", ""} {
		if _, _, err := mobilePreviewURL(raw); err == nil {
			t.Fatal("unsupported preview URL accepted", raw)
		}
	}
}
func TestMobileUsageAndPreviewPanelsFitPhone(t *testing.T) {
	registerFonts()
	count := 2
	for _, dark := range []bool{false, true} {
		m := &mobileApp{home: true}
		m.usage.open = true
		m.usage.hosts = []mobileUsageHost{{desktop: desktopRecent{Name: "MacBook Pro"}, value: agentusage.Snapshot{At: time.Now(), Readings: []agentusage.PublicReading{{Agent: "codex", Plan: "Pro", ResetCount: &count, Windows: []agentusage.Window{{Name: "5 小时", Used: 31, Reset: "今天 19:00"}, {Name: "每周", Used: 72, Reset: "10 月 30 日"}}}, {Agent: "claude", Error: "请先在 CLI 登录"}}}}}
		tt := ui.NewTester(m.view, 390, 750)
		tt.SetDark(dark)
		tt.Frame()
		for _, label := range []string{"订阅用量汇总面板", "关闭订阅用量", "刷新订阅用量"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > 391 || r.Y+r.H > 751 {
				t.Fatal("usage panel outside screen", label, r)
			}
		}
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		saveSettingsImage(t, tt, "mobile-usage-"+suffix)
		m.usage.open = false
		m.preview.open = true
		m.preview.address = "http://localhost:3000"
		tt.Frame()
		if _, ok := tt.Find("开发页面地址"); !ok {
			t.Fatal("manual preview address absent")
		}
		saveSettingsImage(t, tt, "mobile-preview-"+suffix)
		m.preview.open = false
		m.live.tools = true
		m.selected = rex.SessionInfo{ID: "fixture"}
		tt.Frame()
		if _, ok := tt.Find("在锁屏显示此会话"); !ok {
			t.Fatal("lock-screen tracking action absent")
		}
		saveSettingsImage(t, tt, "mobile-tools-"+suffix)
	}
}
