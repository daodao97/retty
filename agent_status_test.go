package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/agents"
	"retty/internal/rex"
)

func withAgentState(p *Pane, agent, state string, revision uint64) rex.SessionInfo {
	in := p.terminal().info
	in.ID, in.Idle, in.Program, in.Exited = p.terminal().SID, false, agent, false
	in.Agent = rex.AgentState{ID: agent, SessionID: "thread-" + p.terminal().SID, State: state, Reason: "question", WaitRevision: revision, Updated: time.Now()}
	return in
}

func TestAgentNotificationsDeduplicateAndFocusCorrectSplit(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	tab := a.tab()
	left := tab.Focus
	a.split(false)
	right := tab.Focus
	a.newTab("/tmp")
	tt.Frame()
	var clicks []func()
	closed := 0
	a.agentNotify = func(opts mygo.NotificationOptions, click func()) func() {
		if !strings.Contains(opts.Title, "Codex") || !strings.Contains(opts.Title, "等待回答") {
			t.Errorf("incorrect notification: %+v", opts)
		}
		clicks = append(clicks, click)
		return func() { closed++ }
	}
	infos := map[string]rex.SessionInfo{
		left.terminal().SID:  withAgentState(left, "claude", agents.Running, 0),
		right.terminal().SID: withAgentState(right, "codex", agents.Waiting, 1),
	}
	a.apply(infos)
	a.apply(infos)
	if len(clicks) != 1 {
		t.Fatalf("duplicate notification: %d", len(clicks))
	}
	if p, s := tabAgentState(tab); p != right || s.State != agents.Waiting {
		t.Fatal("waiting pane did not take priority")
	}
	tab.setFocus(left)
	tab.Zoom = left
	a.settingsOpen, a.paletteOpen = true, true
	right.terminal().find.open = true
	clicks[0]()
	if a.tab() != tab || tab.Focus != right || tab.Zoom != nil || a.focusReq != right || a.settingsOpen || a.paletteOpen || right.terminal().find.open {
		t.Fatal("notification did not reveal and focus its split")
	}
	tt.Frame()
	if !tt.Focused("Terminal") {
		t.Fatal("clicking a notification did not return keyboard focus to the terminal")
	}
	a.apply(infos)
	if len(clicks) != 1 || paneAgentState(right).State != agents.Waiting {
		t.Fatal("viewing a waiting pane cleared state or repeated notification")
	}
	infos[right.terminal().SID] = withAgentState(right, "codex", agents.Running, 1)
	a.apply(infos)
	if closed != 1 {
		t.Fatal("resolved notification was not removed")
	}
	infos[right.terminal().SID] = withAgentState(right, "codex", agents.Waiting, 2)
	a.apply(infos)
	if len(clicks) != 1 {
		t.Fatal("viewed pane sent a notification")
	}
	a.selectTab(1)
	a.apply(infos)
	if len(clicks) != 1 {
		t.Fatal("suppressed waiting transition replayed on switching tabs")
	}
	infos[right.terminal().SID] = withAgentState(right, "codex", agents.Running, 2)
	a.apply(infos)
	infos[right.terminal().SID] = withAgentState(right, "codex", agents.Waiting, 3)
	a.apply(infos)
	if len(clicks) != 2 {
		t.Fatal("new waiting period did not notify")
	}
	a.setAgentNotifications(false)
	infos[right.terminal().SID] = withAgentState(right, "codex", agents.Waiting, 4)
	a.apply(infos)
	if len(clicks) != 2 || !readSettings().HideAgentNotifications {
		t.Fatal("notification preference not respected or saved")
	}
	if a.focusAgentPane("already-closed") {
		t.Fatal("stale notification focused another pane")
	}
}

