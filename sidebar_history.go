package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agenthistory"
	"retty/internal/agents"
	"retty/internal/rex"
)

type sidebarAgentHistory struct {
	reader   agenthistory.Reader
	loading  bool
	loaded   bool
	nextRead time.Time
	err      string
	sessions map[string]agenthistory.Session
	projects []string
	children map[string][]string
	outline  ui.OutlineState[string]
	selected int
	// History identity -> PTY before lifecycle hooks arrive.
	opened map[string]string
	// Preparation in progress, shared by repeated clicks.
	resuming map[string]bool

	prepareCodex func(agenthistory.Session, string, string, string, []string) (agenthistory.Session, error) // fixture/native preparation
}

func (a *App) refreshAgentHistory(c *ui.Context, force bool) {
	if a.agentHistory == nil {
		a.agentHistory = &sidebarAgentHistory{selected: -1}
	}
	h := a.agentHistory
	if h.loading || !force && c.Now().Before(h.nextRead) {
		return
	}
	codex, e1 := agents.ConfigDir("codex")
	claude, e2 := agents.ConfigDir("claude")
	if e1 != nil || e2 != nil {
		h.loaded, h.err = true, "无法读取本机 Agent 配置目录"
		h.nextRead = c.Now().Add(15 * time.Second)
		return
	}
	codex, e1 = filepath.Abs(codex)
	claude, e2 = filepath.Abs(claude)
	if e1 != nil || e2 != nil {
		h.loaded, h.err = true, "无法解析本机 Agent 配置目录"
		h.nextRead = c.Now().Add(15 * time.Second)
		return
	}
	h.loading = true
	h.nextRead = c.Now().Add(15 * time.Second)
	update, services := a.desktopDispatcher(), c.Services()
	go func() {
		home, _ := os.UserHomeDir()
		sources := agenthistory.DiscoverCodexSources(home, codex)
		sessions, err := h.reader.ReadSources(sources, claude)
		update(func() {
			h.loading, h.loaded, h.err = false, true, ""
			if a.quitting || a.agentHistory != h {
				return
			}
			if err != nil {
				h.err = err.Error()
			}
			h.setSessions(sessions)
		})
		services.Invalidate()
	}()
}

func (h *sidebarAgentHistory) setSessions(sessions []agenthistory.Session) {
	// Each project's first entry is its latest activity, regardless of the
	// reader's input order. Clone so refreshes don't reorder callers' data.
	sessions = slices.Clone(sessions)
	slices.SortStableFunc(sessions, func(a, b agenthistory.Session) int {
		return b.Updated.Compare(a.Updated)
	})
	h.projects, h.children = nil, make(map[string][]string)
	h.sessions = make(map[string]agenthistory.Session)
	// Sessions are newest first, so projects and their sessions both follow
	// last activity. Full directory paths keep same-name projects distinct.
	for _, s := range sessions {
		dir := filepath.Clean(s.Dir)
		if hiddenHistoryProject(dir) {
			continue
		}
		project, key := "project:"+dir, s.Key()
		if _, ok := h.children[project]; !ok {
			h.projects = append(h.projects, project)
		}
		h.children[project] = append(h.children[project], key)
		h.sessions[key] = s
	}
}

func hiddenHistoryProject(dir string) bool {
	// macOS also records temporary projects as /var/folders rather than
	// their canonical /private/var/folders spelling.
	for _, root := range []string{"/tmp", "/private", "/var/folders"} {
		if dir == root || strings.HasPrefix(dir, root+"/") {
			return true
		}
	}
	return false
}

