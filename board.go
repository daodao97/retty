package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
	"retty/internal/terminal"

	"github.com/egoist/mygo/ui"
)

// boardPane is the task board: every automation and setup agent, the
// selected one's current run with a read-only preview of its agent's
// screen, and its history. It shows over the tab content, beside the
// sidebar, and is no tab: nothing about automations opens a tab unless the
// person asks for one. Automation sessions get no tabs either (closing such
// a tab would end the run); the board reads snapshots of them instead,
// never attaching, and clicking a preview opens the session in a tab.
type boardPane struct {
	selected string
	// What the selected automation's run looks like, read off the UI
	// thread every few seconds. epoch drops a read made for another
	// selection.
	epoch    int
	fetching bool
	fetched  time.Time
	history  []automation.Run
	runner   rex.SessionInfo
	agent    rex.SessionInfo
	err      string
	// tabbed is a session the person opened in a tab.
	tabbed string
	// term renders termSID's latest snapshot as its tab would, colors and
	// layout included. It reads no session and types into none: it is
	// rebuilt from a snapshot when the session's output changes.
	term       *terminal.Terminal
	termSID    string
	termOutput uint64
}

// dropTerm closes the preview terminal; the session is not touched.
func (b *boardPane) dropTerm() {
	if b.term != nil {
		b.term.Close()
		b.term, b.termSID, b.termOutput = nil, "", 0
	}
}

// showSnapshot renders a session's snapshot in the preview terminal, as the
// terminal history viewer does, rebuilding it only for new output.
func (b *boardPane) showSnapshot(sid string, snap rex.HistorySnapshot) {
	if b.term != nil && b.termSID == sid && b.termOutput == snap.Output {
		return
	}
	conn := &historyConn{done: make(chan struct{})}
	next, err := terminal.New(terminal.Options{Conn: conn, FixedCols: max(snap.Cols, 2), FixedRows: max(snap.Rows, 2), ReflowView: true, Font: termFont, Theme: lightTerm, DarkTheme: darkTerm, AdaptiveColors: true, CopyRawText: prefs.CopyRawText})
	if err != nil {
		conn.Close()
		return
	}
	next.SetInputEnabled(false)
	next.Resize(max(snap.Cols, 2), max(snap.Rows, 2))
	next.Feed(snap.Data)
	b.dropTerm()
	b.term, b.termSID, b.termOutput = next, sid, snap.Output
}

func (b *boardPane) choose(id string) {
	if b.selected == id {
		return
	}
	b.selected, b.history, b.runner, b.agent, b.err = id, nil, rex.SessionInfo{}, rex.SessionInfo{}, ""
	b.dropTerm()
	b.fetched, b.fetching = time.Time{}, false
	b.epoch++
}

// openBoard shows the task board over the current tab's content and
// selects an automation, or a setup agent, when one is given.
func (a *App) openBoard(selected string) {
	a.paletteOpen = false
	if a.taskBoard == nil {
		a.taskBoard = &boardPane{}
	}
	if selected != "" {
		a.taskBoard.choose(selected)
	}
	a.boardShown, a.boardOver = true, a.tab()
	a.focusReq = nil // the board takes the content area
	a.changed()
}

// hideBoard shows the tab content again. The board keeps its selection
// but detaches from the terminal it showed, leaving the session running.
func (a *App) hideBoard() {
	if !a.boardShown {
		return
	}
	a.boardShown = false
	if a.taskBoard != nil {
		a.taskBoard.dropTerm()
	}
	if t := a.tab(); t != nil {
		a.focusReq = t.Focus
	}
	a.changed()
}

// sessionInTab tells that a tab on this computer shows the session.
func (a *App) sessionInTab(sid string) bool {
	for p := range a.hostPanes(nil) {
		if !p.closed && p.terminal().SID == sid {
			return true
		}
	}
	return false
}