func TestBackgroundAgentCompletionNotifications(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	tab := a.tab()
	left := tab.Focus
	a.split(false)
	p := tab.Focus
	a.newTab("/tmp")
	tt.Frame()
	if !a.focusedWin {
		t.Fatal("test must keep the window in front while another tab is active")
	}
	var notices []mygo.NotificationOptions
	var clicks []func()
	closed := 0
	a.agentNotify = func(opts mygo.NotificationOptions, click func()) func() {
		notices = append(notices, opts)
		clicks = append(clicks, click)
		return func() { closed++ }
	}
	info := withAgentState(p, "codex", agents.Running, 0)
	apply := func(state string, revision uint64) {
		info.Agent.State, info.Agent.CompletionRevision, info.Agent.Updated = state, revision, time.Now()
		a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	}
	apply(agents.Running, 0)
	apply(agents.Completed, 1)
	apply(agents.Completed, 1)
	if len(notices) != 1 || !strings.Contains(notices[0].Title, "Codex · 已完成") {
		t.Fatalf("background completion missing or duplicate: %+v", notices)
	}
	tab.setFocus(left)
	tab.Zoom = left
	clicks[0]()
	if a.tab() != tab || tab.Focus != p || tab.Zoom != nil || a.focusReq != p {
		t.Fatal("completion notification did not focus its pane")
	}
	tt.Frame()
	apply(agents.Running, 1)
	apply(agents.Completed, 2)
	if len(notices) != 1 {
		t.Fatal("viewed pane sent completion notification")
	}
	a.selectTab(1)
	apply(agents.Completed, 2)
	if len(notices) != 1 {
		t.Fatal("switching tabs replayed suppressed completion")
	}
	apply(agents.Running, 2)
	apply(agents.Completed, 3)
	if len(notices) != 2 {
		t.Fatal("next turn not notified")
	}
	// A missed intermediate Running poll still produces a new completion.
	apply(agents.Completed, 4)
	if len(notices) != 3 {
		t.Fatal("completion revision was ignored without intermediate state")
	}
	apply(agents.Running, 4)
	apply(agents.Failed, 5)
	if len(notices) != 4 || !strings.Contains(notices[3].Title, "执行失败") {
		t.Fatal("failure not notified")
	}
	a.setAgentNotifications(false)
	if a.agentNotices[p.terminal().SID].close == nil {
		t.Fatal("waiting preference removed completion notice")
	}
	a.setAgentCompletionNotifications(false)
	apply(agents.Running, 5)
	apply(agents.Completed, 6)
	if len(notices) != 4 || !readSettings().HideAgentCompletionNotifications {
		t.Fatal("completion preference not respected")
	}
	a.setAgentCompletionNotifications(true)
	// Losing window focus also announces the active pane's completion.
	a.selectTab(0)
	tab.setFocus(p)
	a.focusedWin = false
	apply(agents.Running, 6)
	apply(agents.Completed, 7)
	if len(notices) != 5 {
		t.Fatal("unfocused window completion not announced")
	}
	// Returning to the shell does not dismiss the result before it is read.
	info.Idle = true
	info.Program = "sh"
	info.Agent.State = ""
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if a.agentNotices[p.terminal().SID].close == nil {
		t.Fatal("completion notice removed when agent exited")
	}
	if closed == 0 {
		t.Fatal("outdated notifications never cleaned up")
	}
	// Reopening an already completed session must not notify old results.
	reopened := &App{tabs: a.tabs, active: 1, agentNotify: a.agentNotify, focusedWin: true}
	info = withAgentState(p, "codex", agents.Completed, 0)
	info.Agent.CompletionRevision = 7
	p.terminal().info = info
	reopened.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if len(notices) != 5 {
		t.Fatal("restored historical completion replayed")
	}
}

func TestBackgroundLongCommandCompletionNotice(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	a.newTab("/tmp")
	tt.Frame()
	var notices []mygo.NotificationOptions
	var click func()
	a.agentNotify = func(opts mygo.NotificationOptions, onClick func()) func() {
		notices = append(notices, opts)
		click = onClick
		return func() {}
	}
	before := p.terminal().info
	before.Program = "sleep"
	before.Idle = false
	before.LastInput = time.Now().Add(-9 * time.Second)
	p.terminal().info = before
	after := before
	after.Idle = true
	after.Program = "sh"
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: after})
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: after})
	if len(notices) != 1 {
		t.Fatal("background long command in focused window did not notify exactly once")
	}
	click()
	if a.tab() != p.Tab {
		t.Fatal("command notification did not return to tab")
	}
	before.LastInput = time.Now().Add(-time.Second)
	p.terminal().info = before
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: after})
	if len(notices) != 1 {
		t.Fatal("short/viewed command should not notify")
	}
}

