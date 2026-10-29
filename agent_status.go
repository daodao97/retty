package main

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agents"
	"retty/internal/rex"
)

// An old hook must not put an agent badge on the shell or another program
// after the agent exited. Direct program reports have server-owned lifetimes
// and also work for programs with no agent-specific integration.
func paneAgentState(p *Pane) rex.AgentState {
	return sessionAgentState(p.sessionInfo())
}

func sessionAgentState(info rex.SessionInfo) rex.AgentState {
	s := info.Agent
	if s.Source == rex.ProgramStatusSource {
		return s
	}
	if s.State == "" || info.Idle || info.Exited {
		return rex.AgentState{}
	}
	if agent, ok := agents.Detect(info.Program, info.Args); !ok || agent.ID != s.ID {
		return rex.AgentState{}
	}
	return s
}

func agentStateLabel(s rex.AgentState) string {
	switch s.State {
	case agents.Ready:
		return "就绪"
	case agents.Running:
		if s.Progress != nil {
			return fmt.Sprintf("执行中 %d%%", *s.Progress)
		}
		return "执行中"
	case agents.Waiting:
		switch s.Reason {
		case "permission":
			return "等待授权"
		case "question":
			return "等待回答"
		case "auth":
			return "等待登录"
		default:
			return "等待输入"
		}
	case agents.Completed:
		return "已完成"
	case agents.Failed:
		return "执行失败"
	}
	return ""
}

func tabAgentState(t *Tab) (*Pane, rex.AgentState) {
	priority := map[string]int{agents.Waiting: 5, agents.Failed: 4, agents.Running: 3, agents.Completed: 2, agents.Ready: 1}
	var chosen *Pane
	var state rex.AgentState
	for _, p := range t.panes() {
		s := paneAgentState(p)
		if priority[s.State] > priority[state.State] || (priority[s.State] > 0 && priority[s.State] == priority[state.State] && p == t.Focus) {
			chosen, state = p, s
		}
	}
	return chosen, state
}

func (a *App) agentIndicator(c *ui.Context, k *colors, p *Pane, s rex.AgentState) {
	if p == nil || s.State == "" || s.State == agents.Ready {
		return
	}
	label := "Agent " + s.State
	if s.State == agents.Waiting {
		label = "Agent waiting for input"
	}
	name := programOf(s.ID).Name
	tip := name + " · " + agentStateLabel(s)
	if s.Label != "" {
		tip += " · " + s.Label
	}
	if s.Message != "" {
		tip += " · " + s.Message
	}
	if s.State == agents.Waiting && s.Progress != nil {
		tip += fmt.Sprintf(" · %d%%", *s.Progress)
	}
	e := ui.Box(c.Key("agent-state-"+p.noticeKey())).Size(13, 16).Shrink(0).Center().
		Role(ui.RoleButton).Label(label).Tooltip(tip + " · 点击定位窗格").Cursor(ui.CursorPointer)
	e.Children(func() {
		switch s.State {
		case agents.Waiting:
			ui.Text(c, "Ⅱ").FontSize(textCaption).FontWeight(700).TextColor(k.attention)
		case agents.Running:
			ui.Box(c).Size(6, 6).Radius(3).Background(k.busy)
		case agents.Completed:
			ui.Icon(c, icon("check")).Size(12, 12).TextColor(k.busy)
		case agents.Failed:
			ui.Icon(c, icon("x")).Size(12, 12).TextColor(colorsOf(c).danger)
		default:
			ui.Icon(c, icon("circle-dot")).Size(9, 9).TextColor(k.textFaint)
		}
	})
	if e.Clicked() {
		a.later(c, func() { a.focusAgentPane(p.noticeKey()) })
	}
}

func (a *App) paneIsViewed(p *Pane) bool {
	if a.terminalHistory != nil && a.terminalHistory.open {
		return false
	}
	if p == nil || p.closed || p.terminal() == nil {
		return false
	}
	if a.pushPresence != nil && !desktopUserPresent(a.pushPresence.locked, a.pushPresence.sleeping, mygo.Power.IdleTime()) {
		return false
	}
	focused := a.focusedWin
	if a.win != nil {
		focused = a.win.IsFocused() && !a.win.IsMinimized()
	}
	t := a.tab()
	// Settings and the command palette do not switch the selected pane.
	return focused && t == p.Tab && t != nil && t.Focus == p && (t.Zoom == nil || t.Zoom == p)
}

func (a *App) closeViewedPaneNotice() {
	if t := a.tab(); t != nil && a.paneIsViewed(t.Focus) {
		a.closeAgentNotice(t.Focus.noticeKey())
		if !a.settingsOpen && !a.paletteOpen && !a.desktops.open && (a.focusedFiles() == nil || !a.focusedFiles().expanded) {
			a.readResult(t.Focus)
		}
	}
}

// agentNotice is a notification shown for a pane, and what it tells of:
// an agent state, "program" or "terminal".
type agentNotice struct {
	close func()
	kind  string
}

func (a *App) closeAgentNotice(sid string) {
	if n := a.agentNotices[sid]; n.close != nil {
		n.close()
		delete(a.agentNotices, sid)
	}
}