func (a *App) sidebarProjectSessions(c *ui.Context, k *colors) {
	a.refreshAgentHistory(c, false)
	h := a.agentHistory
	c.After(15 * time.Second)
	ui.Row(c.Key("project-sessions-heading")).FillWidth().Height(34).Shrink(0).Padding(0, 10).
		BorderWidth(1, 0, 0, 0).BorderColor(k.headerBorder).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "项目会话").FontSize(10.5).FontWeight(600).TextColor(k.textFaint).Grow(1)
		if iconButton(c.Key("refresh-history"), k, "rotate-ccw", "刷新项目会话", 23, 12).Disabled(h.loading).Tooltip("刷新本机 Codex / Claude Code 历史").Clicked() {
			a.refreshAgentHistory(c, true)
		}
	})
	if h.err != "" {
		ui.Text(c, "部分会话未能读取").FontSize(10.5).TextColor(k.textMuted).Padding(4, 12).Tooltip(h.err).Shrink(0)
	}
	if len(h.projects) == 0 {
		message := "暂无 Codex / Claude Code 会话"
		if !h.loaded {
			message = "正在读取会话…"
		}
		ui.Text(c, message).FontSize(10.5).TextColor(k.textFaint).Padding(8, 12).FillWidth().Wrap()
		ui.Spacer(c)
		return
	}
	h.outline.Indent = 8
	h.outline.List.Selected = &h.selected
	h.outline.List.Label = func(i int) string {
		key := h.outline.Item(i)
		if s, ok := h.sessions[key]; ok {
			return programOf(s.Agent).Name + " · " + s.Title
		}
		return "项目 " + key[len("project:"):]
	}
	list := ui.Outline(c.Key("project-sessions"), &h.outline, h.projects, func(key string) []string {
		return h.children[key]
	}, func(key string) {
		a.sidebarHistoryRow(c, k, h, key)
	}).Grow(1).MinHeight(0).FillWidth().Label("项目会话列表")
	if list.Submitted() && h.selected >= 0 && h.selected < h.outline.Rows() {
		a.activateHistoryItem(c, h, h.outline.Item(h.selected))
	}
}

func (a *App) sidebarHistoryRow(c *ui.Context, k *colors, h *sidebarAgentHistory, key string) {
	s, session := h.sessions[key]
	label, glyph, color := "", "folder-open", k.iconMuted
	tooltip := ""
	if session {
		label, glyph = s.Title, programOf(s.Agent).Glyph
		color = programIconColor(c, programOf(s.Agent))
		tooltip = fmt.Sprintf("%s · %s\n%s\n%s\n打开会话；已打开时切换到原标签页", programOf(s.Agent).Name, s.Updated.Local().Format("2006-01-02 15:04"), s.Dir, s.ID)
		if s.Agent == "codex" {
			tooltip += "\n使用当前 provider 和登录账号继续；切换来源时保留原历史并创建续接分支"
			tooltip += "\n账号来源：" + s.AccountLabel + "\n配置目录：" + s.ConfigDir
			if s.Provider != "" {
				tooltip += "\nProvider：" + s.Provider
			}
		}
	} else {
		dir := key[len("project:"):]
		label, tooltip = filepath.Base(dir), dir
	}
	accessible := label
	if session {
		accessible = "恢复 " + programOf(s.Agent).Name + " 会话 " + label
	}
	b := ui.ButtonBase(c.Key(key)).Label(accessible).Tooltip(tooltip).Grow(1).MinWidth(0).Height(30).
		Padding(0, 2).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Radius(4)
	// Outline owns the full row's hover and selection background.
	b.Children(func() {
		programIcon(c, glyph).Size(14, 14).TextColor(color).Shrink(0)
		ui.Text(c, label).FontSize(11).TextColor(k.textMuted).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
		if !session {
			ui.Text(c, fmt.Sprint(len(h.children[key]))).FontSize(10).TextColor(k.textFaint).Shrink(0)
		}
	})
	if b.Clicked() {
		a.activateHistoryItem(c, h, key)
	}
}

func (a *App) activateHistoryItem(c *ui.Context, h *sidebarAgentHistory, key string) {
	if s, ok := h.sessions[key]; ok {
		// Add the tab after this build: the sidebar may still be iterating the
		// active tab's tree. Always target this computer, even from a remote tab.
		a.later(c, func() {
			if a.quitting {
				return
			}
			if s.Agent != "codex" && a.activateOpenHistory(h, s) {
				return
			}
			a.resumeHistorySession(h, s)
		})
	} else if h.outline.Open.Has(key) {
		h.outline.Open.Remove(key)
	} else {
		h.outline.Open.Add(key)
	}
	c.Invalidate()
}

