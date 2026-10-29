package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/automation"
	"retty/internal/remote"
	"retty/internal/rex"
)

type mobileAutomations struct {
	open, busy bool
	selected   string
	value      remote.AutomationsSnapshot
	error      string
	checked    time.Time
	epoch      uint64
	cancel     context.CancelFunc
}

func (m *mobileApp) cancelAutomations() {
	m.automations.epoch++
	if m.automations.cancel != nil {
		m.automations.cancel()
		m.automations.cancel = nil
	}
	m.automations.busy = false
	m.automations.checked = time.Time{}
}

func (m *mobileApp) closeAutomations() {
	m.automations.open = false
	m.cancelAutomations()
}

func (m *mobileApp) openAutomations() {
	m.automations.open = true
	m.refreshAutomations()
}

func (m *mobileApp) refreshAutomations() {
	m.cancelAutomations()
	v := &m.automations
	v.checked = time.Now()
	if !m.connectionUsable() || m.background {
		v.error = "连接恢复后可刷新自动化"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	v.cancel, v.busy, v.error = cancel, true, ""
	epoch, generation, client, id := v.epoch, m.generation, m.client, v.selected
	go func() {
		defer cancel()
		value, err := remote.ReadAutomations(ctx, client, id)
		mygo.RunOnMain(func() {
			if epoch != m.automations.epoch || generation != m.generation || client != m.client {
				return
			}
			v := &m.automations
			v.busy, v.cancel = false, nil
			if err != nil {
				v.error = err.Error()
			} else {
				v.value = value
			}
			m.invalidate()
		})
	}()
	m.invalidate()
}

func (m *mobileApp) selectAutomation(id string) {
	m.automations.selected = id
	m.automations.value.History = nil
	m.refreshAutomations()
}

func (m *mobileApp) automationsDialog(c *ui.Context) {
	v := &m.automations
	if v.open && !m.background {
		c.After(5 * time.Second)
		if !v.busy && time.Since(v.checked) >= 5*time.Second {
			m.refreshAutomations()
		}
	}
	dialog := ui.DialogBase(c, &v.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, .4)).Column().Justify(ui.End).AlignItems(ui.Center).Padding(12)
		panel.Label("手机自动化面板").FillWidth().MaxWidth(480).MaxHeight(c.Root().Bounds().H - 40).Padding(16).Radius(20).Background(c.Theme().Surface).Column().Gap(12).Clip()
		ui.Row(c).FillWidth().Height(44).Shrink(0).AlignItems(ui.Center).Children(func() {
			if v.selected != "" && mobileIconAction(c, "返回自动化列表", "chevron-left").Clicked() {
				m.selectAutomation("")
			}
			ui.Text(c, "自动化").FontSize(18).Bold().Grow(1)
			if mobileIconAction(c, "刷新自动化", "rotate-ccw").Disabled(v.busy || !m.connectionUsable()).Clicked() {
				m.refreshAutomations()
			}
			if mobileIconAction(c, "关闭自动化", "x").Clicked() {
				m.closeAutomations()
			}
		})
		ui.Scroll(c.Key("mobile-automations-" + v.selected)).FillWidth().MinHeight(0).Shrink(1).Gap(12).Children(func() {
			ui.Text(c, m.connectedDevice().displayName()).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth()
			if v.busy {
				ui.Text(c, "正在读取自动化…").FontSize(13).TextColor(c.Theme().TextMuted)
			}
			if v.error != "" {
				ui.Text(c, v.error).Label("自动化读取错误").FontSize(13).TextColor(c.Theme().Danger).FillWidth().Wrap()
			}
			if m.degraded() || m.background {
				ui.Text(c, "连接恢复后更新，以下为上次读取的记录。").FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
			}
			if !v.value.At.IsZero() {
				ui.Text(c, "更新于 "+v.value.At.Local().Format("01-02 15:04:05")).FontSize(11).TextColor(c.Theme().TextMuted)
			}
			if v.selected == "" {
				m.automationList(c)
			} else if i := slices.IndexFunc(v.value.Entries, func(e remote.AutomationEntry) bool { return e.Task.ID == v.selected }); i >= 0 {
				m.automationDetail(c, v.value.Entries[i])
			}
		})
	})
	if dialog.Dismissed() {
		m.closeAutomations()
	}
}

