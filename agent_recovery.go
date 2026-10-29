package main

import (
	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func (a *App) systemShuttingDown() bool {
	if a.shuttingDown != nil {
		return a.shuttingDown()
	}
	return rex.SystemShuttingDown()
}

// Stream EOF has no process exit code. Ask the daemon before deciding whether
// to close a resumed pane; a CLI error must keep its output and retry action.
func (a *App) checkAgentRestoreExit(p *Pane, stream *rex.Stream) {
	p.terminal().streamEnded = true
	sid, update := p.terminal().SID, a.desktopDispatcher()
	go func() {
		infos, err := a.client.List()
		var info *rex.SessionInfo
		for _, in := range infos {
			if in.ID == sid {
				info = &in
				break
			}
		}
		update(func() {
			if p.closed || a.quitting || p.terminal().stream != stream || a.systemShuttingDown() {
				return
			}
			if err == nil && info != nil && info.Exited && info.ExitCode == 0 {
				a.closePane(p)
				return
			}
			p.terminal().recoverySID = sid
			p.terminal().recoveryError = "Agent 恢复进程已退出，请检查终端输出后重试"
			if err != nil || info == nil || !info.Exited {
				p.terminal().recoveryError = "会话连接已断开，可重试连接原会话"
			}
			if info != nil {
				p.terminal().info = *info
			}
			a.changed()
		})
	}()
}

func (a *App) restoreAgentPane(saved *savedNode) *Pane {
	r, err := a.client.RestoreAgent(saved.SID, saved.Cols, saved.Rows, false)
	if err == nil && r.Session == nil && r.Error == "" {
		return nil
	}
	p := &Pane{ID: a.id(), content: &terminalPane{startDir: saved.Dir, restored: true}}
	if err != nil {
		r.Error = err.Error()
	}
	a.applyAgentRestore(p, saved.SID, saved.Cols, saved.Rows, r)
	return p
}

func (a *App) applyAgentRestore(p *Pane, sid string, cols, rows int, r rex.RestoreResult) {
	p.terminal().recoveryBusy = false
	if r.Session != nil {
		p.terminal().SID, p.terminal().info = r.Session.ID, *r.Session
		p.terminal().recoverySID, p.terminal().recoveryError = "", ""
		if r.Error != "" {
			p.terminal().recoverySID, p.terminal().recoveryError = sid, r.Error
		}
		a.attach(p, r.Session.Cols, r.Session.Rows)
		return
	}
	if r.Error == "" {
		r.Error = "原 Agent 会话已结束，无法恢复"
	}
	p.terminal().SID, p.terminal().recoverySID, p.terminal().recoveryError = "", sid, r.Error
	if r.Agent != "" {
		p.terminal().info.Program = r.Agent
	}
	p.terminal().info.Exited = true
	a.attach(p, cols, rows)
}

func (a *App) retryAgentRestore(p *Pane) {
	if p.closed || p.terminal().recoveryBusy || p.terminal().recoverySID == "" {
		return
	}
	sid := p.terminal().recoverySID
	cols, rows := 80, 24
	if p.terminalView() != nil {
		cols, rows = p.terminal().term.Size()
	}
	p.terminal().recoveryBusy = true
	update := a.desktopDispatcher()
	go func() {
		r, err := a.client.RestoreAgent(sid, cols, rows, true)
		if err != nil {
			r.Error = err.Error()
		}
		update(func() {
			p.terminal().recoveryBusy = false
			// Ordinary GUI quit detaches and must preserve a restore already
			// started by the daemon. Only an explicit pane close ends it.
			if a.quitting {
				return
			}
			if p.closed {
				if r.Session != nil {
					go a.client.Kill(r.Session.ID)
				}
				return
			}
			a.closePaneTerminal(p)
			p.terminal().stream = nil
			a.applyAgentRestore(p, sid, cols, rows, r)
			a.focusReq = p
			a.changed()
		})
	}()
}

func (a *App) agentRecoveryBar(c *ui.Context, k *colors, p *Pane) {
	ui.Row(c).Height(30).Padding(0, spaceM).AlignItems(ui.Center).Gap(spaceM).Background(k.panel).Children(func() {
		message := "恢复失败"
		if p.terminal().recoveryBusy {
			message = "正在恢复会话…"
		}
		ui.Text(c, message).FontSize(textSmall).TextColor(k.textMuted).Grow(1).Tooltip(p.terminal().recoveryError)
		if !p.terminal().recoveryBusy && iconButton(c, k, "rotate-ccw", "重试恢复会话", 24, 13).Clicked() {
			a.retryAgentRestore(p)
		}
	})
}
