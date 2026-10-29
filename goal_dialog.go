package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"retty/internal/agents"
	"retty/internal/automation"
	"retty/internal/controlcli"
	"retty/internal/rex"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// goalSetup is the task dialog. A new task goes to an agent, which reads
// the request and sets it up with Retty's commands. An existing agent pane
// takes a goal driven by `retty goal`, and an automation is edited in place.
type goalSetup struct {
	open bool
	// target is the agent pane to drive; nil starts a new agent.
	target              *Pane
	text, check, rounds string
	agent, err          string
	// name is an edited automation's title.
	name string
	// dir is where an edited automation's agent works. dirEpoch renews the
	// input after the folder picker sets it.
	dir      string
	dirEpoch int
	// repeat is an edited automation's interval, or "custom" for custom.
	repeat, custom string
	// editing is the automation being changed; agentArgv keeps its agent's
	// own arguments while the agent stays the same.
	editing   *automation.Automation
	agentArgv []string
}

func (g *goalSetup) setInterval(every time.Duration) {
	for _, o := range repeatOptions {
		if d, err := time.ParseDuration(o.value); err == nil && d == every {
			g.repeat = o.value
			return
		}
	}
	g.repeat, g.custom = "custom", shortDuration(every)
}

// shortDuration writes 2h or 45m, as a person types an interval.
func shortDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

var repeatOptions = []struct{ value, label string }{
	{"15m", "每 15 分钟"}, {"1h", "每小时"}, {"24h", "每天"}, {"custom", "自定义"},
}

func (g *goalSetup) interval() (string, error) {
	// A one-off task keeps running a single time.
	if g.editing != nil && g.editing.Once {
		return "", nil
	}
	if g.repeat != "custom" {
		return g.repeat, nil
	}
	if every := strings.TrimSpace(g.custom); every != "" {
		return every, nil
	}
	return "", errors.New("请填写间隔，例如 30m、2h、24h")
}

// workDir is the chosen directory, which must exist: a run started in a
// missing one would fail every interval.
func (g *goalSetup) workDir() (string, error) {
	dir := strings.TrimSpace(g.dir)
	if dir == "" {
		dir = "~"
	}
	if rest, ok := strings.CutPrefix(dir, "~"); ok && (rest == "" || rest[0] == '/') {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home + rest
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("目录不存在：%s", dir)
	}
	return dir, nil
}

// agentCommand keeps an edited automation's own agent arguments, as
// codex --no-daemon, unless another agent was chosen.
func (g *goalSetup) agentCommand() []string {
	if len(g.agentArgv) > 0 && filepath.Base(g.agentArgv[0]) == g.agent {
		return slices.Clone(g.agentArgv)
	}
	return []string{g.agent}
}

// displayDir shortens the home directory to ~, as a shell prompt does.
func displayDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dir == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(dir, home+"/"); ok {
			return "~/" + rest
		}
	}
	return dir
}

// appExecutable is this binary at its real path, which launchd and runner
// sessions start: the CLI link resolves to the app.
func appExecutable() string {
	exe, _ := controlcli.Executable()
	return exe
}

// syncAutomations installs the minute timer while any automation is enabled.
func syncAutomations(dir string) error {
	return automation.Sync(dir, appExecutable())
}

// goalExecutable is this binary, which handles `goal` and `automation` like
// the headless CLI. Tests replace it so no session starts the test binary.
var goalExecutable = os.Executable

// setupAgentCommand starts the agent that sets up a new task. Tests replace
// it with a stand-in that records its prompt.
var setupAgentCommand = func(agent string) []string { return []string{agent} }

var (
	cmdGoalNew    = command{ID: "goal.new", Title: "新建任务…", Run: func(a *App) { a.openGoal(nil) }}
	cmdGoalAttach = command{ID: "goal.attach", Terminal: true, Title: "为当前 agent 设定目标…", Run: func(a *App) {
		if t := a.tab(); t != nil {
			a.openGoal(t.Focus)
		}
	}}
)