// openAutomationTab gives an automation's or a setup agent's session a tab
// of its own, at the person's request. Closing that tab ends the session,
// as for any tab.
func (a *App) openAutomationTab(b *boardPane, in rex.SessionInfo) {
	if a.focusAgentPane(in.ID) {
		return
	}
	b.tabbed = in.ID
	if a.openedAutomation == nil {
		a.openedAutomation = map[string]bool{}
	}
	a.openedAutomation[in.ID] = true
	a.addSessionTab(in)
	a.tabs[len(a.tabs)-1].Focus.terminal().automationOpened = true
	a.selectTab(len(a.tabs) - 1)
	a.focusReq = a.tab().Focus
	a.changed()
}

// fetchBoard reads the selected automation's history, its runner's
// progress and its agent's session, and finds the setup agents running.
// The sessions come from the window's own poll, which lists them every half
// second anyway; the board lists them itself only before that poll has.
func (a *App) fetchBoard(b *boardPane) {
	id, client, epoch, dir, services := b.selected, a.client, b.epoch, rex.Dir(), a.services
	last := a.automations.runs[id]
	setup, isSetup := strings.CutPrefix(id, setupPrefix)
	if isSetup {
		last = automation.Run{Agent: setup}
	}
	var polled []rex.SessionInfo
	if byID, ok := a.pollSnapshots[nil]; ok && client != nil {
		polled = sortedInfos(byID)
	}
	// An agent with no output since the preview's snapshot keeps its
	// screen, and the board reads no snapshot of it.
	shownSID, shownOutput := "", uint64(0)
	if b.term != nil {
		shownSID, shownOutput = b.termSID, b.termOutput
	}
	b.fetching = true
	go func() {
		var history []automation.Run
		if id != "" && !isSetup {
			history = automation.History(dir, id, 50)
		}
		infos, listed, failure := polled, polled != nil, ""
		if !listed && client != nil {
			if all, err := client.List(); err != nil {
				failure = err.Error()
			} else {
				infos, listed = all, true
			}
		}
		var runner, agent rex.SessionInfo
		var found []setupSession
		alive := map[string]rex.SessionInfo{}
		for _, s := range infos {
			switch s.ID {
			case last.Runner:
				runner = s
			case last.Agent:
				agent = s
			}
			if s.Exited {
				continue
			}
			alive[s.ID] = s
			if request := setupRequest(s); request != "" {
				found = append(found, setupSession{info: s, request: request})
			}
		}
		// A read-only snapshot: the board never attaches, so it neither
		// resizes the session nor writes into it.
		var snap rex.HistorySnapshot
		haveSnap := false
		if client != nil && agent.ID != "" && (agent.ID != shownSID || agent.Output != shownOutput) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if got, err := client.History(ctx, agent.ID, 200); err == nil {
				snap = got
				haveSnap = true
			}
			cancel()
		}
		a.laterFrom(services, func() {
			if listed {
				a.mergeSetups(found, alive)
			}
			if b.epoch != epoch {
				return
			}
			b.fetching, b.fetched = false, time.Now()
			b.history, b.runner, b.agent, b.err = history, runner, agent, failure
			if haveSnap {
				b.showSnapshot(agent.ID, snap)
			}
		})
	}()
}

// mergeSetups takes the setup agents the server runs: those found by their
// prompt, those this window started that still run, and any started a
// moment ago, which a list made before them cannot show yet.
func (a *App) mergeSetups(found []setupSession, alive map[string]rex.SessionInfo) {
	var next []setupSession
	for _, s := range a.setupSessions {
		if in, ok := alive[s.info.ID]; ok {
			s.info = in
			next = append(next, s)
		} else if time.Since(s.added) < 5*time.Second {
			next = append(next, s)
		}
	}
	for _, s := range found {
		if !slices.ContainsFunc(next, func(x setupSession) bool { return x.info.ID == s.info.ID }) {
			a.hideSession(s.info.ID)
			next = append(next, s)
		}
	}
	a.setupSessions = next
}