func (a *App) resumeHistorySession(h *sidebarAgentHistory, s agenthistory.Session) {
	if h.resuming[s.Key()] {
		return
	}
	if h.resuming == nil {
		h.resuming = make(map[string]bool)
	}
	h.resuming[s.Key()] = true
	update, services := a.desktopDispatcher(), a.services
	agentName := programOf(s.Agent).Name
	go func() {
		target := s
		executable, pathEnv, err := rex.ResumeExecutable(s.Agent)
		if err != nil {
			err = fmt.Errorf("无法找到 %s 命令，请确认已安装且可在登录 shell 中运行：%w", agentName, err)
		} else if s.Agent == "codex" {
			var home string
			home, err = agents.ConfigDir("codex")
			if err == nil {
				prepare := h.prepareCodex
				if prepare == nil {
					prepare = agenthistory.ContinueCodex
				}
				target, err = prepare(s, executable, home, filepath.Join(rex.Dir(), "codex-continuations.json"), pathEnv)
			}
		}
		ready := err == nil
		var command, env []string
		if err == nil {
			command, env, err = target.Resume()
			if err == nil {
				command[0] = executable
				env = append(pathEnv, env...)
			}
		}
		update(func() {
			delete(h.resuming, s.Key())
			if a.quitting {
				return
			}
			if ready && a.activateOpenHistory(h, target) {
				return
			}
			if err != nil {
				a.err = err.Error()
				return
			}
			t := a.newLocalOptionsTab(rex.CreateOptions{Dir: target.Dir, Command: command, Env: env})
			t.Name = s.Title
			if t.Focus.SID != "" {
				if h.opened == nil {
					h.opened = make(map[string]string)
				}
				h.opened[target.Key()] = t.Focus.SID
			}
		})
		services.Invalidate()
	}()
}

func (a *App) activateOpenHistory(h *sidebarAgentHistory, s agenthistory.Session) bool {
	for t, p := range a.allPanes() {
		if t.Host != nil || p.host != nil || p.closed || p.info.Exited || p.SID == "" {
			continue
		}
		state := p.info.Agent
		if state.ID == s.Agent && state.ConfigDir != "" && !sameHistoryHome(state.ConfigDir, s.ConfigDir) {
			continue
		}
		// Older servers did not expose the config home. When several homes
		// contain the same ID, only a known launch may resolve that ambiguity.
		if state.ConfigDir == "" && h.opened[s.Key()] != p.SID && ambiguousHistoryHome(h, s) {
			continue
		}
		match := false
		if state.ID == s.Agent && state.SessionID != "" && state.Source != rex.ProgramStatusSource {
			// The user can switch conversations inside an existing CLI. Current
			// lifecycle identity takes precedence over its original launch argv.
			match = state.State != "" && state.SessionID == s.ID
		} else if h.opened[s.Key()] == p.SID && p.info.Shell == s.Agent {
			match = true
		} else if sessionProgramName(p.info) == s.Agent {
			match = historyResumeArgs(s, p.info.Args)
		}
		if match {
			if prefs.FilesOpen {
				a.toggleFiles()
			}
			return a.focusAgentPane(p.noticeKey())
		}
	}
	return false
}

func sameHistoryHome(a, b string) bool {
	if a, err := filepath.EvalSymlinks(a); err == nil {
		if b, err := filepath.EvalSymlinks(b); err == nil {
			return a == b
		}
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func ambiguousHistoryHome(h *sidebarAgentHistory, s agenthistory.Session) bool {
	for _, other := range h.sessions {
		if other.Agent == s.Agent && other.ID == s.ID && !sameHistoryHome(other.ConfigDir, s.ConfigDir) {
			return true
		}
	}
	return false
}

// Recognize explicit resume launches after a GUI reopen, even without hooks.
// Stop at unknown/positional input so prompt words cannot identify a session.
func historyResumeArgs(s agenthistory.Session, args []string) bool {
	if len(args) < 2 {
		return false
	}
	agent, ok := agents.Detect("", args)
	if !ok || agent.ID != s.Agent {
		return false
	}
	// Foreground argv may belong to node/bun/npx rather than the CLI itself.
	// Locate the entry point using the shared detector; never scan prompt text
	// for an agent name or a resume command.
	entry := 0
	for ; entry < len(args); entry++ {
		if _, ok := agents.Detect("", args[:entry+1]); ok {
			break
		}
	}
	for i := entry + 1; i < len(args); i++ {
		key, value, equal := strings.Cut(args[i], "=")
		if s.Agent == "codex" && key == "resume" || s.Agent == "claude" && (key == "--resume" || key == "-r") {
			if equal {
				return value == s.ID
			}
			return i+1 < len(args) && args[i+1] == s.ID
		}
		switch key {
		case "-C", "--cd", "-c", "--config", "-m", "--model", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "--remote", "--effort", "--permission-mode":
			// Claude -p/--print starts a noninteractive prompt, not a resume.
			if s.Agent == "claude" && key == "-p" {
				return false
			}
			if !equal {
				i++
			}
		case "--no-daemon", "--full-auto", "--verbose":
		default:
			return false
		}
	}
	return false
}
