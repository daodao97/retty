package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func TestDesktopSidebarSwitchPreservesSessionsAndPersists(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	a.tabs[0].Name = "Build logs"
	a.newTab("/tmp")
	a.tab().Name = "Code review"
	a.split(false)
	tab, root, focus := a.tab(), a.tab().Root, a.tab().Focus
	tab.Zoom = focus
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	tt.Frame()
	before, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	pid := map[string]int{}
	for _, session := range before {
		pid[session.ID] = session.PID
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if err := tt.Click("侧边栏模式"); err != nil {
		t.Fatal(err)
	}
	if prefs.DesktopLayout != "sidebar" || readSettings().DesktopLayout != "sidebar" {
		t.Fatal("layout choice was not applied and saved")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := tt.Find("Tabs"); ok {
		t.Fatal("sidebar mode still has the classic tab strip")
	}
	if side, ok := tt.Find("Tab sidebar"); !ok || side.X != 0 || side.W != 224 || side.H != 620 {
		t.Fatalf("sidebar does not fill the left edge: %+v", side)
	}
	if r := focus.bounds; r.X != 224 || r.Y != compactTitleH || r.W != 776 || r.Y+r.H != 620 {
		t.Fatalf("terminal does not fill the remaining content: %+v", r)
	}
	if !tt.Focused("Terminal") {
		t.Fatal("closing settings did not return to the terminal")
	}
	if err := tt.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	if !readSettings().SidebarCollapsed || focus.bounds.X != 0 || focus.bounds.W != 1000 {
		t.Fatal("collapse did not release the sidebar's space or save its state")
	}
	tt.Key(ui.Ctrl|ui.Cmd, ui.KeyS)
	if prefs.SidebarCollapsed || focus.bounds.X != 224 || !tt.Focused("Terminal") {
		t.Fatal("keyboard expansion did not restore the content and terminal focus")
	}
	if a.tab() != tab || tab.Root != root || tab.Focus != focus || tab.Zoom != focus {
		t.Fatal("switching presentation changed the selected tab or pane layout")
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if err := tt.Click("Tab 模式"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if _, ok := tt.Find("Tabs"); !ok || prefs.DesktopLayout != "" || focus.bounds.X != 0 || focus.bounds.W != 1000 {
		t.Fatal("classic layout was not restored")
	}
	after, err := a.client.List()
	if err != nil || len(after) != len(before) {
		t.Fatal("layout change lost sessions", err)
	}
	for _, session := range after {
		if pid[session.ID] != session.PID {
			t.Fatal("layout change restarted a shell", session.ID)
		}
	}
}

func TestSidebarTabActionsScrollAndSmallWindow(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.tabs[0].Name = "Session 01"
	for i := 2; i <= 20; i++ {
		pane := &Pane{ID: i, content: &terminalPane{}}
		tab := &Tab{ID: i, Name: fmt.Sprintf("Session %02d", i), Root: &Node{ID: i, Pane: pane}, Focus: pane}
		pane.Tab, pane.Node = tab, tab.Root
		a.tabs = append(a.tabs, tab)
	}
	tt.Frame()
	if err := tt.Click("Session 02"); err != nil {
		t.Fatal(err)
	}
	if a.active != 1 {
		t.Fatal("sidebar did not select the existing tab")
	}
	a.startRename()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Review")
	tt.Key(0, ui.KeyEnter)
	if a.tab().Name != "Review" {
		t.Fatalf("sidebar rename was not applied: %q", a.tab().Name)
	}
	// Drag reorder uses the same Tab object and retains its pane tree.
	first, _ := tt.Find("Session 01")
	second, _ := tt.Find("Review")
	tab := a.tab()
	tt.Press(second.X+30, second.Y+20)
	tt.Move(second.X+30, first.Y+10)
	tt.Release(second.X+30, first.Y+10)
	if a.tabs[0] != tab || a.tab() != tab {
		t.Fatal("sidebar drag did not reorder or changed the active tab")
	}
	for range 6 {
		tt.Scroll(100, 240, 0, 140)
	}
	last, ok := tt.Find("Session 20")
	if !ok || last.Y < compactTitleH || last.Y+last.H > 620 {
		t.Fatalf("wheel did not remain at the end of the list: %+v", last)
	}
	if err := tt.Click("Session 20"); err != nil {
		t.Fatal(err)
	}
	a.selectTab(0)
	tt.Frame()
	first, _ = tt.Find("Review")
	if first.Y < compactTitleH || first.Y > 200 {
		t.Fatal("selection did not reveal the active tab")
	}
	tt.Move(first.X+20, first.Y+20)
	if err := tt.Click("Close Tab"); err != nil {
		t.Fatal(err)
	}
	if len(a.tabs) != 19 || !tab.Focus.closed {
		t.Fatal("hover close did not close only the chosen tab")
	}
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	tt.SetSize(560, 340)
	for _, label := range []string{"收起侧边栏", "New Tab", "打开文件浏览器", "打开浏览器窗格", "连接"} {
		if r, ok := tt.Find(label); !ok || r.W <= 0 || r.X < 0 || r.X+r.W > 560 || r.Y+r.H > 340 {
			t.Fatalf("small-window control %q is unreachable: %+v", label, r)
		}
	}
	if a.tab().Focus.bounds.X != 188 || a.tab().Focus.bounds.W != 372 {
		t.Fatal("small-window sidebar crowded out the terminal")
	}
}

func TestDesktopLayoutSettingsDefaults(t *testing.T) {
	t.Setenv("RETTY_DIR", t.TempDir())
	for _, tc := range []struct{ saved, want string }{{"", ""}, {"tabs", ""}, {"sidebar", "sidebar"}, {"unknown", ""}} {
		b, _ := json.Marshal(settings{DesktopLayout: tc.saved, SidebarCollapsed: true})
		if err := os.WriteFile(settingsPath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readSettings(); got.DesktopLayout != tc.want || !got.SidebarCollapsed {
			t.Fatalf("saved %q: %+v", tc.saved, got)
		}
	}
}

func TestSidebarAppearanceAndRemoteMarker(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	a.tab().Name = "Sidebar navigation"
	p.terminal().info.Program = "codex"
	p.terminal().info.Agent = rex.AgentState{ID: "codex", State: "running"}
	p.terminal().term.Feed([]byte("\x1b[2J\x1b[H\x1b[?25l› Add a collapsible session sidebar\r\n\r\n  Working on the desktop layout\r\n\r\n  ✓ Preserve tabs and split panes\r\n  ✓ Remember the selected layout\r\n  ✓ Expand the terminal when collapsed\r\n\r\n› "))
	for i, name := range []string{"Build logs", "API workspace", "Notes"} {
		pane := &Pane{ID: i + 2, content: &terminalPane{}}
		tab := &Tab{ID: i + 2, Name: name, Root: &Node{ID: i + 2, Pane: pane}, Focus: pane}
		pane.Tab, pane.Node = tab, tab.Root
		if name == "API workspace" {
			tab.Host = &desktopHost{key: "sidebar-fixture", recent: desktopRecent{Name: "Mac mini"}}
			pane.host = tab.Host
		}
		a.tabs = append(a.tabs, tab)
	}
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	tt.SetScale(2)
	p.terminal().term.SetTheme(lightTerm, darkTerm)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Advance(180 * time.Millisecond) // capture after the chrome's color transition
		if !tt.HasText("Mac mini") {
			t.Fatal("sidebar lost the remote host marker")
		}
		saveSettingsImage(t, tt, map[bool]string{false: "sidebar-light", true: "sidebar-dark"}[dark])
	}
	if err := tt.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	saveSettingsImage(t, tt, "sidebar-collapsed")
	if _, ok := tt.Find("Tab sidebar"); ok || p.bounds.X != 0 || p.bounds.W != 1000 {
		t.Fatal("collapsed sidebar still occupies content space")
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	saveSettingsImage(t, tt, "sidebar-setting")
}

func TestCommandOneTogglesSidebar(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, SidebarCollapsed: true}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	a.newTab("/tmp")
	a.newTab("/tmp")
	tt.Frame()
	if !tt.Focused("Terminal") {
		t.Fatal("terminal is not focused")
	}
	tt.Key(ui.Cmd, ui.Key2)
	if prefs.DesktopLayout != "" || a.active != 2 {
		t.Fatal("Command-digit selected a tab in tab mode")
	}
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	if prefs.DesktopLayout != "sidebar" || prefs.SidebarCollapsed || readSettings().DesktopLayout != "sidebar" || a.active != 2 {
		t.Fatal("Command-1 did not switch to an expanded sidebar")
	}
	if _, ok := tt.Find("Tab sidebar"); !ok || !tt.Focused("Terminal") {
		t.Fatal("sidebar did not show or the terminal lost focus")
	}
	tt.Key(ui.Cmd, ui.Key2)
	if a.active != 2 {
		t.Fatal("Command-digit selected a tab in sidebar mode")
	}
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	if prefs.DesktopLayout != "" || readSettings().DesktopLayout != "" || a.active != 2 {
		t.Fatal("Command-1 did not restore tab mode")
	}
	if _, ok := tt.Find("Tabs"); !ok || !tt.Focused("Terminal") {
		t.Fatal("tab strip did not return or the terminal lost focus")
	}
	// The tab bar opens the sidebar and the sidebar closes itself; only
	// one of the two buttons is shown at a time.
	if _, ok := tt.Find("收起侧边栏"); ok {
		t.Fatal("tab mode shows the collapse button")
	}
	if err := tt.Click("展开侧边栏"); err != nil {
		t.Fatal(err)
	}
	if prefs.DesktopLayout != "sidebar" || prefs.SidebarCollapsed {
		t.Fatal("tab bar button did not open the sidebar")
	}
	if _, ok := tt.Find("展开侧边栏"); ok {
		t.Fatal("open sidebar still shows the expand button")
	}
	if r, ok := tt.Find("收起侧边栏"); !ok || r.X+r.W > 224 {
		t.Fatalf("collapse button is not in the sidebar: %+v", r)
	}
	if err := tt.Click("收起侧边栏"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("收起侧边栏"); ok || !prefs.SidebarCollapsed {
		t.Fatal("collapsed sidebar still shows the collapse button")
	}
	if err := tt.Click("展开侧边栏"); err != nil || prefs.SidebarCollapsed {
		t.Fatal("collapsed sidebar could not be opened from the tab bar", err)
	}
}