// runnerProgress is what the goal runner reports on its own session, as
// "第 2/8 轮 · 检查失败，agent 继续".
func runnerProgress(s rex.SessionInfo) string {
	for _, status := range s.ProgramStatuses {
		if status.ID == "goal" && status.Message != "" {
			return status.Message
		}
	}
	return s.Agent.Message
}

var runStateLabels = map[string]string{"running": "运行中", "done": "达成", "failed": "未达成", "skipped": "跳过", "interrupted": "已中断"}

func (a *App) boardView(c *ui.Context, k *colors, b *boardPane) {
	v := &a.automations
	a.refreshAutomations(c, 2*time.Second)
	// Until the schedule is first read, the selection may be one it holds.
	_, setup := a.setupSession(b.selected)
	if !setup && !v.loaded.IsZero() && !slices.ContainsFunc(v.list, func(x automation.Automation) bool { return x.ID == b.selected }) {
		next := ""
		if len(v.list) > 0 {
			next = v.list[0].ID
		}
		b.choose(next)
	}
	// Fetching also finds setup agents, so it runs with nothing selected.
	if !b.fetching && time.Since(b.fetched) > 2*time.Second {
		a.fetchBoard(b)
	}
	c.After(2 * time.Second)
	now := time.Now()
	ui.Row(c.Key("board")).Fill().Children(func() {
		ui.Column(c.Key("board-list")).Label("任务列表").Width(300).Shrink(0).FillHeight().
			BorderWidth(0, 1, 0, 0).BorderColor(k.headerBorder).Children(func() {
			ui.Row(c).Height(52).Padding(0, 10, 0, 20).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Text(c, "任务").FontSize(13.5).FontWeight(600).TextColor(k.text)
				ui.Text(c, fmt.Sprint(len(v.list)+len(a.setupSessions))).FontSize(12).TextColor(k.textFaint).Grow(1)
				if iconButton(c.Key("board-new"), k, "plus", "新建任务", 28, 14).Tooltip("新建任务").Clicked() {
					a.openGoal(nil)
				}
			})
			if len(v.list) == 0 && len(a.setupSessions) == 0 {
				ui.Text(c, "还没有任务").FontSize(12.5).TextColor(k.textFaint).Padding(8, 20)
			}
			ui.Scroll(c.Key("board-items")).Grow(1).MinHeight(0).FillWidth().Padding(0, 10, 10, 10).Gap(4).Children(func() {
				for _, s := range a.setupSessions {
					key := setupPrefix + s.info.ID
					if boardRow(c, k, "board-setup-"+s.info.ID, "正在设置 "+s.request, s.request, "新任务", "设置中", stateColor(k, "running"), key == b.selected) {
						b.choose(key)
					}
				}
				for _, item := range v.list {
					status, tone := rowStatus(k, item, v.runs[item.ID], now)
					if boardRow(c, k, "board-item-"+item.ID, "任务 "+item.Name(), item.Name(), displayDir(item.Dir), status, tone, item.ID == b.selected) {
						b.choose(item.ID)
					}
				}
			})
		})
		ui.Scroll(c.Key("board-detail")).Label("任务详情").Grow(1).Basis(0).MinWidth(0).FillHeight().Padding(28, 36).Children(func() {
			ui.Column(c).Gap(30).FillWidth().MaxWidth(980).Children(func() {
				if s, ok := a.setupSession(b.selected); ok {
					a.boardSetupDetail(c, k, b, s)
					return
				}
				i := slices.IndexFunc(v.list, func(x automation.Automation) bool { return x.ID == b.selected })
				if i < 0 {
					ui.Text(c, "未选择任务").FontSize(13).TextColor(k.textFaint)
					return
				}
				a.boardDetail(c, k, b, v.list[i], v.runs[v.list[i].ID], now)
			})
		})
	})
}

// boardAccent is the board's one accent, for its primary action.
func boardAccent(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#4c8df6")
	}
	return ui.Hex("#2f6fdb")
}

