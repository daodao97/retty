package main

import (
	"testing"
	"time"

	"retty/internal/gitstate"
	"retty/internal/rex"
)

func TestSidebarTabsOmitProjectHeadingsAndKeepOriginalSelection(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	a, tt := newStaticTestApp(t)
	a.agentHistory = &sidebarAgentHistory{loaded: true, nextRead: time.Now().Add(time.Hour), selected: -1}
	main := a.tab()
	main.Name, main.Focus.terminal().info.Dir = "Main task", "/fixture/repo"
	main.Focus.terminal().term.Feed([]byte("\x1b[H\x1b[2J$ git status --short\r\n M app.go\r\n"))
	p := &Pane{ID: 2, content: &terminalPane{SID: "linked-pane", info: rex.SessionInfo{Dir: "/fixture/worktree"}}}
	linked := &Tab{ID: 2, Name: "Worktree task", Focus: p, Root: &Node{ID: 2, Pane: p}}
	p.Tab, p.Node = linked, linked.Root
	a.tabs = append(a.tabs, linked)
	a.gitStatus = &desktopGitState{entries: make(map[gitScope]*desktopGitEntry)}
	for _, tab := range a.tabs {
		a.gitStatus.entries[paneGitScope(tab.Focus)] = &desktopGitEntry{status: &gitstate.Status{Repository: gitstate.Repository{Root: tab.Focus.terminal().info.Dir, CommonDir: "/fixture/repo/.git", ProjectDir: "/fixture/repo"}, Branch: "task/fix"}}
	}
	tt.Frame()
	if tt.HasText("repo") {
		t.Fatal("unexpected project heading in session list", tt.Texts())
	}
	saveSettingsImage(t, tt, "worktree-sidebar")
	if err := tt.Click("Worktree task"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.tab() != linked || a.active != 1 || linked.Focus.terminal().SID != "linked-pane" || main.Focus.closed {
		t.Fatal("project grouping selected or closed the wrong tab")
	}
}

func TestSidebarProjectsGroupsWorktreesByHostWithoutChangingTabIdentity(t *testing.T) {
	previous := prefs
	prefs.HideGitStatus = false
	t.Cleanup(func() { prefs = previous })
	a := &App{gitStatus: &desktopGitState{entries: make(map[gitScope]*desktopGitEntry)}}
	remote := &desktopHost{key: "remote-fixture", hello: rex.Hello{Host: rex.HostInfo{Name: "devbox"}}}
	add := func(id int, root string, host *desktopHost) *Tab {
		p := &Pane{ID: id, host: host, content: &terminalPane{SID: root, info: rex.SessionInfo{Dir: root}}}
		tab := &Tab{ID: id, Host: host, Focus: p, Root: &Node{Pane: p}}
		p.Tab, p.Node = tab, tab.Root
		a.tabs = append(a.tabs, tab)
		a.gitStatus.entries[paneGitScope(p)] = &desktopGitEntry{status: &gitstate.Status{Repository: gitstate.Repository{Root: root, CommonDir: "/repo/.git", ProjectDir: "/repo"}}}
		return tab
	}
	main := add(1, "/repo", nil)
	otherHost := add(2, "/repo", remote)
	linked := add(3, "/outside/worktree", nil)
	groups := a.sidebarProjects()
	if len(groups) != 2 || groups[0].label != "repo" || groups[1].label != "repo · devbox" || groups[0].tabs[0].tab != main || groups[0].tabs[1].tab != linked || groups[1].tabs[0].tab != otherHost {
		t.Fatalf("incorrect project/host grouping: %+v", groups)
	}
	if a.tabs[0] != main || a.tabs[1] != otherHost || a.tabs[2] != linked || groups[0].tabs[1].index != 2 || linked.Focus.terminal().SID != "/outside/worktree" {
		t.Fatal("presentation grouping changed saved order or resume identity")
	}
	// Missing or disabled Git metadata preserves the ordinary tab list.
	prefs.HideGitStatus = true
	groups = a.sidebarProjects()
	if len(groups) != 3 || groups[0].label != "" || groups[1].tabs[0].tab != otherHost {
		t.Fatal("disabled metadata still regrouped tabs", groups)
	}
}
