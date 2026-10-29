package main

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"

	"github.com/egoist/mygo/ui"
)

// automationsView is the schedule the sidebar and the task board show. It
// lives in files the minute timer reads, so the window reads them again
// every few seconds, not on every frame.
type automationsView struct {
	automationsRead
	// loaded is when the files were read; refreshing is a read off the UI
	// thread not taken yet.
	loaded     time.Time
	refreshing bool
	collapsed  bool
	// removing is the automation whose delete button asks to be confirmed,
	// since when; the question lapses after a few seconds.
	removing    string
	removeAsked time.Time
}

// removeConfirmWindow is how long a delete button waits for its
// confirming click.
const removeConfirmWindow = 4 * time.Second

var cmdAutomations = command{ID: "automation.list", Title: "任务看板", Run: func(a *App) { a.openBoard("") }}

// automationsRead is the schedule as its files hold it.
type automationsRead struct {
	list []automation.Automation
	runs map[string]automation.Run
	err  string
}

func readAutomations(dir string) automationsRead {
	list, err := automation.List(dir)
	r := automationsRead{list: list, runs: make(map[string]automation.Run, len(list))}
	if err != nil {
		r.err = err.Error()
	}
	for _, a := range list {
		r.runs[a.ID] = automation.LastRun(dir, a.ID)
	}
	return r
}

func (v *automationsView) take(r automationsRead, at time.Time) {
	v.automationsRead, v.loaded = r, at
}

// reload reads the schedule at once, for a decision that depends on it, as
// whether a session gets a tab.
func (v *automationsView) reload() { v.take(readAutomations(rex.Dir()), time.Now()) }

// refreshAutomations reads the schedule again off the UI thread once it is
// older than age, for the sidebar's and the board's periodic refresh: with
// a few automations that is a few dozen file reads, too slow for a frame.
func (a *App) refreshAutomations(c *ui.Context, age time.Duration) {
	v := &a.automations
	if v.refreshing || time.Since(v.loaded) <= age {
		return
	}
	v.refreshing = true
	services, dir, started := c.Services(), rex.Dir(), time.Now()
	go func() {
		read := readAutomations(dir)
		a.laterFrom(services, func() {
			v.refreshing = false
			// A reload made meanwhile read newer files.
			if v.loaded.Before(started) {
				v.take(read, started)
			}
		})
	}()
}

// hiddenAutomationSession tells that a session belongs to an automation
// run, which the task board shows instead of a tab: closing such a tab
// would end the run. One opened in a tab from the board stays one.
func (a *App) hiddenAutomationSession(in rex.SessionInfo) bool {
	if a.openedAutomation[in.ID] {
		return false
	}
	if a.hiddenSessions[in.ID] {
		return true
	}
	// The runner's own process carries its run record, and a setup agent
	// its prompt, also after the window restarts.
	if slices.Contains(in.Args, "--state-file") || setupRequest(in) != "" {
		a.hideSession(in.ID)
		return true
	}
	// Callers read the schedule again first: a stale one would give a
	// just-started agent a tab.
	v := &a.automations
	for _, item := range v.list {
		run := v.runs[item.ID]
		if run.Runner != "" && in.ID == run.Runner || run.Agent != "" && in.ID == run.Agent {
			a.hideSession(in.ID)
			return true
		}
		// A runner starts its agent a moment before recording it.
		if run.State == "running" && run.Agent == "" && in.Dir == item.Dir &&
			!in.Created.Before(run.Started) && in.Created.Before(run.Started.Add(30*time.Second)) {
			a.hideSession(in.ID)
			return true
		}
	}
	return false
}

// hideSession keeps a session the task board shows out of the tabs.
func (a *App) hideSession(sid string) {
	if a.hiddenSessions == nil {
		a.hiddenSessions = map[string]bool{}
	}
	a.hiddenSessions[sid] = true
}

// setupPromptLead starts every setup agent's prompt, which identifies its
// session among the server's, also after the window restarts.
const setupPromptLead = "请把下面的需求设置成 Retty 任务"

// setupPrefix marks a setup agent among the board's selections.
const setupPrefix = "setup:"

// setupSession is an agent setting up a task from a request.
type setupSession struct {
	info    rex.SessionInfo
	request string
	added   time.Time
}

// setupRequest is the request a setup agent's prompt carries, or "".
func setupRequest(in rex.SessionInfo) string {
	for _, arg := range in.Args {
		if !strings.HasPrefix(arg, setupPromptLead) {
			continue
		}
		_, request, ok := strings.Cut(arg, "需求：")
		if !ok {
			return "（未知需求）"
		}
		request, _, _ = strings.Cut(request, "\n")
		return strings.TrimSpace(request)
	}
	return ""
}

func (a *App) setupSession(selected string) (setupSession, bool) {
	sid, ok := strings.CutPrefix(selected, setupPrefix)
	if !ok {
		return setupSession{}, false
	}
	i := slices.IndexFunc(a.setupSessions, func(s setupSession) bool { return s.info.ID == sid })
	if i < 0 {
		return setupSession{}, false
	}
	return a.setupSessions[i], true
}