func statusDot(c *ui.Context, tone ui.Color) {
	ui.Box(c).Size(7, 7).Radius(3.5).Background(tone).Shrink(0)
}

// statusPill is a state in a tinted capsule of its own color.
func statusPill(c *ui.Context, text string, tone ui.Color) {
	ui.Row(c).Height(22).Padding(0, 9).Gap(6).Radius(11).AlignItems(ui.Center).Background(tone.Alpha(0.14)).Shrink(0).Children(func() {
		statusDot(c, tone)
		ui.Text(c, text).FontSize(11.5).FontWeight(500).TextColor(tone)
	})
}

// boardRow is one entry of the board's list; it reports a click.
func boardRow(c *ui.Context, k *colors, key, label, title, detail, status string, tone ui.Color, selected bool) bool {
	row := ui.ButtonBase(c.Key(key)).Label(label).FillWidth().Padding(10, 12).Gap(5).Radius(8).
		Cursor(ui.CursorPointer).Column().AlignItems(ui.Stretch)
	if selected {
		row.Background(k.panelSel)
	} else if row.Hovered() || row.FocusVisible() {
		row.Background(k.hover)
	}
	row.Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…").MinWidth(0)
		ui.Row(c).Gap(7).AlignItems(ui.Center).FillWidth().Children(func() {
			statusDot(c, tone)
			ui.Text(c, status).FontSize(11.5).TextColor(k.textMuted).Shrink(0)
			ui.Text(c, detail).FontSize(11.5).TextColor(k.textFaint).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		})
	})
	return row.Clicked()
}

// rowStatus is what a list row says of its automation, and its tone.
func rowStatus(k *colors, item automation.Automation, run automation.Run, now time.Time) (string, ui.Color) {
	switch {
	case !item.Enabled:
		return "已暂停", k.textFaint
	case run.State == "running":
		return "运行中", stateColor(k, "running")
	case run.State == "failed" || run.State == "interrupted":
		return runStateLabels[run.State], stateColor(k, run.State)
	case run.State == "done" && run.Result != nil && run.Result.Status != "" && run.Result.Status != "ok":
		return runVerdict(k, run)
	}
	tone := k.textFaint
	if run.State == "done" {
		tone = stateColor(k, "done")
	}
	return automationWhen(item, run, now), tone
}

func intervalLabel(item automation.Automation) string {
	if item.Once {
		return "一次性"
	}
	return everyLabel(item.Interval())
}

func nextRunLabel(item automation.Automation, run automation.Run, now time.Time) string {
	at := item.Next(run, now)
	switch {
	case !item.Enabled, run.State == "running":
		return ""
	case item.Once && at.IsZero():
		return ""
	case !at.After(now):
		return "即将运行"
	}
	return "下次 " + automationWhen(item, run, now)
}

// Button kinds of the board.
const (
	buttonSecondary = iota
	buttonPrimary
	buttonDanger
	// buttonConfirm asks to confirm a destructive action, filled in red.
	buttonConfirm
	// buttonDisabled keeps its place but takes no clicks.
	buttonDisabled
)

func boardButton(c *ui.Context, k *colors, key, label string, kind int) ui.Element {
	return boardIconButton(c, k, key, "", label, kind)
}

// boardIconButton leads its label with an icon, which tells the board's
// actions apart before their labels are read.
func boardIconButton(c *ui.Context, k *colors, key, name, label string, kind int) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(30).Padding(0, 13).Gap(6).Radius(7).AlignItems(ui.Center)
	if name != "" {
		b.Padding(0, 13, 0, 11)
	}
	if kind != buttonDisabled {
		b.Cursor(ui.CursorPointer)
	}
	text := k.text
	switch kind {
	case buttonPrimary, buttonConfirm:
		accent := boardAccent(c)
		if kind == buttonConfirm {
			accent = stateColor(k, "failed")
		}
		if b.Hovered() || b.FocusVisible() {
			accent = accent.Mix(ui.Hex("#ffffff"), 0.1)
		}
		b.Background(accent)
		text = ui.Hex("#ffffff")
	case buttonDisabled:
		b.Border(1, k.panelBorder)
		text = k.textFaint
	default:
		b.Border(1, k.panelBorder)
		if b.Pressed() {
			b.Background(k.pressed)
		} else if b.Hovered() || b.FocusVisible() {
			b.Background(k.hover)
		}
		if kind == buttonDanger {
			text = stateColor(k, "failed")
		}
	}
	b.Children(func() {
		if name != "" {
			ui.Icon(c, icon(name)).Size(13, 13).TextColor(text).Shrink(0)
		}
		ui.Text(c, label).FontSize(12.5).FontWeight(500).TextColor(text)
	})
	return b
}