func (a *App) openGoal(target *Pane) {
	if a.currentHost() != nil {
		a.err = "任务目前只支持本机会话"
		return
	}
	if target != nil {
		info := target.terminal().info
		if _, ok := agents.Detect(info.Program, info.Args); !ok {
			a.err = "当前窗格没有运行 Claude Code 或 Codex"
			return
		}
	}
	next := &goalSetup{open: true, target: target, rounds: "8", agent: "claude"}
	// The check and agent usually stay the same between tasks.
	if previous := a.goalSetup; previous != nil && previous.editing == nil {
		next.check, next.agent = previous.check, previous.agent
	}
	a.goalSetup = next
	a.paletteOpen = false
	a.focusReq = nil // the dialog takes the focus
}

// openEditAutomation fills the dialog with an automation to change.
func (a *App) openEditAutomation(item automation.Automation) {
	g := &goalSetup{open: true, editing: &item, name: item.Title, text: item.Goal, check: item.Check, dir: displayDir(item.Dir),
		rounds: strconv.Itoa(item.Rounds), agent: "claude", agentArgv: item.Agent}
	if len(item.Agent) > 0 {
		g.agent = filepath.Base(item.Agent[0])
	}
	if !item.Once {
		g.setInterval(item.Interval())
	}
	a.goalSetup = g
	a.focusReq = nil
}