func (m *mobileApp) automationList(c *ui.Context) {
	v := &m.automations
	if len(v.value.Entries) == 0 && !v.busy && v.error == "" {
		ui.Text(c, "这台主机还没有自动化，可在桌面端创建。").FontSize(14).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
	}
	for _, entry := range v.value.Entries {
		row := ui.ButtonBase(c.Key("mobile-automation-" + entry.Task.ID)).Label("查看自动化 " + entry.Task.Name()).Role(ui.RoleButton).
			FillWidth().MinHeight(76).Padding(12).Radius(12).Background(c.Theme().Background).Column().Gap(6)
		row.Children(func() {
			ui.Text(c, entry.Task.Name()).FontSize(15).Bold().FillWidth().Wrap()
			ui.Text(c, automationSchedule(entry.Task, entry.Run, time.Now())).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
			ui.Text(c, mobileAutomationState(entry.Run)).FontSize(12).TextColor(stateColor(colorsOf(c), entry.Run.State)).FillWidth().Wrap()
		})
		if row.Clicked() {
			m.selectAutomation(entry.Task.ID)
		}
	}
}

func mobileAutomationState(run automation.Run) string {
	if label := runStateLabels[run.State]; label != "" {
		return label
	}
	return "尚未运行"
}

func (m *mobileApp) automationSession(run automation.Run) (rex.SessionInfo, bool) {
	// Prefer the actual agent, falling back to the runner during startup.
	for _, id := range []string{run.Agent, run.Runner} {
		for _, session := range m.sessions {
			if id != "" && session.ID == id && !session.Exited {
				return session, true
			}
		}
	}
	return rex.SessionInfo{}, false
}

func (m *mobileApp) automationDetail(c *ui.Context, entry remote.AutomationEntry) {
	v, task, run := &m.automations, entry.Task, entry.Run
	ui.Text(c, task.Name()).FontSize(17).Bold().FillWidth().Wrap()
	if task.Goal != task.Name() {
		ui.Text(c, task.Goal).FontSize(14).FillWidth().Wrap()
	}
	ui.Text(c, automationSchedule(task, run, time.Now())).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
	ui.Text(c, task.Dir).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
	if task.Check != "" {
		ui.Text(c, "检查命令："+task.Check).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
	}
	ui.Text(c, "最近执行").FontSize(14).Bold()
	mobileAutomationRun(c, run)
	if session, ok := m.automationSession(run); ok {
		if mobileTextAction(c, "打开自动化执行会话", "打开执行会话").Disabled(!m.connectionUsable()).Clicked() {
			m.closeAutomations()
			m.openSession(session)
		}
	} else {
		ui.Text(c, "当前没有可打开的执行会话").FontSize(12).TextColor(c.Theme().TextMuted).FillWidth()
	}
	ui.Text(c, "执行历史 · 最近 50 次").FontSize(14).Bold()
	if len(v.value.History) == 0 && !v.busy {
		ui.Text(c, "还没有执行记录").FontSize(13).TextColor(c.Theme().TextMuted)
	}
	for i, previous := range v.value.History {
		ui.Column(c.Key(fmt.Sprintf("automation-history-%d", i))).FillWidth().Padding(12).Radius(12).Background(c.Theme().Background).Gap(6).Children(func() {
			mobileAutomationRun(c, previous)
		})
	}
}

func mobileAutomationRun(c *ui.Context, run automation.Run) {
	label := mobileAutomationState(run)
	if !run.Started.IsZero() {
		label = run.Started.Local().Format("01-02 15:04") + " · " + label
	}
	if run.Finished.After(run.Started) && !run.Started.IsZero() {
		label += " · 用时 " + run.Finished.Sub(run.Started).Round(time.Second).String()
	}
	if run.Rounds > 0 {
		label += fmt.Sprintf(" · %d 轮", run.Rounds)
	}
	ui.Text(c, label).FontSize(13).TextColor(stateColor(colorsOf(c), run.State)).FillWidth().Wrap()
	if run.Summary != "" {
		ui.Text(c, run.Summary).FontSize(13).LineHeight(1.5).FillWidth().Wrap()
	}
	if run.Result != nil {
		mobileResult(c, *run.Result)
	}
	if run.Message != "" && run.Message != run.Summary {
		ui.Text(c, run.Message).FontSize(12).TextColor(c.Theme().TextMuted).FillWidth().Wrap()
	}
}