// updateAutomation saves an edited automation and shows it on the board. A
// new directory or agent starts a new conversation, since the previous one
// had another project as its context.
func (a *App) updateAutomation(old, next automation.Automation) error {
	dir := rex.Dir()
	if err := automation.Save(dir, next); err != nil {
		return err
	}
	// Only a run that happened has an agent to forget; writing a record for
	// one that never ran would make it due at once.
	if last := automation.LastRun(dir, next.ID); !last.Started.IsZero() && (next.Dir != old.Dir || !slices.Equal(next.Agent, old.Agent)) {
		if err := automation.UpdateRun(automation.RunPath(dir, next.ID), func(r *automation.Run) { r.Agent = "" }); err != nil {
			return err
		}
	}
	if err := syncAutomations(dir); err != nil {
		a.err = "无法更新自动化定时器：" + err.Error()
	}
	a.automations.reload()
	a.openBoard(next.ID)
	return nil
}

// runAutomation starts one run off the UI thread, since creating a session
// waits for its PTY. The task board then shows the run.
func (a *App) runAutomation(id, exe string) {
	dir, socket := rex.Dir(), rex.SocketPath()
	go func() {
		err := automation.Tick(dir, exe, func() (*rex.Client, error) {
			return rex.ConnectDial(context.Background(), func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
			})
		}, id, time.Now())
		read, at := readAutomations(dir), time.Now()
		a.laterFrom(a.services, func() {
			if err != nil {
				a.err = "自动化运行失败：" + err.Error()
			}
			if a.automations.loaded.Before(at) {
				a.automations.take(read, at)
			}
		})
	}()
}

func (a *App) changeAutomation(change func(dir string) error) {
	dir := rex.Dir()
	if err := change(dir); err != nil {
		a.automations.err = err.Error()
	} else if err = syncAutomations(dir); err != nil {
		a.automations.err = "无法更新自动化定时器：" + err.Error()
	}
	err := a.automations.err
	a.automations.reload()
	if a.automations.err == "" {
		a.automations.err = err
	}
}

// automationActions are an automation's buttons on the board. A running
// automation cannot be started again, and deleting asks for a second
// click, since nothing brings an automation back.
func (a *App) automationActions(c *ui.Context, k *colors, item automation.Automation, running bool) {
	if running {
		boardIconButton(c, k, "automation-run-"+item.ID, "play", "运行中", buttonDisabled)
	} else if boardIconButton(c, k, "automation-run-"+item.ID, "play", "立即运行", buttonPrimary).Clicked() {
		if exe, err := goalExecutable(); err != nil {
			a.automations.err = err.Error()
		} else {
			a.runAutomation(item.ID, exe)
		}
	}
	toggle, toggleIcon := "暂停", "pause"
	if !item.Enabled {
		toggle, toggleIcon = "恢复", "play"
	}
	if boardIconButton(c, k, "automation-toggle-"+item.ID, toggleIcon, toggle, buttonSecondary).Clicked() {
		a.changeAutomation(func(dir string) error {
			item.Enabled = !item.Enabled
			return automation.Save(dir, item)
		})
	}
	if boardIconButton(c, k, "automation-edit-"+item.ID, "pencil", "编辑", buttonSecondary).Clicked() {
		a.openEditAutomation(item)
	}
	v := &a.automations
	if v.removing == item.ID && time.Since(v.removeAsked) < removeConfirmWindow {
		c.After(removeConfirmWindow - time.Since(v.removeAsked))
		// Removing keeps the automation's sessions open.
		if boardIconButton(c, k, "automation-remove-"+item.ID, "trash-2", "确认删除", buttonConfirm).Clicked() {
			v.removing = ""
			a.changeAutomation(func(dir string) error { return automation.Remove(dir, item.ID) })
		}
		return
	}
	if iconButton(c.Key("automation-remove-"+item.ID), k, "trash-2", "删除", 30, 14).Tooltip("删除任务").Clicked() {
		v.removing, v.removeAsked = item.ID, time.Now()
		c.Invalidate()
	}
}

// automationsToggle sits beside the files button in both title bars.
func (a *App) automationsToggle(c *ui.Context, k *colors) {
	shown := a.boardShown
	if compactTitleButton(c.Key("automations-toggle"), k, "clock", "任务看板", shown).Tooltip("任务看板").Clicked() {
		if shown {
			a.hideBoard()
		} else {
			a.openBoard("")
		}
		c.Invalidate()
	}
}