func TestActivePaneSuppressesAllDesktopNoticePaths(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	p := &Pane{content: &terminalPane{SID: "active"}}
	other := &Pane{content: &terminalPane{SID: "other"}}
	tab := &Tab{Focus: p, Root: &Node{A: &Node{Pane: p}, B: &Node{Pane: other}}}
	p.Tab, other.Tab = tab, tab
	a := &App{tabs: []*Tab{tab}, focusedWin: true}
	notices, closed := 0, 0
	a.agentNotify = func(mygo.NotificationOptions, func()) func() {
		notices++
		return func() { closed++ }
	}
	info := withAgentState(p, "codex", agents.Running, 0)
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	info.Agent.State, info.Agent.CompletionRevision = agents.Completed, 1
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	a.showTerminalNotice(p, "Codex · 已完成", "terminal notification")
	a.showPaneNotice(p, "program", mygo.NotificationOptions{Title: "command finished"})
	if notices != 0 {
		t.Fatal("active pane notified through an agent, OSC or command path")
	}
	// Opening an overlay keeps this pane active; it must not turn an
	// otherwise suppressed completion into a desktop notification.
	a.settingsOpen, a.paletteOpen = true, true
	info.Agent.CompletionRevision = 2
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	a.showTerminalNotice(p, "Codex", "active pane under overlay")
	if notices != 0 {
		t.Fatal("an overlay made the selected pane notify")
	}
	a.settingsOpen, a.paletteOpen = false, false
	// An unfocused split in this same tab must still be allowed to notify.
	a.showTerminalNotice(other, "other", "background split")
	if notices != 1 {
		t.Fatal("unfocused split was suppressed with the active pane")
	}
	tab.setFocus(other)
	a.closeViewedPaneNotice()
	if closed != 1 || a.agentNotices[other.terminal().SID].close != nil {
		t.Fatal("returning to the notified split left its banner pending")
	}
	tab.setFocus(p)
	a.focusedWin = false
	a.showTerminalNotice(p, "", "background window")
	if notices != 2 {
		t.Fatal("unfocused window did not notify")
	}
	a.focusedWin = true
	a.closeViewedPaneNotice()
	if closed != 2 {
		t.Fatal("returning window focus left the active pane's banner pending")
	}
	// Focused completions stay consumed; switching away cannot replay them.
	tab.setFocus(other)
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if notices != 2 {
		t.Fatal("leaving the active pane replayed a suppressed completion")
	}
}