func boardSection(c *ui.Context, k *colors, title, count string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(k.text)
		if count != "" {
			ui.Text(c, count).FontSize(12).TextColor(k.textFaint)
		}
	})
}

// boardCard is the board's surface: a hairline border, no shadow.
func boardCard(c *ui.Context, k *colors, key string) ui.Element {
	return ui.Column(c.Key(key)).FillWidth().Radius(10).Clip().Border(1, k.panelBorder)
}

// boardTerminal previews a session's latest screen, read without attaching,
// so the board neither resizes nor types into it. Clicking the preview
// opens the session in a tab, to see and use it in full.
func (a *App) boardTerminal(c *ui.Context, k *colors, b *boardPane, in rex.SessionInfo, status string) {
	if b.tabbed == in.ID || a.sessionInTab(in.ID) {
		boardCard(c, k, "board-terminal-tabbed").Padding(14, 16).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Icon(c, icon("square-terminal")).Size(15, 15).TextColor(k.iconMuted).Shrink(0)
				ui.Text(c, "已在标签页中打开").FontSize(12.5).TextColor(k.textMuted).Grow(1)
				if boardButton(c, k, "board-switch-tab", "切换到标签页", buttonSecondary).Clicked() {
					a.later(c, func() {
						if !a.focusAgentPane(in.ID) {
							b.tabbed = ""
						}
					})
				}
			})
		})
		return
	}
	fg := lightTerm.Foreground
	if c.Theme().Dark {
		fg = darkTerm.Foreground
	}
	boardCard(c, k, "board-terminal-frame").Background(terminalBackground(c)).Children(func() {
		ui.Row(c).Height(38).Padding(0, 6, 0, 14).Gap(9).AlignItems(ui.Center).FillWidth().
			BorderWidth(0, 0, 1, 0).BorderColor(fg.Alpha(0.1)).Children(func() {
			ui.Icon(c, icon("square-terminal")).Size(14, 14).TextColor(fg.Alpha(0.5)).Shrink(0)
			ui.Text(c, status).FontSize(12).TextColor(fg.Alpha(0.72)).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			if in.Exited {
				ui.Text(c, "已结束").FontSize(12).TextColor(fg.Alpha(0.5)).Padding(0, 8)
				return
			}
			open := ui.ButtonBase(c.Key("board-open-tab")).Label("在标签页中打开").Height(26).Padding(0, 10).Gap(5).Radius(6).
				AlignItems(ui.Center).Cursor(ui.CursorPointer)
			if open.Hovered() || open.FocusVisible() {
				open.Background(fg.Alpha(0.08))
			}
			open.Children(func() {
				ui.Text(c, "在标签页中打开").FontSize(12).TextColor(fg.Alpha(0.85))
				ui.Icon(c, icon("arrow-up-right")).Size(12, 12).TextColor(fg.Alpha(0.6))
			})
			if open.Clicked() {
				a.later(c, func() { a.openAutomationTab(b, in) })
			}
		})
		// The session's own screen, rendered as its tab renders it, from a
		// snapshot: watching it neither resizes nor types into the session.
		ui.Box(c.Key("board-terminal")).Label("终端预览").Height(360).FillWidth().Padding(10, 14).Clip().Children(func() {
			if b.term != nil && b.termSID == in.ID {
				terminal.View(c, b.term).Key("board-preview-terminal").Fill().TextCaretFunc(nil).Label("终端预览")
			}
		})
	})
}