// sidebarAutomations lists the schedule above the project sessions, with
// each run's state, so a background run is visible at a glance.
func (a *App) sidebarAutomations(c *ui.Context, k *colors) {
	v := &a.automations
	a.refreshAutomations(c, 5*time.Second)
	c.After(5 * time.Second)
	ui.Row(c.Key("automations-heading")).FillWidth().Height(34).Shrink(0).Padding(0, 10).
		BorderWidth(1, 0, 0, 0).BorderColor(k.headerBorder).AlignItems(ui.Center).Children(func() {
		toggle := ui.ButtonBase(c.Key("toggle-automations")).Label("自动化分组").Expanded(!v.collapsed).
			Focusable().Grow(1).MinWidth(0).Height(33).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer)
		toggle.Children(func() {
			arrow := "chevron-down"
			if v.collapsed {
				arrow = "chevron-right"
			}
			ui.Icon(c, icon(arrow)).Size(12, 12).TextColor(k.iconMuted).Shrink(0)
			ui.Text(c, "自动化").FontSize(10.5).FontWeight(600).TextColor(k.textFaint).Grow(1)
		})
		if toggle.Clicked() {
			v.collapsed = !v.collapsed
			c.Invalidate()
		}
		if iconButton(c.Key("new-automation"), k, "plus", "新建任务", 23, 12).Tooltip("新建任务").Clicked() {
			a.openGoal(nil)
		}
	})
	if v.collapsed {
		return
	}
	if len(v.list) == 0 {
		ui.Text(c, "点 + 描述任务，agent 会帮你设置，如「每小时检查系统错误」").FontSize(10.5).TextColor(k.textFaint).Padding(2, 12, 8, 12).FillWidth().Wrap().Shrink(0)
		return
	}
	now := time.Now()
	ui.Scroll(c.Key("sidebar-automations")).Label("自动化列表").MaxHeight(160).Shrink(0).FillWidth().Padding(0, 8, 6, 8).Children(func() {
		for _, item := range v.list {
			run := v.runs[item.ID]
			tooltip := item.Name() + "\n" + automationSchedule(item, run, now) + "\n" + displayDir(item.Dir)
			if result := automationResult(run); result != "" {
				tooltip += "\n" + result
			}
			b := ui.ButtonBase(c.Key("sidebar-automation-"+item.ID)).Label("自动化 "+item.Name()).Tooltip(tooltip).
				FillWidth().Height(30).Padding(0, 6).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Radius(4)
			if b.Hovered() || b.FocusVisible() {
				b.Background(k.hover)
			}
			b.Children(func() {
				ui.Text(c, "●").FontSize(9).TextColor(automationColor(k, item, run)).Shrink(0)
				ui.Text(c, item.Name()).FontSize(11).TextColor(k.textMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				ui.Text(c, automationWhen(item, run, now)).FontSize(9.5).TextColor(k.textFaint).Shrink(0)
			})
			if b.Clicked() {
				a.openBoard(item.ID)
			}
		}
	})
}

// stateColor tells a run's state at a glance: blue runs, green met its
// goal, red did not or was cut off, amber was skipped, grey has not run.
func stateColor(k *colors, state string) ui.Color {
	switch state {
	case "running":
		return ui.Hex("#3b82f6")
	case "done":
		return ui.Hex("#22c55e")
	case "failed", "interrupted":
		return ui.Hex("#ef4444")
	case "skipped":
		return ui.Hex("#f59e0b")
	}
	return k.textFaint
}

// automationColor is grey for a paused automation, else its last run's.
func automationColor(k *colors, item automation.Automation, run automation.Run) ui.Color {
	if !item.Enabled {
		return k.textFaint
	}
	// A finished run's own result can report trouble, as the board shows.
	_, tone := runVerdict(k, run)
	return tone
}

func automationWhen(item automation.Automation, run automation.Run, now time.Time) string {
	switch {
	case !item.Enabled:
		return "已暂停"
	case run.State == "running":
		return "运行中"
	}
	next := item.Next(run, now)
	if item.Once && next.IsZero() {
		return "已运行"
	}
	if !next.After(now) {
		return "即将"
	}
	if y, m, d := next.Local().Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return next.Local().Format("15:04")
	}
	return next.Local().Format("01-02 15:04")
}

func everyLabel(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		if d == 24*time.Hour {
			return "每天"
		}
		return fmt.Sprintf("每 %d 天", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		if d == time.Hour {
			return "每小时"
		}
		return fmt.Sprintf("每 %d 小时", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("每 %d 分钟", d/time.Minute)
	}
	return "每 " + d.String()
}

func automationSchedule(item automation.Automation, run automation.Run, now time.Time) string {
	agent := ""
	if len(item.Agent) > 0 {
		agent = " · " + item.Agent[0]
	}
	next := "已暂停"
	if item.Enabled {
		switch at := item.Next(run, now); {
		case item.Once && at.IsZero():
			next = "已运行"
		case at.After(now):
			next = "下次 " + at.Local().Format("01-02 15:04")
		default:
			next = "即将运行"
		}
	}
	return intervalLabel(item) + agent + " · " + next
}

func automationResult(run automation.Run) string {
	label := runStateLabels[run.State]
	if run.State != "running" && label != "" {
		label = "上次" + label
	}
	if label == "" {
		return ""
	}
	label += " · " + run.Started.Local().Format("01-02 15:04")
	if run.Message != "" {
		label += " · " + run.Message
	}
	return label
}