func TestAgentStateMarkersInTabs(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	registerFonts()
	p := &Pane{ID: 2, content: &terminalPane{SID: "pane", info: rex.SessionInfo{Shell: "zsh", Dir: "/work/repo"}}}
	tab := &Tab{ID: 1, Focus: p, Root: &Node{ID: 3, Pane: p}}
	p.Tab, p.Node = tab, tab.Root
	a := &App{tabs: []*Tab{tab}}
	tt := ui.NewTester(a.view, 1000, 620)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	for _, state := range []string{agents.Ready, agents.Running, agents.Waiting, agents.Completed, agents.Failed, agents.Ready} {
		p.terminal().info = withAgentState(p, "claude", state, 1)
		p.attention = true // old bell attention must not hide hook state
		tt.Frame()
		label := "Agent " + state
		if state == agents.Waiting {
			label = "Agent waiting for input"
		}
		if _, ok := tt.Find(label); ok != (state != agents.Ready) {
			t.Fatalf("%s marker visible = %v", label, ok)
		}
		if _, ok := tt.Find("Claude Code icon"); !ok {
			t.Fatal("state marker replaced the brand icon")
		}
		prefs.DesktopLayout = "sidebar"
		tt.Frame()
		if _, ok := tt.Find(label); ok != (state != agents.Ready) {
			t.Fatalf("sidebar %s marker visible = %v", label, ok)
		}
		prefs.DesktopLayout = ""
	}
	p.terminal().info = withAgentState(p, "claude", agents.Waiting, 1)
	tt.Frame()
	if err := tt.Click("Agent waiting for input"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focusReq != p {
		t.Fatal("status click did not focus pane")
	}
	p.terminal().info.Program, p.terminal().info.Idle = "zsh", true
	tt.Frame()
	if _, ok := tt.Find("Agent waiting for input"); ok {
		t.Fatal("exited agent left a stale marker")
	}
	if dir := os.Getenv("MYGO_TEST_IMAGES"); dir != "" {
		os.MkdirAll(dir, 0o755)
		a.tabs = nil
		for i, state := range []string{agents.Running, agents.Waiting, agents.Completed} {
			pane := &Pane{ID: 10 + i, content: &terminalPane{SID: state, info: rex.SessionInfo{Shell: "zsh", Dir: "/work/repo"}}}
			pane.terminal().info = withAgentState(pane, "claude", state, 1)
			tab := &Tab{ID: 20 + i, Focus: pane, Root: &Node{ID: 30 + i, Pane: pane}}
			pane.Tab = tab
			a.tabs = append(a.tabs, tab)
		}
		tt.SetSize(1512, 240)
		tt.SetScale(2)
		tt.SetDark(true)
		saveSettingsImage(t, tt, "agent-status-compact-dark")
	}
}

func TestAgentIntegrationSettingsInstallRemoveAndErrors(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	a, tt := newTestApp(t)
	a.openSettings()
	tt.Frame()
	if err := tt.Click("Agent integrations"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude", "codex"} {
		name := programOf(id).Name
		if err := tt.Click("Install " + name + " integration"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, "hook installation", func() bool { return a.agentHookBusy == "" && a.agentHooks[id].Installed })
		tt.Frame()
		if err := tt.Click("Remove " + name + " integration"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, "hook removal", func() bool { return a.agentHookBusy == "" && !a.agentHooks[id].Present })
	}
	path, _ := agents.HookPath("claude")
	os.WriteFile(path, []byte("broken"), 0o600)
	a.changeAgentHooks("claude", true)
	waitFor(t, tt, "malformed config error", func() bool { return a.agentHookBusy == "" && a.agentHookError != "" })
	data, _ := os.ReadFile(path)
	if string(data) != "broken" {
		t.Fatal("settings action overwrote a broken user config")
	}
	tt.SetSize(1512, 948)
	tt.SetScale(2)
	tt.SetDark(true)
	if dir := os.Getenv("MYGO_TEST_IMAGES"); dir != "" {
		os.WriteFile(path, []byte("{}"), 0o600)
		a.agentHookError = ""
		a.refreshAgentHooks()
		os.MkdirAll(filepath.Clean(dir), 0o755)
		saveSettingsImage(t, tt, "settings-agents-dark")
	}
}

func TestAgentHookSurvivesWindowReconnect(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script, capture := filepath.Join(bin, "claude"), filepath.Join(dir, "environment")
	// A local fixture exercises PTY foreground detection and inherited hook
	// credentials without starting an AI session or touching user settings.
	fixture := "#!/bin/sh\numask 077\nprintf '%s\\n' \"$RETTY_SESSION\" \"$RETTY_AGENT_TOKEN\" \"$RETTY_AGENT_SOCKET\" > \"$1\"\nprintf 'fixture-ready\\n'\nwhile IFS= read -r line; do :; done\n"
	if err := os.WriteFile(script, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	p := a.tab().Focus
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	p.terminal().stream.Write([]byte(quote(script) + " " + quote(capture) + "\r"))
	waitFor(t, tt, "fixture foreground", func() bool {
		refresh(a)
		return p.terminal().info.Program == "claude" && !p.terminal().info.Idle && strings.Contains(p.terminal().term.Text(), "fixture-ready")
	})
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(fields) != 3 || fields[0] != p.terminal().SID {
		t.Fatal("fixture did not inherit pane credentials")
	}
	a.newTab("/tmp")
	a.focusedWin = true
	report := func(event, tool string) {
		t.Helper()
		err := rex.ReportAgent(fields[2], fields[0], fields[1], rex.AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{SessionID: "fixture-thread", Event: event, Tool: tool}})
		if err != nil {
			t.Fatal(err)
		}
	}
	report("SessionStart", "")
	report("UserPromptSubmit", "")
	report("PreToolUse", "AskUserQuestion")
	refresh(a)
	if s := paneAgentState(p); s.State != agents.Waiting || s.WaitRevision != 1 {
		t.Fatalf("live hook state not delivered: %+v", s)
	}
	a.save()
	for _, tab := range a.tabs {
		for _, pane := range tab.panes() {
			pane.terminal().term.Close()
		}
	}
	reopened := &App{client: a.client}
	if !reopened.restore() {
		t.Fatal("window layout did not reconnect")
	}
	t.Cleanup(func() {
		for _, tab := range reopened.tabs {
			for _, pane := range tab.panes() {
				pane.terminal().term.Close()
			}
		}
	})
	var restored *Pane
	for _, tab := range reopened.tabs {
		for _, pane := range tab.panes() {
			if pane.terminal().SID == p.terminal().SID {
				restored = pane
			}
		}
	}
	if restored == nil || paneAgentState(restored).State != agents.Waiting {
		t.Fatal("reconnect lost pending question")
	}
	report("PostToolUse", "AskUserQuestion")
	report("Stop", "")
	refresh(reopened)
	if paneAgentState(restored).State != agents.Completed {
		t.Fatal("completed event did not update reconnected pane")
	}
}

func TestCodexTitleCompletionDoesNotNotify(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	p := a.tab().Focus
	a.newTab("/tmp")
	tt.Frame()
	var notices []mygo.NotificationOptions
	a.agentNotify = func(opts mygo.NotificationOptions, _ func()) func() {
		notices = append(notices, opts)
		return func() {}
	}
	info := withAgentState(p, "codex", agents.Running, 0)
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	info.Agent.State, info.Agent.CompletionRevision = agents.Completed, 1
	info.Agent.Summary = `{"title":"提交 relaykit-bridge PR"}`
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if len(notices) != 0 {
		t.Fatal("internal title generation sent a desktop notification", notices)
	}
	info.Agent.CompletionRevision++
	info.Agent.Summary = "PR 已提交。"
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if len(notices) != 1 || notices[0].Body != info.Agent.Summary {
		t.Fatal("real completion after title generation was lost", notices)
	}
}