// boardTitle is a detail's heading: its title and actions, then a line of
// state and facts.
func boardTitle(c *ui.Context, k *colors, title string, actions, meta func()) {
	ui.Column(c).Gap(12).FillWidth().Children(func() {
		ui.Row(c).Gap(20).AlignItems(ui.Start).FillWidth().Children(func() {
			ui.Text(c, title).FontSize(20).FontWeight(600).LineHeight(1.35).TextColor(k.text).Grow(1).MinWidth(0).Wrap()
			ui.Row(c).Gap(8).Shrink(0).Children(actions)
		})
		meta()
	})
}

// boardSetupDetail shows a setup agent at work, previewing its screen.
func (a *App) boardSetupDetail(c *ui.Context, k *colors, b *boardPane, s setupSession) {
	in := b.agent
	if in.ID == "" {
		in = s.info
	}
	boardTitle(c, k, s.request, func() {
		// The setup agent stays for follow-up questions until ended here.
		if boardButton(c, k, "board-setup-end", "结束设置", buttonSecondary).Clicked() {
			sid := in.ID
			a.setupSessions = slices.DeleteFunc(a.setupSessions, func(x setupSession) bool { return x.info.ID == sid })
			b.choose("")
			if client := a.client; client != nil {
				go client.Kill(sid)
			}
		}
	}, func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			statusPill(c, "设置中", stateColor(k, "running"))
			ui.Text(c, "正在设置任务").FontSize(12.5).TextColor(k.textMuted)
		})
	})
	if _, ok := a.setupSession(b.selected); ok {
		ui.Column(c.Key("board-setup-current")).Gap(12).FillWidth().Children(func() {
			boardSection(c, k, "设置过程", "")
			a.boardTerminal(c, k, b, in, "agent 正在设置任务")
		})
	}
}

// templateMessage is a runner's routine result line, which a summary
// already says better.
func templateMessage(m string) bool {
	return m == "" || strings.HasPrefix(m, "agent 已完成") || strings.HasPrefix(m, "目标达成")
}