func (a *App) updateAgentNotice(p *Pane, previous rex.AgentState) {
	s := paneAgentState(p)
	// A completed one-shot agent may already have returned to the shell
	// before polling. The authenticated event can still announce its result.
	if s.State == "" && agents.Finished(p.terminal().info.Agent.State) {
		s = p.terminal().info.Agent
	}
	finished := agents.Finished(s.State)
	if s.State != agents.Waiting && !finished {
		if s.State != "" || a.agentNotices[p.noticeKey()].kind == agents.Waiting || previous.Source == rex.ProgramStatusSource {
			a.closeAgentNotice(p.noticeKey())
		}
		return
	}
	if (s.State != previous.State && !finished) || (finished && a.agentNotices[p.noticeKey()].kind == agents.Waiting) {
		a.closeAgentNotice(p.noticeKey())
	}
	var hidden bool
	if finished {
		if a.agentFinishedNotified == nil {
			a.agentFinishedNotified = map[string]uint64{}
		}
		if s.CompletionRevision <= a.agentFinishedNotified[p.noticeKey()] {
			return
		}
		a.agentFinishedNotified[p.noticeKey()] = s.CompletionRevision
		// Restoring a window must not announce historical completed turns.
		if previous.SessionID == s.SessionID && previous.CompletionRevision == s.CompletionRevision && agents.Finished(previous.State) {
			return
		}
		if rex.AgentTitleCompletion(s) {
			a.closeAgentNotice(p.noticeKey())
			return
		}
		hidden = prefs.HideAgentCompletionNotifications
	} else {
		if a.agentNotified == nil {
			a.agentNotified = map[string]uint64{}
		}
		if s.WaitRevision <= a.agentNotified[p.noticeKey()] {
			return
		}
		a.agentNotified[p.noticeKey()] = s.WaitRevision
		hidden = prefs.HideAgentNotifications
	}
	if hidden {
		return
	}
	if a.paneIsViewed(p) {
		if a.pushPresence != nil {
			a.routePaneNotice(p, s.State, mygo.NotificationOptions{})
		}
		return
	}
	if !a.programNoticeLimiter.Allow(p.noticeKey(), s, time.Now()) {
		return
	}
	body := ""
	if p.host == nil {
		body = rex.AgentNoticeBody(rex.Dir(), p.terminal().info)
	} else {
		body = p.host.name() + " · " + agentStateLabel(s)
		if s.Source == rex.ProgramStatusSource || s.Summary != "" {
			body += " · " + rex.AgentNoticeBody("", p.terminal().info)
		}
	}
	opts := mygo.NotificationOptions{Title: programOf(s.ID).Name + " · " + agentStateLabel(s), Body: body}
	a.showPaneNotice(p, s.State, opts)
}

func (a *App) showPaneNotice(p *Pane, kind string, opts mygo.NotificationOptions) {
	if p == nil || p.closed || a.quitting {
		return
	}
	if a.pushPresence != nil {
		a.routePaneNotice(p, kind, opts)
		return
	}
	a.showDesktopPaneNotice(p, kind, opts)
}

func (a *App) showDesktopPaneNotice(p *Pane, kind string, opts mygo.NotificationOptions) {
	// All notification paths, including terminal OSC messages, share the
	// current focus check. A cached polling snapshot may already be stale.
	if prefs.HideDesktopNotifications || p == nil || p.closed || a.quitting || a.paneIsViewed(p) {
		return
	}
	// Headless views use an injected notifier, never the native AppKit API.
	if a.win == nil && a.agentNotify == nil {
		return
	}
	if a.agentNotices == nil {
		a.agentNotices = map[string]agentNotice{}
	}
	a.closeAgentNotice(p.noticeKey())
	sid := p.noticeKey()
	click := func() {
		if a.win == nil && a.agentNotify == nil && a.openWindow != nil {
			a.openWindow()
		}
		if a.win != nil {
			if a.win.IsMinimized() {
				a.win.Restore()
			}
			a.win.Show()
			a.win.Focus()
			a.win.Update(func() { a.focusAgentPane(sid) })
		} else {
			a.focusAgentPane(sid)
		}
	}
	if a.agentNotify != nil {
		a.agentNotices[sid] = agentNotice{a.agentNotify(opts, click), kind}
	} else {
		n := mygo.NewNotification(opts)
		n.OnClick(click)
		if err := n.Show(); err != nil {
			a.agentHookError = fmt.Sprintf("无法发送通知：%v。请检查系统设置中的通知权限。", err)
			n.Close()
			return
		}
		a.agentNotices[sid] = agentNotice{n.Close, kind}
	}
}

func (a *App) showTerminalNotice(p *Pane, title, body string) {
	if title == "" {
		title = "Retty"
	}
	a.showPaneNotice(p, "terminal", mygo.NotificationOptions{Title: title, Body: body})
}

func (a *App) focusAgentPane(sid string) bool {
	for t, p := range a.allPanes() {
		if p.terminal() == nil || p.noticeKey() != sid || p.closed {
			continue
		}
		a.settingsOpen, a.paletteOpen, a.renaming = false, false, nil
		p.terminal().find.open, p.terminal().find.focus = false, false
		if t.Zoom != nil && t.Zoom != p {
			t.Zoom = nil
		}
		t.setFocus(p)
		a.selectTab(slices.Index(a.tabs, t))
		return true
	}
	return false
}
