package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agents"
	"retty/internal/diagnostics"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/runtimeinfo"
)

var cmdDiagnostics = command{ID: "app.diagnostics", Title: "Hook 与版本诊断…", Automation: true, Run: func(a *App) { a.openDiagnostics() }}

type desktopDiagnostics struct {
	agent, source, cwdSource, runnerSource string
	busy                                   bool
	err                                    string
	report                                 *diagnostics.Report
	hello                                  rex.Hello
	gui                                    runtimeinfo.Info
	lastHook                               rex.AgentState
	different                              bool
	epoch                                  uint64
	cancel                                 context.CancelFunc
}

func (a *App) openDiagnostics() {
	a.openSettings()
	a.settingsSection = 4
	if a.diagnostics == nil {
		a.diagnostics = &desktopDiagnostics{}
	}
	a.refreshDiagnostics("")
}

func (a *App) refreshDiagnostics(agent string) {
	if a.diagnostics == nil {
		a.diagnostics = &desktopDiagnostics{}
	}
	d := a.diagnostics
	if d.cancel != nil {
		d.cancel()
	}
	d.epoch++
	epoch := d.epoch
	d.busy, d.err, d.report, d.different = true, "", nil, false
	d.gui = runtimeinfo.Current()
	d.source, d.cwdSource = "此电脑", "未取得工作目录"
	d.runnerSource = "服务执行路径推断；旧服务未提供会话创建时的 Hook 路径"
	d.hello, d.lastHook = a.hello, rex.AgentState{}
	var p *Pane
	if t := a.tab(); t != nil {
		p = t.Focus
	}
	var host *desktopHost
	var client *rex.Client
	req := diagnostics.Request{Agent: agent}
	if p != nil {
		host = p.host
		d.lastHook = p.sessionInfo().Agent
		req.CWD = p.sessionInfo().Agent.CWD
		if req.CWD != "" {
			d.cwdSource = "最近接受的 Agent 事件 cwd"
		} else {
			req.CWD = p.sessionInfo().Dir
			if req.CWD == "" {
				req.CWD = p.directory()
			}
			if req.CWD != "" {
				d.cwdSource = "会话目录（未取得 Agent cwd）"
			}
		}
		if req.Agent == "" {
			req.Agent = d.lastHook.ID
		}
		if !slices.Contains(agents.Integrated, req.Agent) {
			req.Agent = "codex"
		}
		if req.Agent == d.lastHook.ID {
			req.ConfigDir = d.lastHook.ConfigDir
		}
		if host != nil {
			client = host.client
			d.source, d.hello = host.name(), host.hello
		}
	}
	if req.Agent == "" {
		req.Agent = "codex"
	}
	d.agent = req.Agent
	req.HookExecutable = d.hello.Exe
	if p != nil && p.sessionInfo().HookExecutable != "" {
		req.HookExecutable = p.sessionInfo().HookExecutable
		d.runnerSource = "会话创建时注入的 RETTY_HOOK；之后手工修改环境不会被追踪"
	}
	if req.HookExecutable == "" && host == nil {
		req.HookExecutable, _ = os.Executable()
		d.runnerSource = "当前 GUI 执行路径推断；未取得会话创建时的 Hook 路径"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	d.cancel = cancel
	hello, loader := d.hello, a.diagnosticRead
	connected := host == nil || host.connected()
	update := a.desktopDispatcher()
	go func() {
		defer cancel()
		var report diagnostics.Report
		var err error
		switch {
		case !connected:
			err = fmt.Errorf("远端未连接，诊断不会回退到本机")
		case loader != nil:
			report, err = loader(ctx, client, req)
		case host != nil:
			report, err = remote.DiagnoseHost(ctx, client, req)
		default:
			report, err = diagnostics.Collect(ctx, req)
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		different := false
		if host == nil && err == nil && hello.Exe != "" {
			if st, statErr := os.Stat(report.Executable); statErr == nil {
				different = hello.Exe != report.Executable || !hello.ExeTime.Equal(st.ModTime())
			}
		}
		update(func() {
			if a.diagnostics != d || d.epoch != epoch || !a.settingsOpen || a.quitting || host != nil && host.client != client || p != nil && p.closed {
				if a.diagnostics == d && d.epoch == epoch {
					d.busy = false
					d.err = "源会话或连接已变化，请刷新诊断"
				}
				return
			}
			d.busy = false
			if err != nil {
				d.err = err.Error()
			} else {
				d.report, d.different = &report, different
			}
		})
	}()
}

func buildDescription(b runtimeinfo.Info) string {
	parts := []string{}
	if b.Version != "" {
		parts = append(parts, b.Version)
	}
	if b.Revision != "" {
		parts = append(parts, b.Revision[:min(12, len(b.Revision))])
	}
	if b.Modified {
		parts = append(parts, "含本地修改")
	}
	if len(parts) == 0 {
		return "未提供构建版本"
	}
	return strings.Join(parts, " · ")
}

func diagnosticPath(c *ui.Context, theme *ui.Theme, title string, p diagnostics.PathCheck) {
	color := theme.TextMuted
	if p.Code != "ok" && p.Code != "unknown" && p.Code != "dynamic" {
		color = ui.Hex("#d96b65")
	}
	ui.Text(c, title+"："+p.Detail).FontSize(12).TextColor(color)
	if p.Path != "" {
		ui.Text(c, p.Path).FontSize(11).TextColor(theme.TextMuted)
	}
}

func (a *App) diagnosticsPage(c *ui.Context, theme *ui.Theme) {
	if a.diagnostics == nil {
		a.later(c, func() { a.refreshDiagnostics("") })
		ui.Text(c, "正在准备诊断…").FontSize(12)
		return
	}
	d := a.diagnostics
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		selected := d.agent
		if ui.Select(c, &selected, agents.Integrated).Label("Diagnostic agent").Grow(1).Changed() {
			a.refreshDiagnostics(selected)
		}
		if ui.Button(c, "刷新").Label("Refresh hook diagnostics").Clicked() {
			a.refreshDiagnostics(d.agent)
		}
		if ui.Button(c, "复制诊断").Label("Copy hook diagnostics").Disabled(d.report == nil || d.busy).Clicked() {
			last := struct {
				Agent, Event, CWD string
				Updated           time.Time
			}{d.lastHook.ID, d.lastHook.HookEvent, d.lastHook.CWD, d.lastHook.Updated}
			service := struct {
				Build         runtimeinfo.Info
				PID, Protocol int
				Executable    string
			}{d.hello.Build, d.hello.PID, d.hello.Version, d.hello.Exe}
			data, _ := json.MarshalIndent(struct {
				Source    string              `json:"source"`
				GUI       runtimeinfo.Info    `json:"gui"`
				Daemon    any                 `json:"daemon"`
				LastEvent any                 `json:"lastEvent"`
				Report    *diagnostics.Report `json:"report"`
			}{d.source, d.gui, service, last, d.report}, "", "  ")
			c.WriteClipboard(string(data))
		}
	})
	ui.Text(c, "检查主机："+d.source).FontSize(12).Margin(14, 0, 6, 0)
	ui.Text(c, "GUI："+buildDescription(d.gui)).FontSize(12)
	if d.gui.MyGo != "" {
		ui.Text(c, "GUI MyGo："+d.gui.MyGo).FontSize(11).TextColor(theme.TextMuted)
	}
	if d.hello.PID > 0 {
		ui.Text(c, fmt.Sprintf("会话服务：%s · PID %d · 协议 %d", buildDescription(d.hello.Build), d.hello.PID, d.hello.Version)).FontSize(12).Margin(8, 0, 0, 0)
		ui.Text(c, d.hello.Exe).FontSize(11).TextColor(theme.TextMuted)
		if d.hello.Build.MyGo != "" {
			ui.Text(c, "服务 MyGo："+d.hello.Build.MyGo).FontSize(11).TextColor(theme.TextMuted)
		}
	}
	if d.different || d.hello.Build.Version != "" && d.gui.Version != "" && d.hello.Build.Version != d.gui.Version {
		ui.Text(c, "会话服务与当前 GUI 构建不同；正在运行的任务保持原样。").FontSize(12).TextColor(theme.TextMuted).Margin(8, 0)
	}
	if d.busy {
		ui.Spinner(c).Size(16, 16).Margin(16, 0)
		ui.Text(c, "正在读取主机配置…").FontSize(12)
		return
	}
	if d.err != "" {
		ui.Text(c, d.err).FontSize(12).TextColor(ui.Hex("#d96b65")).Margin(16, 0)
		return
	}
	if d.report == nil {
		return
	}
	r := d.report
	ui.Text(c, "诊断进程 / 网关："+buildDescription(r.Build)).FontSize(12).Margin(8, 0, 0, 0)
	ui.Text(c, r.Executable).FontSize(11).TextColor(theme.TextMuted)
	ui.Text(c, "只读检查 · "+r.At.Format("15:04:05")).FontSize(11).TextColor(theme.TextMuted).Margin(14, 0, 6, 0)
	ui.Text(c, r.Note).FontSize(11).TextColor(theme.TextMuted)
	ui.Column(c).Gap(5).Margin(16, 0).Children(func() {
		diagnosticPath(c, theme, "工作目录（"+d.cwdSource+"）", r.CWD)
		ui.Text(c, "Retty Hook 路径来源："+d.runnerSource).FontSize(11).TextColor(theme.TextMuted)
		diagnosticPath(c, theme, "系统 /bin/sh（参考；实际 Shell 由 Agent 选择）", r.Shell)
		diagnosticPath(c, theme, "Hook 配置", r.Config)
	})
	if d.lastHook.HookEvent != "" {
		ui.Text(c, "最近接受的 Agent 事件："+d.lastHook.ID+" / "+d.lastHook.HookEvent+" · "+d.lastHook.Updated.Format("15:04:05")).FontSize(11).TextColor(theme.TextMuted)
		ui.Text(c, "事件到达不代表其他 Hook 执行成功。").FontSize(11).TextColor(theme.TextMuted)
	} else {
		ui.Text(c, "没有可确认的 Hook 运行记录；启动前失败的 Hook 需结合 Agent 的报错。").FontSize(11).TextColor(theme.TextMuted)
	}
	for i, h := range r.Hooks {
		ui.Column(c.Key(fmt.Sprintf("diagnostic-hook-%d", i))).FillWidth().Gap(5).Padding(12).Margin(8, 0).Radius(7).Background(theme.Surface).Children(func() {
			ui.Text(c, h.Event).FontSize(12).FontWeight(600)
			command := []rune(h.Command)
			if len(command) > 400 {
				command = append(command[:400], '…')
			}
			ui.Text(c, string(command)).FontSize(11).TextColor(theme.TextMuted)
			diagnosticPath(c, theme, "执行文件", h.Executable)
			if h.Script != nil {
				diagnosticPath(c, theme, "脚本文件", *h.Script)
			}
		})
	}
	if len(r.Hooks) == 0 {
		ui.Text(c, "此配置中没有 command Hook。").FontSize(12).Margin(12, 0)
	}
	if r.Truncated {
		ui.Text(c, "诊断最多显示 128 条命令；过长条目已省略。").FontSize(11).TextColor(theme.TextMuted)
	}
}
