package main

import (
	"testing"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

func TestSidebarListsAutomations(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	tt.Frame()
	if !tt.HasText("点 + 描述任务，agent 会帮你设置，如「每小时检查系统错误」") {
		t.Fatal("empty automations section hidden")
	}
	// The timer adds runs while the window is open; the sidebar reads the
	// files again after a few seconds, here at once.
	item, err := automation.New(automation.Automation{Goal: "每天整理 issue", Every: "24h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = automation.Save(rex.Dir(), item); err != nil {
		t.Fatal(err)
	}
	if err = automation.UpdateRun(automation.RunPath(rex.Dir(), item.ID), func(r *automation.Run) {
		r.Started, r.State = time.Now(), "done"
	}); err != nil {
		t.Fatal(err)
	}
	a.automations.loaded = time.Time{}
	tt.Frame()
	if !tt.HasText("每天整理 issue") {
		t.Fatal("automation not listed in the sidebar")
	}
	if !tt.HasText(time.Now().Add(24 * time.Hour).Format("01-02 15:04")) {
		t.Fatal("next run not shown")
	}
	if err := tt.Click("自动化 每天整理 issue"); err != nil || !a.boardShown || a.taskBoard.selected != item.ID {
		t.Fatal("row did not open the board on its automation", err)
	}
}