func (a *App) boardDetail(c *ui.Context, k *colors, b *boardPane, item automation.Automation, run automation.Run, now time.Time) {
	agent := b.agent
	// A runner that ended without its result was closed or killed; the
	// timer records it as interrupted on its next pass.
	interrupted := run.State == "running" && !b.fetched.IsZero() && (b.runner.ID == "" || b.runner.Exited) && now.Sub(run.Started) > 15*time.Second
	state, tone := "未运行", k.textFaint
	switch {
	case interrupted:
		state, tone = "已中断", stateColor(k, "interrupted")
	case run.State == "running":
		state, tone = "运行中", stateColor(k, "running")
	case !item.Enabled:
		state = "已暂停"
	case run.State != "":
		state, tone = runVerdict(k, run)
		state = "上次" + state
	}
	boardTitle(c, k, item.Name(), func() { a.automationActions(c, k, item, run.State == "running" && !interrupted) }, func() {
		meta := []string{intervalLabel(item), strings.Join(item.Agent, " "), displayDir(item.Dir)}
		if next := nextRunLabel(item, run, now); next != "" {
			meta = append(meta, next)
		}
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			statusPill(c, state, tone)
			ui.Text(c, strings.Join(meta, "  ·  ")).FontSize(12.5).TextColor(k.textMuted).Tooltip(item.Dir)
		})
		// The full instruction the agent receives, under its short title.
		if item.Goal != item.Name() {
			ui.Text(c, item.Goal).FontSize(13).LineHeight(1.6).TextColor(k.textMuted).FillWidth().Wrap()
		}
		if item.Check != "" {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "检查").FontSize(12).TextColor(k.textFaint)
				ui.Text(c, item.Check).Font(termFont.Family).FontSize(12).TextColor(k.textMuted)
			})
		}
		if err := a.automations.err; err != "" {
			ui.Text(c, err).FontSize(12).TextColor(stateColor(k, "failed"))
		}
	})

	ui.Column(c.Key("board-current")).Gap(12).FillWidth().Children(func() {
		boardSection(c, k, "当前执行", "")
		var status string
		switch {
		case interrupted:
			status = "runner 会话已关闭，下次到点重新运行"
		case run.State == "running":
			status = "启动中"
			if p := runnerProgress(b.runner); b.runner.ID != "" && p != "" {
				status = p
			}
			if label := agentStateLabel(agent.Agent); label != "" && !strings.Contains(status, label) {
				status += " · " + label
			}
			status += " · " + run.Started.Local().Format("15:04") + " 开始"
		case !agent.Exited:
			status = "上次运行的终端，下次运行继续使用"
		default:
			status = "上次运行的终端"
		}
		// The last run's agent, also one that ended: its final screen is
		// what it reported.
		if agent.ID != "" {
			a.boardTerminal(c, k, b, agent, status)
		} else {
			boardCard(c, k, "board-idle").Padding(18, 18).Children(func() {
				text := "未在运行"
				if next := nextRunLabel(item, run, now); next != "" {
					text += " · " + next
				}
				ui.Text(c, text).FontSize(12.5).TextColor(k.textFaint)
			})
		}
		if b.err != "" {
			ui.Text(c, b.err).FontSize(12).TextColor(k.textFaint)
		}
	})

	a.boardResult(c, k, b, item)

	ui.Column(c.Key("board-history")).Gap(12).FillWidth().Children(func() {
		count := ""
		if len(b.history) > 0 {
			count = fmt.Sprint(len(b.history))
		}
		boardSection(c, k, "执行历史", count)
		if len(b.history) == 0 {
			boardCard(c, k, "board-history-empty").Padding(18, 18).Children(func() {
				ui.Text(c, "暂无记录").FontSize(12.5).TextColor(k.textFaint)
			})
			return
		}
		boardCard(c, k, "board-history-list").Children(func() {
			for i, r := range b.history {
				row := ui.Row(c.Key(fmt.Sprint("board-run-", i))).Gap(18).Padding(14, 18).AlignItems(ui.Start).FillWidth()
				if i > 0 {
					row.BorderWidth(1, 0, 0, 0).BorderColor(k.panelBorder)
				}
				row.Children(func() {
					ui.Column(c).Gap(3).Width(64).Shrink(0).Children(func() {
						ui.Text(c, r.Started.Local().Format("15:04")).FontSize(13).FontWeight(500).TextColor(k.text)
						ui.Text(c, r.Started.Local().Format("01-02")).FontSize(11.5).TextColor(k.textFaint)
					})
					ui.Column(c).Gap(7).Grow(1).MinWidth(0).Children(func() {
						var meta []string
						if !r.Finished.IsZero() && r.Finished.After(r.Started) {
							meta = append(meta, "用时 "+r.Finished.Sub(r.Started).Round(time.Second).String())
						}
						if r.Rounds > 0 {
							meta = append(meta, fmt.Sprintf("%d 轮", r.Rounds))
						}
						ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
							verdictPill(c, k, r)
							ui.Text(c, strings.Join(meta, "  ·  ")).FontSize(12).TextColor(k.textFaint)
						})
						if r.Summary != "" {
							ui.Text(c, r.Summary).FontSize(13).LineHeight(1.5).TextColor(k.text).FillWidth().Wrap()
						}
						if r.Result != nil {
							if brief := resultBrief(*r.Result); brief != "" {
								ui.Text(c, brief).FontSize(12).TextColor(k.textMuted).FillWidth().Wrap()
							}
						}
						if r.Message != r.Summary && !templateMessage(r.Message) {
							ui.Text(c, r.Message).FontSize(12).LineHeight(1.5).TextColor(k.textMuted).FillWidth().Wrap()
						}
					})
				})
			}
		})
	})
}
