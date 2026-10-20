package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/agenthistory"
	"retty/internal/rex"
)

func historyFixture(a *App, sessions []agenthistory.Session) {
	a.agentHistory = &sidebarAgentHistory{loaded: true, nextRead: time.Now().Add(time.Hour), selected: -1}
	a.agentHistory.prepareCodex = func(s agenthistory.Session, _, _, _ string, _ []string) (agenthistory.Session, error) { return s, nil }
	a.agentHistory.setSessions(sessions)
}

func TestSidebarProjectHistoryDisclosureAndResume(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(cwd, ".retty-history-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(dir, "project with spaces")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(project, 0o700)
	os.MkdirAll(bin, 0o700)
	for _, agent := range []string{"codex", "claude"} {
		script := "#!/bin/sh\nprintf 'cwd=%s\\ncodex-home=%s\\nclaude-home=%s\\n' \"$PWD\" \"$CODEX_HOME\" \"$CLAUDE_CONFIG_DIR\"\nprintf 'arg=%s\\n' \"$@\"\nwhile IFS= read -r line; do :; done\n"
		if err := os.WriteFile(filepath.Join(bin, agent), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	a, tt := newTestApp(t)
	before, _ := a.client.List()
	original := a.tab()
	sessions := []agenthistory.Session{
		{Agent: "codex", ID: "codex-exact", Dir: project, Title: "Codex conversation", ConfigDir: filepath.Join(dir, "codex config"), Updated: time.Now()},
		{Agent: "claude", ID: "claude-exact", Dir: project, Title: "Claude conversation", ConfigDir: filepath.Join(dir, "claude config"), Updated: time.Now()},
	}
	historyFixture(a, sessions)
	tt.Frame()
	if tt.HasText("Codex conversation") {
		t.Fatal("closed project shows its sessions")
	}
	if err := tt.Click("project with spaces"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Codex conversation") || !tt.HasText("Claude conversation") {
		t.Fatal("project did not expand", tt.Texts())
	}
	if err := tt.Click("project with spaces"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("Codex conversation") {
		t.Fatal("project did not collapse")
	}
	if len(a.tabs) != 1 || a.tab() != original {
		t.Fatal("disclosure created or switched a tab")
	}
	if err := tt.Click("project with spaces"); err != nil {
		t.Fatal(err)
	}
	for i, s := range sessions {
		if err := tt.Click("恢复 " + programOf(s.Agent).Name + " 会话 " + s.Title); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, "new history tab", func() bool { return len(a.tabs) == i+2 })
		if len(a.tabs) != i+2 || a.tab() == original || a.tab().Host != nil || a.tab().Name != s.Title {
			t.Fatal("resume did not open a new local named tab")
		}
		p := a.tab().Focus
		waitFor(t, tt, "resume exact history", func() bool { return p.term != nil && strings.Contains(p.term.Text(), "arg="+s.ID) })
		out := p.term.Text()
		if !strings.Contains(out, "cwd="+project) || !strings.Contains(out, s.ConfigDir) || strings.Contains(out, "arg=--last") {
			t.Fatal("resume lost exact project or config", out)
		}
		if i == 0 && !strings.Contains(out, "arg=resume") || i == 1 && !strings.Contains(out, "arg=--resume") {
			t.Fatal("incorrect resume command", out)
		}
		// Repeated clicks before hooks/polling arrive must reuse the same PTY.
		a.selectTab(0)
		tt.Frame()
		if err := tt.Click("恢复 " + programOf(s.Agent).Name + " 会话 " + s.Title); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, "reactivate history pane", func() bool { return a.tab().Focus == p })
		if len(a.tabs) != i+2 || a.tab().Focus != p {
			t.Fatal("already-open conversation was resumed a second time")
		}
	}
	after, _ := a.client.List()
	for _, b := range before {
		found := false
		for _, s := range after {
			if s.ID == b.ID && s.PID == b.PID {
				found = true
			}
		}
		if !found {
			t.Fatal("resume disturbed an existing shell")
		}
	}
	// Local history must also open locally when a remote tab is selected.
	original.Host = &desktopHost{key: "history-fixture"}
	a.active = 0
	if err := tt.Click("恢复 Codex 会话 " + sessions[0].Title); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 3 || a.tab().Host != nil || a.tab().Name != sessions[0].Title {
		t.Fatal("local history did not reactivate its local tab from a remote tab")
	}
	original.Host = nil
}

func TestSidebarHistoryActivatesExistingSplitAndIgnoresOtherConversations(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, _ := newTestApp(t)
	original := a.tab()
	a.split(false)
	panes := original.panes()
	p, other := panes[0], panes[1]
	p.info.Agent = rex.AgentState{ID: "codex", SessionID: "already-open", State: "running"}
	original.Zoom = other
	original.setFocus(other)
	a.newTab("/tmp")
	s := agenthistory.Session{Agent: "codex", ID: "already-open", Dir: "/missing", ConfigDir: "/fixture"}
	h := &sidebarAgentHistory{}
	before, _ := a.client.List()
	if !a.activateOpenHistory(h, s) || a.tab() != original || a.tab().Focus != p || a.focusReq != p || original.Zoom != nil {
		t.Fatal("open conversation was not revealed in its original split")
	}
	after, _ := a.client.List()
	if len(a.tabs) != 2 || len(before) != len(after) {
		t.Fatal("activating an existing conversation created a session")
	}
	p.info.Agent.SessionID = "switched-conversation"
	p.info.Program, p.info.Shell = "codex", "codex"
	p.info.Args = []string{"codex", "resume", s.ID}
	h.opened = map[string]string{s.Key(): p.SID}
	if a.activateOpenHistory(h, s) {
		t.Fatal("stale launch identity overrode the current conversation")
	}
	p.info.Agent = rex.AgentState{}
	p.info.Exited = true
	if a.activateOpenHistory(h, s) {
		t.Fatal("exited conversation was treated as open")
	}
	p.info.Exited = false
	original.Host = &desktopHost{key: "other-machine"}
	if a.activateOpenHistory(h, s) {
		t.Fatal("local history matched a remote conversation")
	}
	original.Host = nil
}

func TestHistoryResumeArgsExactIdentity(t *testing.T) {
	for _, tc := range []struct {
		agent string
		args  []string
		want  bool
	}{
		{"codex", []string{"/usr/local/bin/codex", "resume", "exact"}, true},
		{"codex", []string{"codex", "-C", "/project", "resume", "exact"}, true},
		{"codex", []string{"codex", "resume", "another"}, false},
		{"codex", []string{"codex", "exec", "resume", "exact"}, false},
		{"claude", []string{"claude", "--model", "sonnet", "--resume", "exact"}, true},
		{"claude", []string{"claude", "--resume=exact"}, true},
		{"claude", []string{"claude", "--resume", "other"}, false},
		{"claude", []string{"claude", "-p", "--resume", "exact"}, false},
		{"claude", []string{"claude", "prompt", "--resume", "exact"}, false},
		{"claude", []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "--resume", "exact"}, true},
		{"codex", []string{"node", "--require", "helper.js", "/usr/lib/node_modules/@openai/codex/bin/codex.js", "resume", "exact"}, true},
		{"claude", []string{"npx", "--yes", "@anthropic-ai/claude-code", "--resume", "exact"}, true},
		{"claude", []string{"node", "app.js", "claude", "--resume", "exact"}, false},
		{"claude", []string{"node", "--eval", "claude", "--resume", "exact"}, false},
		{"claude", []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "prompt", "--resume", "exact"}, false},
	} {
		if got := historyResumeArgs(agenthistory.Session{Agent: tc.agent, ID: "exact"}, tc.args); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestSidebarHistoryDoesNotActivateOtherAccountHome(t *testing.T) {
	a, _ := newTestApp(t)
	p := a.tab().Focus
	first := agenthistory.Session{Agent: "codex", ID: "same-id", ConfigDir: "/accounts/first"}
	second := first
	second.ConfigDir = "/accounts/second"
	h := &sidebarAgentHistory{sessions: map[string]agenthistory.Session{first.Key(): first, second.Key(): second}}
	p.info.Agent = rex.AgentState{ID: "codex", SessionID: "same-id", State: "running", ConfigDir: first.ConfigDir}
	if a.activateOpenHistory(h, second) || !a.activateOpenHistory(h, first) {
		t.Fatal("conversation ID crossed account configuration homes")
	}
	p.info.Agent.ConfigDir = ""
	if a.activateOpenHistory(h, first) || a.activateOpenHistory(h, second) {
		t.Fatal("legacy server guessed a configuration home for an ambiguous ID")
	}
	h.opened = map[string]string{second.Key(): p.SID}
	if !a.activateOpenHistory(h, second) {
		t.Fatal("known scoped launch was not reused")
	}
}

func TestSidebarHistoryShowsTitlesWithoutAccountOrProvider(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.tab().Name = "Retty"
	a.tab().Focus.term.Feed([]byte("\x1b[2J\x1b[H› Select a conversation to resume\r\n"))
	historyFixture(a, []agenthistory.Session{
		{Agent: "codex", ID: "personal", Dir: "/work/retty", ConfigDir: "/fixture/personal", Title: "Personal conversation", Provider: "openai", AccountLabel: "Default Codex Account", Updated: time.Now()},
		{Agent: "codex", ID: "work", Dir: "/work/retty", ConfigDir: "/fixture/work", Title: "Gateway conversation", Provider: "aicoding", AccountLabel: "Work", Updated: time.Now()},
	})
	a.agentHistory.outline.Open.Add("project:/work/retty")
	tt.Frame()
	for _, text := range []string{"Personal conversation", "Gateway conversation"} {
		if !tt.HasText(text) {
			t.Fatal("title missing from history row", text)
		}
	}
	for _, text := range []string{"Default Codex Account", "Work", "openai · Default Codex Account", "aicoding · Work"} {
		if tt.HasText(text) {
			t.Fatal("account/provider remains in history row", text)
		}
	}
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	tt.SetScale(2)
	saveSettingsImage(t, tt, "sidebar-history-titles")
}

func TestSidebarHistoryAsyncRefreshAndMissingProject(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(cwd, ".retty-history-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	project := filepath.Join(home, "missing project")
	path := filepath.Join(home, "codex", "sessions", "rollout-fixture.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	data := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":\"exact-id\",\"cwd\":%q}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"History task\"}}\n", project)
	os.WriteFile(path, []byte(data), 0o600)
	a, tt := newTestApp(t)
	waitFor(t, tt, "history scan", func() bool { return a.agentHistory != nil && a.agentHistory.loaded })
	if err := tt.Click("missing project"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("恢复 Codex 会话 History task"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "missing project error", func() bool { return a.err != "" })
	if len(a.tabs) != 1 || !strings.Contains(a.err, "项目目录不可用") {
		t.Fatal("missing project resumed a shell elsewhere", a.err)
	}
	os.Remove(path)
	if err := tt.Click("刷新项目会话"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "history refresh", func() bool { return !a.agentHistory.loading })
	if len(a.agentHistory.projects) != 0 {
		t.Fatal("deleted history remained in sidebar")
	}
}

func TestSidebarProjectHistoryAppearanceAndVirtualization(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.tab().Name = "Current work"
	a.tab().Focus.term.Feed([]byte("\x1b[2J\x1b[H› Continue an earlier conversation from the sidebar\r\n\r\n  Codex and Claude Code sessions grouped by project\r\n  Each conversation resumes in a new tab\r\n"))
	var sessions []agenthistory.Session
	for i := range 1000 {
		agent := "codex"
		if i%2 == 1 {
			agent = "claude"
		}
		sessions = append(sessions, agenthistory.Session{Agent: agent, ID: fmt.Sprint(i), Dir: "/work/retty", Title: fmt.Sprintf("Conversation %04d", i), ConfigDir: "/fixture", Updated: time.Unix(1000-int64(i), 0)})
	}
	sessions = append(sessions, agenthistory.Session{Agent: "claude", ID: "another", Dir: "/work/mygo", Title: "Native components", ConfigDir: "/fixture", Updated: time.Now()})
	historyFixture(a, sessions)
	tt.Frame()
	if err := tt.Click("retty"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Conversation 0000") || tt.HasText("Conversation 0999") {
		t.Fatal("history list did not virtualize offscreen sessions")
	}
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	tt.SetScale(2)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Advance(180 * time.Millisecond)
		saveSettingsImage(t, tt, map[bool]string{false: "sidebar-projects-light", true: "sidebar-projects-dark"}[dark])
	}
	tt.SetSize(560, 340)
	if r, ok := tt.Find("项目会话列表"); !ok || r.H < 100 || r.Y+r.H > 340 {
		t.Fatalf("small window makes project history unreachable: %+v", r)
	}
}

func TestSidebarProjectsSortByLatestActivityAfterRefresh(t *testing.T) {
	now := time.Unix(1000, 0)
	sessions := []agenthistory.Session{
		{Agent: "codex", ID: "a-old", Dir: "/a", Updated: now},
		{Agent: "claude", ID: "b-new", Dir: "/b", Updated: now.Add(3 * time.Hour)},
		{Agent: "claude", ID: "a-new", Dir: "/a", Updated: now.Add(2 * time.Hour)},
		{Agent: "codex", ID: "c", Dir: "/c", Updated: now.Add(time.Hour)},
	}
	h := &sidebarAgentHistory{}
	h.setSessions(sessions)
	if !reflect.DeepEqual(h.projects, []string{"project:/b", "project:/a", "project:/c"}) ||
		!reflect.DeepEqual(h.children["project:/a"], []string{sessions[2].Key(), sessions[0].Key()}) {
		t.Fatal("projects did not follow their latest conversation", h.projects, h.children)
	}
	h.outline.Open.Add("project:/a")
	sessions[0].Updated = now.Add(4 * time.Hour)
	h.setSessions(sessions)
	if !reflect.DeepEqual(h.projects, []string{"project:/a", "project:/b", "project:/c"}) ||
		h.children["project:/a"][0] != sessions[0].Key() || !h.outline.Open.Has("project:/a") {
		t.Fatal("refresh did not reorder by activity while retaining disclosure", h.projects)
	}
}

func TestSidebarHistoryResolvesCLIOutsideDaemonPath(t *testing.T) {
	a, tt := newTestApp(t)
	dir, err := os.MkdirTemp("/tmp", "retty-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(bin, "claude")
	script := "#!/bin/sh\nprintf 'cli=%s\\npath=%s\\n' \"$0\" \"$PATH\"\nprintf 'arg=%s\\n' \"$@\"\nwhile IFS= read -r line; do :; done\n"
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	loginShell := filepath.Join(dir, "login-shell")
	if err := os.WriteFile(loginShell, []byte("#!/bin/sh\nprintf '\\000RETTY_PATH\\000%s\\n\\000' \"$RETTY_TEST_LOGIN_PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", loginShell)
	t.Setenv("RETTY_TEST_LOGIN_PATH", bin+":/usr/bin:/bin")
	s := agenthistory.Session{Agent: "claude", ID: "path-exact", Dir: dir, Title: "Path fixture", ConfigDir: dir}
	historyFixture(a, []agenthistory.Session{s})
	tt.Frame()
	a.resumeHistorySession(a.agentHistory, s)
	a.resumeHistorySession(a.agentHistory, s)
	waitFor(t, tt, "resolved Claude tab", func() bool { return len(a.tabs) == 2 || a.err != "" })
	if a.err != "" || len(a.tabs) != 2 {
		t.Fatal("CLI missing from daemon PATH was not resolved", a.err)
	}
	p := a.tab().Focus
	waitFor(t, tt, "resolved CLI output", func() bool { return p.term != nil && strings.Contains(p.term.Text(), "arg=path-exact") })
	out := p.term.Text()
	if !strings.Contains(out, "cli="+cli) || !strings.Contains(out, bin) || len(a.tabs) != 2 {
		t.Fatal("resume lost absolute path/PATH or created duplicates", out)
	}
}

func TestSidebarHistoryClickFocusesNestedPaneWithoutNewTab(t *testing.T) {
	for _, mode := range []string{"codex-hook", "claude-hook", "claude-node-resume"} {
		t.Run(mode, func(t *testing.T) {
			previous := prefs
			prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
			t.Cleanup(func() { prefs = previous })
			a, tt := newTestApp(t)
			bin := t.TempDir()
			os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			original, left := a.tab(), a.tab().Focus
			a.split(false)
			a.split(true)
			target := original.Focus
			agent := "claude"
			if mode == "codex-hook" {
				agent = "codex"
			}
			s := agenthistory.Session{Agent: agent, ID: "nested-conversation", Dir: "/fixture/nested-project", Title: "Nested conversation", ConfigDir: "/fixture/config"}
			target.info.Program, target.info.Idle = agent, false
			if mode == "claude-node-resume" {
				target.info.Args = []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "--resume", s.ID}
			} else {
				target.info.Agent = rex.AgentState{ID: agent, SessionID: s.ID, State: "running"}
			}
			original.setFocus(left)
			original.Zoom = left
			a.newTab("/tmp")
			historyFixture(a, []agenthistory.Session{s})
			a.agentHistory.outline.Open.Add("project:" + s.Dir)
			tt.Frame()
			before, err := a.client.List()
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := tt.Click("恢复 " + programOf(agent).Name + " 会话 " + s.Title); err != nil {
					t.Fatal(err)
				}
				waitFor(t, tt, "focus nested history pane", func() bool { return a.tab() == original && original.Focus == target })
				if len(a.tabs) != 2 || a.tab() != original || original.Focus != target || original.Zoom != nil {
					t.Fatal("sidebar did not reveal the existing nested pane")
				}
				if !tt.Focused("Terminal") {
					t.Fatal("keyboard focus did not reach the existing pane")
				}
			}
			after, err := a.client.List()
			if err != nil || len(before) != len(after) {
				t.Fatal("sidebar created a new PTY", err)
			}
			for _, old := range before {
				found := false
				for _, current := range after {
					found = found || current.ID == old.ID && current.PID == old.PID
				}
				if !found {
					t.Fatal("existing PTY was replaced")
				}
			}
		})
	}
}

func TestSidebarCodexHistoryFocusesCurrentProviderContinuation(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := a.tab()
	originalPane := original.Focus
	originalPane.info.Agent = rex.AgentState{ID: "codex", SessionID: "old-provider-session", State: "running", ConfigDir: "/fixture/current"}
	continuationTab := a.newTab("/tmp")
	a.split(false)
	continuationPane := continuationTab.Focus
	continuationPane.info.Agent = rex.AgentState{ID: "codex", SessionID: "current-provider-branch", State: "running", ConfigDir: "/fixture/current"}
	continuationTab.setFocus(continuationTab.panes()[0])
	continuationTab.Zoom = continuationTab.Focus
	a.selectTab(0)
	source := agenthistory.Session{Agent: "codex", ID: "old-provider-session", Dir: "/fixture/project", ConfigDir: "/fixture/current", Provider: "before", Title: "Old provider history"}
	historyFixture(a, []agenthistory.Session{source})
	a.agentHistory.outline.Open.Add("project:" + source.Dir)
	a.agentHistory.prepareCodex = func(s agenthistory.Session, _, _, _ string, _ []string) (agenthistory.Session, error) {
		s.ID = "current-provider-branch"
		s.Provider = "after"
		return s, nil
	}
	tt.Frame()
	before, _ := a.client.List()
	for range 2 {
		if err := tt.Click("恢复 Codex 会话 " + source.Title); err != nil {
			t.Fatal(err)
		}
		waitFor(t, tt, "current-provider continuation pane", func() bool { return a.tab() == continuationTab && a.tab().Focus == continuationPane })
		if len(a.tabs) != 2 || continuationTab.Zoom != nil || a.tab() == original {
			t.Fatal("activated original provider or created another tab")
		}
		a.selectTab(0)
		tt.Frame()
	}
	after, _ := a.client.List()
	if len(before) != len(after) {
		t.Fatal("continuation activation created a PTY")
	}
}