// knownProjectDirs are the projects a request may name: this computer's
// open terminals' directories and the sidebar's project sessions.
func (a *App) knownProjectDirs() []string {
	var dirs []string
	add := func(dir string) {
		if dir != "" && !slices.Contains(dirs, dir) && len(dirs) < 40 {
			dirs = append(dirs, dir)
		}
	}
	for t, p := range a.allPanes() {
		if t.Host == nil && p.terminal() != nil {
			add(p.directory())
		}
	}
	if h := a.agentHistory; h != nil {
		for _, dir := range h.projectDirs {
			add(dir)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// setupPrompt hands a request to an agent, which decides the directory,
// interval, task and check with the tools it already has, and sets them up
// with Retty's own commands. Running those asks the person first, so each
// task is seen before it exists.
func setupPrompt(request, exe, agent, current string, known []string) string {
	retty := controlcli.ShellWord(exe)
	var b strings.Builder
	fmt.Fprintf(&b, "%s。只负责设置，不要自己执行任务内容。\n\n需求：%s\n\n", setupPromptLead, request)
	if current != "" {
		fmt.Fprintf(&b, "用户提出需求时所在的目录：%s（需求说“这个项目”时指它）。你在主目录中启动，任务本身在你确定的目录中运行。\n\n", current)
	}
	b.WriteString("1. 确定任务在哪个目录执行，必须是已存在的绝对路径。需求提到的项目可先在下方已知项目中找，找不到就在文件系统中查找；与具体项目无关的任务用主目录。\n")
	b.WriteString("2. 判断是否需要重复执行。需要时确定间隔，写成 30m、2h、24h 这样的格式，至少 1m。Retty 只支持固定间隔、不支持具体时刻；需求指定了时刻时，选最接近的间隔并在最后说明。\n")
	b.WriteString("3. 把需求改写成交给执行 agent 的任务说明，去掉调度和目录信息，写清楚要做什么以及如何报告结果；另起一个 12 字以内的简短标题，概括任务，不含目录和频率，例如「线上错误日志巡检」。\n")
	b.WriteString("4. 只有能用一条命令客观判断任务完成时（退出码 0 表示达成）才设置检查命令，否则不设。\n")
	fmt.Fprintf(&b, "5. 重复执行的任务运行：\n   %s automation add --dir 目录 --every 间隔 --title '标题' --goal '任务说明' [--check '检查命令'] -- %s\n", retty, agent)
	fmt.Fprintf(&b, "   只执行一次的任务运行：\n   %s automation add --dir 目录 --once --title '标题' --goal '任务说明' [--check '检查命令'] -- %s\n", retty, agent)
	b.WriteString("   不要用 new 或直接启动 agent 来运行任务：那样会新开标签页，任务看板也看不到它。\n")
	fmt.Fprintf(&b, "6. 用 %s automation list 确认已保存，然后用一两句话说明设置了什么（标题、目录、间隔或一次性、任务、检查命令），结束。\n", retty)
	if len(known) > 0 {
		b.WriteString("\n已知项目：\n")
		for _, dir := range known {
			fmt.Fprintf(&b, "- %s\n", dir)
		}
	}
	return b.String()
}

// startSetup starts the setup agent in the home directory, since no
// project is its own, and shows it on the task board, without a tab. The
// directory the person is in is named in the prompt.
func (a *App) startSetup() error {
	g := a.goalSetup
	text := strings.TrimSpace(g.text)
	if text == "" {
		return errors.New("请描述要做的任务")
	}
	exe, err := goalExecutable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	prompt := setupPrompt(text, exe, g.agent, a.currentDir(), a.knownProjectDirs())
	info, err := a.client.Create(rex.CreateOptions{Dir: home, Command: append(setupAgentCommand(g.agent), prompt), Cols: 120, Rows: 36})
	if err != nil {
		return err
	}
	a.hideSession(info.ID)
	a.setupSessions = append(a.setupSessions, setupSession{info: info, request: text, added: time.Now()})
	a.openBoard(setupPrefix + info.ID)
	return nil
}

// pickGoalDir shows the native folder picker off the UI thread.
func (a *App) pickGoalDir(g *goalSetup) {
	win, start := a.win, g.dir
	if dir, err := g.workDir(); err == nil {
		start = dir
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "选择任务目录", ButtonLabel: "选择", DefaultPath: start, Directory: true})
		a.post(func() {
			if err != nil {
				g.err = err.Error()
			} else if len(paths) == 1 {
				g.dir, g.err = displayDir(paths[0]), ""
				g.dirEpoch++
			}
		})
		if win != nil {
			win.Invalidate()
		}
	}()
}

// submit sets up a new task, saves an edited automation, or drives an
// existing agent pane with `retty goal` in a pane below it.
func (a *App) submit() error {
	g := a.goalSetup
	if g.target == nil && g.editing == nil {
		return a.startSetup()
	}
	text, check := strings.TrimSpace(g.text), strings.TrimSpace(g.check)
	if g.editing != nil {
		if text == "" {
			return errors.New("请填写目标")
		}
		every, err := g.interval()
		if err != nil {
			return err
		}
		dir, err := g.workDir()
		if err != nil {
			return err
		}
		next := *g.editing
		next.Title, next.Goal, next.Check, next.Dir, next.Agent, next.Every = strings.TrimSpace(g.name), text, check, dir, g.agentCommand(), every
		return a.updateAutomation(*g.editing, next)
	}
	if g.target.closed || g.target.terminal() == nil {
		return errors.New("agent 窗格已关闭")
	}
	exe, err := goalExecutable()
	if err != nil {
		return err
	}
	argv := []string{exe, "goal", "--rounds", g.rounds}
	if text != "" {
		argv = append(argv, "--goal", text)
	}
	if check != "" {
		argv = append(argv, "--check", check)
	}
	old := g.target
	argv = append(argv, "--target", old.terminal().SID)
	cols, rows := 80, 12
	if old.terminalView() != nil {
		cols, _ = old.terminal().term.Size()
	}
	p := a.newOptionsPane(rex.CreateOptions{Dir: old.directory(), Command: argv, Cols: cols, Rows: rows})
	a.placePane(old, p, true)
	return nil
}

func (a *App) goalDialog(c *ui.Context, k *colors) {
	g := a.goalSetup
	if g == nil || !g.open {
		return
	}
	setup := g.target == nil && g.editing == nil
	ui.DialogBase(c.Key("goal"), &g.open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		width, _ := c.Size()
		panel.Column().Width(480).MaxWidth(width - 32).Padding(20).Gap(12).Radius(12).Background(k.panel).Children(func() {
			title, placeholder := "新建任务", "描述要做什么，例如：每小时检查 ~/work/subapi 的错误日志并报告"
			switch {
			case g.editing != nil:
				title, placeholder = "编辑自动化", "交给执行 agent 的任务说明"
			case g.target != nil:
				title, placeholder = "为当前 agent 设定目标", "可留空：继续当前任务，直到检查通过"
			}
			ui.Text(c, title).FontSize(16).FontWeight(600)
			if g.editing != nil {
				ui.TextInput(c.Key("goal-name"), &g.name).Label("标题").Placeholder("简短标题，留空则取说明首句").FillWidth().FontSize(13)
			}
			ui.TextArea(c.Key("goal-text"), &g.text).Label("目标").Placeholder(placeholder).Height(88).FillWidth().FontSize(13).AutoFocus()
			if !setup {
				ui.TextInput(c.Key("goal-check"), &g.check).Label("检查命令").Placeholder("可选：检查命令，退出码 0 即达成，如 go test ./...").FillWidth().FontSize(13)
			}
			if g.editing != nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "目录").FontSize(13).TextColor(k.textMuted)
					ui.TextInput(c.Key(fmt.Sprint("goal-dir-", g.dirEpoch)), &g.dir).Label("目录").Placeholder("agent 在此目录中工作").Grow(1).MinWidth(0).FontSize(13)
					if ui.Button(c, "选择…").Clicked() {
						a.pickGoalDir(g)
					}
				})
			}
			if g.target == nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					for _, name := range []string{"claude", "codex"} {
						b := ui.Button(c.Key("goal-agent-"+name), name)
						if g.agent == name {
							b.Background(k.panelSel)
						}
						if b.Clicked() {
							g.agent = name
						}
					}
				})
			}
			if g.editing != nil && !g.editing.Once {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "重复").FontSize(13).TextColor(k.textMuted)
					for _, o := range repeatOptions {
						b := ui.Button(c.Key("goal-repeat-"+o.value), o.label)
						if g.repeat == o.value {
							b.Background(k.panelSel)
						}
						if b.Clicked() {
							g.repeat = o.value
						}
					}
					if g.repeat == "custom" {
						ui.TextInput(c.Key("goal-every"), &g.custom).Label("间隔").Placeholder("如 2h").Width(72).FontSize(13)
					}
				})
			}
			var hint string
			switch {
			case setup:
				hint = "新开一个设置用的 agent：它理解你的描述，确定项目目录、是否定时和间隔、检查方式，再用 Retty 命令创建任务；任务本身在它确定的目录中运行。运行命令前是否请你确认，取决于该 agent 的权限设置。"
			case g.editing != nil:
				hint = "保存后按新的间隔运行；修改目录或 agent 后，下次运行新开一个 agent。"
			case strings.TrimSpace(g.check) == "":
				hint = "没有检查命令时，agent 完成一轮即结束。你在 agent 中输入会暂停目标。"
			default:
				hint = "每轮结束后运行检查；未通过就把失败输出交回 agent。你在 agent 中输入会暂停目标。"
			}
			ui.Text(c, hint).FontSize(12).TextColor(k.textMuted)
			if g.err != "" {
				ui.Text(c, g.err).FontSize(13)
			}
			ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					g.open = false
				}
				start := "开始"
				switch {
				case setup:
					start = "交给 agent"
				case g.editing != nil:
					start = "保存"
				}
				if ui.Button(c, start).Clicked() {
					if err := a.submit(); err != nil {
						g.err = err.Error()
					} else {
						g.open, g.err = false, ""
					}
				}
			})
		})
	})
}
