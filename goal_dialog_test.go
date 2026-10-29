package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

func TestNewTaskHandsRequestToAgent(t *testing.T) {
	a, tt := newTestApp(t)
	// A stand-in agent records the arguments it was started with.
	dir := t.TempDir()
	recorded := filepath.Join(dir, "args")
	agent := filepath.Join(dir, "agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nfor arg; do printf '%s\\0' \"$arg\"; done > "+recorded+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousAgent, previousExe := setupAgentCommand, goalExecutable
	setupAgentCommand = func(name string) []string { return []string{agent, name} }
	goalExecutable = func() (string, error) { return "/Apps/Retty Test/Retty", nil }
	t.Cleanup(func() { setupAgentCommand, goalExecutable = previousAgent, previousExe })
	// A project is known once a terminal works in it.
	project := filepath.Join(t.TempDir(), "subapi")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	a.newTab(project)

	tabs := len(a.tabs)
	a.openGoal(nil)
	tt.Frame()
	// No directory picker, interval or check: the agent decides those.
	if tt.HasText("选择…") || tt.HasText("每小时") || tt.HasText("可选：检查命令") {
		t.Fatal("a new task asks for what the agent decides")
	}
	if err := tt.Click("交给 agent"); err != nil {
		t.Fatal(err)
	}
	if len(a.tabs) != tabs || a.goalSetup.err != "请描述要做的任务" {
		t.Fatal("an empty request started an agent", a.goalSetup.err)
	}
	if err := tt.Click("目标"); err != nil {
		t.Fatal(err)
	}
	tt.Type("每小时检查 subapi 的错误日志并报告")
	if err := tt.Click("codex"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("交给 agent"); err != nil {
		t.Fatal(err)
	}
	// The setup agent gets no tab: the task board shows it.
	if len(a.setupSessions) != 1 || a.goalSetup.open {
		t.Fatal("setup agent not started", a.goalSetup.err)
	}
	setup := a.setupSessions[0].info
	if len(a.tabs) != tabs || !a.boardShown || a.taskBoard.selected != setupPrefix+setup.ID || a.sessionInTab(setup.ID) {
		t.Fatal("setup agent not on the board")
	}
	infos, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]rex.SessionInfo{}
	for _, in := range infos {
		byID[in.ID] = in
	}
	a.syncSessionTabs(byID)
	if len(a.tabs) != tabs {
		t.Fatal("setup agent adopted as a tab")
	}
	// The setup agent is not the task: it starts at home, not in the project.
	if dir := byID[setup.ID].Dir; dir != "" && dir != os.Getenv("HOME") {
		t.Fatal("setup agent started in", dir)
	}
	tt.Frame()
	if !tt.HasText("正在设置任务") || !tt.HasText("结束设置") {
		t.Fatal("setup detail not shown")
	}
	var data []byte
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, _ = os.ReadFile(recorded); len(data) > 0 {
			break
		}
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(args) != 2 || args[0] != "codex" {
		t.Fatalf("setup agent arguments = %q", args)
	}
	real, _ := filepath.EvalSymlinks(project)
	prompt := args[1]
	for _, want := range []string{
		"需求：每小时检查 subapi 的错误日志并报告",
		"'/Apps/Retty Test/Retty' automation add --dir 目录 --every 间隔",
		"-- codex\n",
		"'/Apps/Retty Test/Retty' automation add --dir 目录 --once --title",
		"- " + real + "\n",
		"用户提出需求时所在的目录：" + real,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("setup prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestTaskBoardShowsHistoryLiveTerminalAndActions(t *testing.T) {
	a, tt := newTestApp(t)
	dir := rex.Dir()
	// The title bar shows the board over the tab content, opening no tab.
	tabs := len(a.tabs)
	if err := tt.Click("任务看板"); err != nil || !a.boardShown || len(a.tabs) != tabs {
		t.Fatal("title bar did not open the board", err)
	}
	tt.Frame()
	if !tt.HasText("还没有任务") {
		t.Fatal("empty board hint missing")
	}
	// Choosing a tab shows it in place of the board.
	a.selectTab(0)
	if a.boardShown {
		t.Fatal("board still shown over a chosen tab")
	}
	a.openBoard("")
	if len(a.tabs) != tabs || !a.boardShown {
		t.Fatal("board opened a tab")
	}

	item, err := automation.New(automation.Automation{Goal: "每小时检查 CI", Every: "1h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = automation.Save(dir, item); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = automation.FinishRun(automation.RunPath(dir, item.ID), func(r *automation.Run) {
		*r = automation.Run{Started: now.Add(-2 * time.Hour), Finished: now.Add(-2*time.Hour + 90*time.Second), State: "done", Rounds: 1, Summary: "没有发现新错误"}
	}); err != nil {
		t.Fatal(err)
	}
	// The current run's agent is a live session; the board shows its screen.
	live, err := a.client.Create(rex.CreateOptions{Dir: "/tmp", Command: []string{"/bin/sh", "-c", "printf 'board-live-output\\n'; exec sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.Kill(live.ID)
	if err = automation.UpdateRun(automation.RunPath(dir, item.ID), func(r *automation.Run) {
		*r = automation.Run{Started: now, Agent: live.ID, State: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	a.automations.loaded = time.Time{}
	board := a.taskBoard
	waitFor(t, tt, "board run", func() bool {
		return tt.HasText("没有发现新错误") && board.term != nil && board.termSID == live.ID && strings.Contains(board.term.Text(), "board-live-output")
	})
	// The preview reads a snapshot: it never attaches to the session.
	if infos, err := a.client.List(); err == nil {
		for _, in := range infos {
			if in.ID == live.ID && in.Attached != 0 {
				t.Fatal("preview attached to the session", in.Attached)
			}
		}
	}
	if !tt.HasText("当前执行") || !tt.HasText("执行历史") || !tt.HasText("在标签页中打开") {
		t.Fatal("board sections missing")
	}
	// The run's session gets no tab of its own: closing one would end it.
	infos, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]rex.SessionInfo{}
	for _, in := range infos {
		byID[in.ID] = in
	}
	a.syncSessionTabs(byID)
	if len(a.tabs) != tabs {
		t.Fatal("automation session opened as a tab")
	}
	// Opened in a tab on request, it leaves the board, without ending.
	if err := tt.Click("在标签页中打开"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != tabs+1 || a.boardShown || a.tab().Focus.terminal() == nil || a.tab().Focus.terminal().SID != live.ID {
		t.Fatal("agent not opened in a tab")
	}
	a.openBoard("")
	tt.Frame()
	if !tt.HasText("已在标签页中打开") {
		t.Fatal("board does not say where the agent is")
	}

	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	if all, _ := automation.List(dir); len(all) != 1 || all[0].Enabled {
		t.Fatal("pause not saved", all)
	}
	tt.Frame()
	// Deleting asks for a second click before it removes anything.
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if all, _ := automation.List(dir); len(all) != 1 {
		t.Fatal("automation removed without confirmation", all)
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if all, _ := automation.List(dir); len(all) != 0 || automation.History(dir, item.ID, 10) != nil {
		t.Fatal("automation or its history not removed", all)
	}
	if err := tt.Click("新建任务"); err != nil || a.goalSetup == nil || !a.goalSetup.open {
		t.Fatal("new task did not open the task dialog", err)
	}
}

func TestAutomationEditKeepsIdentityAndStartsNewAgentForNewDirectory(t *testing.T) {
	a, tt := newTestApp(t)
	dir := rex.Dir()
	item, err := automation.New(automation.Automation{Goal: "keep CI green", Every: "1h", Dir: "/tmp", Agent: []string{"/opt/homebrew/bin/codex", "--no-daemon"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = automation.Save(dir, item); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-10 * time.Minute)
	if err = automation.UpdateRun(automation.RunPath(dir, item.ID), func(r *automation.Run) {
		r.Started, r.Agent, r.State = started, "agent-session", "done"
	}); err != nil {
		t.Fatal(err)
	}
	a.openBoard(item.ID)
	tt.Frame()
	if !tt.HasText("/tmp") {
		t.Fatal("automation directory not listed")
	}
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	g := a.goalSetup
	if g == nil || g.editing == nil || g.text != "keep CI green" || g.repeat != "1h" || g.agent != "codex" || !tt.HasText("编辑自动化") || tt.HasText("不重复") {
		t.Fatal("edit dialog not filled", g)
	}
	if err := tt.Click("每天"); err != nil {
		t.Fatal(err)
	}
	// As the folder picker does: set the value and renew the input.
	project := t.TempDir()
	g.dir = project
	g.dirEpoch++
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if g.open || !a.boardShown || a.taskBoard.selected != item.ID {
		t.Fatal("saving did not return to the board", g.err)
	}
	saved, err := automation.Find(dir, item.ID)
	if err != nil || saved.Every != "24h0m0s" || saved.Dir != project || !saved.Created.Equal(item.Created) || !saved.Enabled || !slices.Equal(saved.Agent, item.Agent) {
		t.Fatal("edit not saved in place", saved, err)
	}
	// A new project starts a new conversation, but the edit does not make
	// the automation due at once.
	if last := automation.LastRun(dir, item.ID); last.Agent != "" || !last.Started.Equal(started) {
		t.Fatal("run record after editing the directory", last)
	}
}

func TestAutomationEditRejectsMissingDirectory(t *testing.T) {
	a, tt := newTestApp(t)
	item, err := automation.New(automation.Automation{Goal: "整理 issue", Every: "24h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a.openEditAutomation(item)
	a.goalSetup.dir = "/nonexistent/retty-goal"
	a.goalSetup.dirEpoch++
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if a.goalSetup.err != "目录不存在：/nonexistent/retty-goal" || !a.goalSetup.open {
		t.Fatal("missing directory accepted", a.goalSetup.err)
	}
}

func TestGoalAttachRequiresAgentPane(t *testing.T) {
	a, _ := newTestApp(t)
	cmdGoalAttach.Run(a)
	if a.goalSetup != nil || !strings.Contains(a.err, "Claude Code 或 Codex") {
		t.Fatal("goal attached to a plain shell", a.err)
	}
}
