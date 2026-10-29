package main

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/automation"
	"retty/internal/remote"
	"retty/internal/rex"
)

func mobileAutomationFixture() mobileAutomations {
	now := time.Now()
	task := automation.Automation{ID: "test", Goal: "每小时检查项目构建并汇总结果", Every: "1h", Dir: "/Users/fixture/project", Agent: []string{"codex"}, Enabled: true}
	run := automation.Run{Started: now.Add(-time.Minute), Finished: now, State: "done", Agent: "agent", Runner: "runner", Summary: "构建通过，所有检查成功。", Rounds: 2}
	return mobileAutomations{open: true, checked: now, value: remote.AutomationsSnapshot{At: now, Entries: []remote.AutomationEntry{{Task: task, Run: run}}, History: []automation.Run{run}}}
}

func TestMobileAutomationsListAndDetailFitPhone(t *testing.T) {
	registerFonts()
	for _, size := range [][2]int{{320, 568}, {390, 750}, {750, 310}} {
		for _, dark := range []bool{false, true} {
			m := &mobileApp{client: &rex.Client{}, automations: mobileAutomationFixture()}
			m.hello.Host.Name = "测试主机"
			tt := ui.NewTester(m.view, size[0], size[1])
			tt.SetDark(dark)
			tt.Frame()
			for _, label := range []string{"手机自动化面板", "刷新自动化", "关闭自动化", "查看自动化 每小时检查项目构建并汇总结果"} {
				r, ok := tt.Find(label)
				if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
					t.Fatal("list control clipped", size, label, r)
				}
			}
			m.automations.selected = "test"
			m.sessions = []rex.SessionInfo{{ID: "agent", Program: "codex"}}
			tt.Frame()
			for _, label := range []string{"返回自动化列表", "构建通过，所有检查成功。", "打开自动化执行会话", "执行历史 · 最近 50 次"} {
				if _, ok := tt.Find(label); !ok {
					t.Fatal("detail missing", label)
				}
			}
			if size == [2]int{390, 750} {
				suffix := map[bool]string{false: "light", true: "dark"}[dark]
				saveSettingsImage(t, tt, "mobile-automation-detail-"+suffix)
			}
			tt.Click("关闭自动化")
			if m.automations.open || m.automations.busy {
				t.Fatal("close retained panel/request")
			}
		}
	}
}

func TestMobileAutomationsEntryEmptyErrorAndDismissal(t *testing.T) {
	registerFonts()
	m := &mobileApp{client: &rex.Client{}}
	tt := ui.NewTester(m.view, 390, 750)
	if r, ok := tt.Find("查看自动化"); !ok || r.W < 44 || r.H < 44 {
		t.Fatal("missing accessible automation entry", r)
	}
	m.automations = mobileAutomations{open: true, checked: time.Now()}
	tt.Frame()
	if _, ok := tt.Find("这台主机还没有自动化，可在桌面端创建。"); !ok {
		t.Fatal("missing empty state")
	}
	m.automations.error = remote.ErrAutomationsUnsupported.Error()
	tt.Frame()
	if _, ok := tt.Find("自动化读取错误"); !ok {
		t.Fatal("missing upgrade/error state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.automations.cancel, m.automations.busy = cancel, true
	epoch := m.automations.epoch
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if m.automations.open || m.automations.busy || m.automations.epoch <= epoch || ctx.Err() == nil {
		t.Fatal("dismissal did not cancel pending reads")
	}
}

func TestMobileAutomationsSessionRoutingAndBackgroundCancellation(t *testing.T) {
	m := &mobileApp{automations: mobileAutomationFixture(), sessions: []rex.SessionInfo{{ID: "runner"}, {ID: "agent"}}}
	run := m.automations.value.Entries[0].Run
	if session, ok := m.automationSession(run); !ok || session.ID != "agent" {
		t.Fatal("did not prefer actual agent", session)
	}
	m.sessions[1].Exited = true
	if session, ok := m.automationSession(run); !ok || session.ID != "runner" {
		t.Fatal("startup runner unavailable", session)
	}
	m.sessions[0].Exited = true
	if _, ok := m.automationSession(run); ok {
		t.Fatal("offered a dead session")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.automations.cancel, m.automations.busy = cancel, true
	m.enterBackground()
	if ctx.Err() == nil || m.automations.busy || !m.automations.open || len(m.automations.value.Entries) != 1 {
		t.Fatal("background did not cancel while retaining cached records")
	}
	m.disconnect(false)
	if m.automations.open || len(m.automations.value.Entries) != 0 || m.automations.selected != "" {
		t.Fatal("disconnect retained another host's automations")
	}
}
