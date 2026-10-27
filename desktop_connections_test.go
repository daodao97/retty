package main

import (
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/skip2/go-qrcode"
	"retty/internal/agents"
	"retty/internal/remote"
	"retty/internal/rex"
)

// This child serves only its disposable directory, never the installed daemon.
func TestDesktopSessionFixture(t *testing.T) {
	if os.Getenv("RETTY_DESKTOP_FIXTURE") != "1" {
		t.Skip("owned test subprocess")
	}
	if err := rex.Serve(); err != nil {
		t.Fatal(err)
	}
}
func desktopFixtureClient(t *testing.T) (*rex.Client, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "retty-desktop-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "server.sock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDesktopSessionFixture$", "-test.timeout=3m")
	cmd.Env = append(os.Environ(), "RETTY_DESKTOP_FIXTURE=1", "RETTY_MOBILE_RECOVERY_E2E=0", "RETTY_DIR="+dir, "SHELL=/bin/sh")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var client *rex.Client
	t.Cleanup(func() {
		if client != nil {
			client.Shutdown()
			client.Close()
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		os.RemoveAll(dir)
	})
	dial := func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "unix", socket) }
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		client, err = rex.ConnectDial(context.Background(), dial)
		if err == nil {
			return client, socket
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fixture server did not start")
	return nil, ""
}
func fixtureDesktopHost(t *testing.T, client *rex.Client) *desktopHost {
	t.Helper()
	hello, err := client.Hello()
	if err != nil {
		t.Fatal(err)
	}
	hello.Host.Name = "Studio Mac"
	h := &desktopHost{key: "fixture", client: client, hello: hello, recent: desktopRecent{Name: "Studio Mac"}}
	return h
}
func TestDesktopRemoteSessionRoutingAndDetach(t *testing.T) {
	a, tt := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	local := a.tab().Focus
	localBefore, _ := a.client.List()
	a.createDesktopSession(h, "/tmp", nil, false)
	waitFor(t, tt, "remote tab", func() bool { return len(a.tabs) == 2 })
	tab, p := a.tab(), a.tab().Focus
	if tab.Host != h || p.host != h || a.currentHost() != h || a.paneClient(p) != client {
		t.Fatal("remote tab borrowed local transport")
	}
	waitFor(t, tt, "remote input attachment", func() bool { return p.terminal().remoteView.inputReady() })
	tt.Type("export REMOTE_CONTINUE=kept; printf 'remote-%s\\n' ready")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "remote input", func() bool { return strings.Contains(p.terminal().term.Text(), "remote-ready") })
	if strings.Contains(local.terminal().term.Text(), "remote-ready") {
		t.Fatal("input reached local shell")
	}
	// Cmd-T and split inherit the remote computer and directory.
	a.newTab(a.currentDir())
	waitFor(t, tt, "second remote tab", func() bool { return len(a.tabs) == 3 })
	if a.tab().Host != h {
		t.Fatal("new tab did not inherit host")
	}
	a.split(true)
	waitFor(t, tt, "remote split", func() bool { return len(a.tab().panes()) == 2 })
	for _, pane := range a.tab().panes() {
		if pane.host != h {
			t.Fatal("split crossed computers")
		}
	}
	// The local server's layout and poll must never adopt remote sessions.
	snapshot := a.snapshot()
	if len(snapshot.Tabs) != 1 || snapshot.Tabs[0].Root.SID != local.terminal().SID {
		t.Fatal("remote sessions leaked into local layout")
	}
	infos, _ := client.List()
	h.sessions = liveDesktopSessions(infos)
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: {ID: p.terminal().SID, Dir: "WRONG-LOCAL-DIR"}, local.terminal().SID: local.terminal().info})
	if p.terminal().info.Dir == "WRONG-LOCAL-DIR" {
		t.Fatal("local poll changed remote pane")
	}
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("新建本地会话"); err != nil {
		t.Fatal(err)
	}
	if a.tab().Host != nil || a.paneClient(a.tab().Focus) != a.client {
		t.Fatal("local menu inherited remote host")
	}
	a.closeTab(tab)
	remoteAfter, _ := client.List()
	found := false
	for _, in := range remoteAfter {
		if in.ID == p.terminal().SID && in.PID == p.terminal().info.PID && !in.Exited {
			found = true
		}
	}
	if !found {
		t.Fatal("closing remote tab ended shell")
	}
	// Reattach shares the exact shell, including its environment.
	a.openDesktopSession(h, p.terminal().info, true)
	reopened := a.tab().Focus
	waitFor(t, tt, "reattached remote input", func() bool { return reopened.terminal().remoteView.inputReady() })
	tt.Type("printf 'continued-%s\\n' \"$REMOTE_CONTINUE\"")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "same remote shell", func() bool { return strings.Contains(reopened.terminal().term.Text(), "continued-kept") })
	count := len(a.tabs)
	a.openDesktopSession(h, p.terminal().info, true)
	if len(a.tabs) != count {
		t.Fatal("existing session duplicated")
	}
	a.disconnectDesktop(h)
	select {
	case <-client.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not finish releasing remote pane ownership")
	}
	after, _ := client.List() // Disconnect owns this control client, so redial it.
	if after != nil {
		t.Fatal("disconnected control remained open")
	}
	check, err := client.Redial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	live, err := check.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 {
		t.Fatalf("disconnect ended remote tasks: got %d", len(live))
	}
	localAfter, _ := a.client.List()
	for _, in := range localAfter {
		if in.ID == local.terminal().SID && (in.PID != localBefore[0].PID) {
			t.Fatal("local shell replaced")
		}
	}
	for _, tab := range a.tabs {
		if tab.Host != nil {
			t.Fatal("disconnect retained attached remote tabs")
		}
	}
	a.quitting = true
	for _, tab := range a.tabs {
		for _, p := range tab.panes() {
			p.closed = true
			p.terminal().term.Close()
		}
	}
	check.Shutdown() // only the owned child
}

func TestDesktopMirrorRecoversWhileControlRemainsConnected(t *testing.T) {
	a, tt := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	in, err := client.Create(rex.CreateOptions{Dir: "/tmp", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	a.openDesktopSession(h, in, true)
	p := a.tab().Focus
	tt.Frame()
	waitFor(t, tt, "initial mirror snapshot ready", func() bool { return p.terminal().remoteView.inputReady() })
	tt.Type("export MIRROR_STATE=kept; printf 'mirror-%s\\n' ready")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "initial mirror", func() bool { return strings.Contains(p.terminal().term.Text(), "mirror-ready") })
	cols, rows := p.terminal().term.Size()
	oldStream := p.terminal().stream
	oldStream.Close()
	waitFor(t, tt, "viewer stream ended", func() bool { return p.terminal().streamEnded })
	if !h.connected() {
		t.Fatal("fixture also disconnected control")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.pollDesktop(ctx, h, client, h.generation, a.desktopDispatcher())
	waitFor(t, tt, "mirror restored by healthy control poll", func() bool {
		return p.terminal().stream != oldStream && !p.terminal().streamEnded && p.terminal().remoteView.inputReady() && strings.Contains(p.terminal().term.Text(), "mirror-ready")
	})
	if h.client != client || p.terminal().SID != in.ID || p.terminal().info.PID != in.PID {
		t.Fatal("viewer recovery replaced control or remote session")
	}
	tt.Type("printf 'continued-%s\\n' \"$MIRROR_STATE\"")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "same shell after viewer recovery", func() bool { return strings.Contains(p.terminal().term.Text(), "continued-kept") })
	infos, err := client.List()
	if err != nil || len(infos) != 1 || infos[0].Cols != cols || infos[0].Rows != rows {
		t.Fatal("viewer recovery changed active pane dimensions or created another session", infos, err)
	}
	a.quitting = true
	for _, tab := range a.tabs {
		for _, pane := range tab.panes() {
			pane.closed = true
			pane.terminal().term.Close()
		}
	}
}

func TestDesktopNotificationKeysAndLocalPollIsolation(t *testing.T) {
	local := &Pane{content: &terminalPane{SID: "same", info: rex.SessionInfo{ID: "same", Program: "codex", Agent: rex.AgentState{ID: "codex", State: agents.Running}}}}
	host := &desktopHost{key: "another-computer"}
	remotePane := &Pane{host: host, content: &terminalPane{SID: "same", info: rex.SessionInfo{ID: "same", Dir: "/remote"}}}
	lt, rt := &Tab{Root: &Node{Pane: local}, Focus: local}, &Tab{Host: host, Root: &Node{Pane: remotePane}, Focus: remotePane}
	local.Tab, remotePane.Tab = lt, rt
	closed := 0
	a := &App{tabs: []*Tab{lt, rt}, agentNotices: map[string]agentNotice{remotePane.noticeKey(): {close: func() { closed++ }}}}
	a.apply(map[string]rex.SessionInfo{"same": local.terminal().info})
	if remotePane.terminal().info.Dir != "/remote" || closed != 0 {
		t.Fatal("local poll overwrote remote state or dismissed remote notice")
	}
	if local.noticeKey() == remotePane.noticeKey() || noticeOnHost(remotePane.noticeKey(), nil) {
		t.Fatal("session identity crosses hosts")
	}
	if !a.focusAgentPane(remotePane.noticeKey()) || a.tab() != rt {
		t.Fatal("remote notification focused local session with same ID")
	}
}

func TestDesktopConnectionViews(t *testing.T) {
	a, tt := newStaticTestApp(t)
	a.tab().Focus.terminal().term.Feed([]byte("\x1b[2J\x1b[H\x1b[32m❯\x1b[0m retty\r\n\r\nPersistent terminals, across your devices.\r\n"))
	h := &desktopHost{key: "visual", client: &rex.Client{}, hello: rex.Hello{Host: rex.HostInfo{Name: "Studio Mac", Home: "/Users/developer"}}, recent: desktopRecent{Name: "Studio Mac", OS: "macOS"}, sessions: []rex.SessionInfo{
		{ID: "one", Program: "codex", Shell: "zsh", Dir: "/Users/developer/work/retty"},
		{ID: "two", Program: "claude", Shell: "zsh", Dir: "/Users/developer/work/mygo"},
	}}
	a.desktops.hosts = []*desktopHost{h}
	a.desktops.recentSessions = []mobileRecentSession{{Desktop: h.key, Session: "one", Title: "修复会话同步", Program: "codex"}}
	a.desktops.incoming = []remote.ConnectedDevice{{ID: "phone-fixture", Name: "iPhone 16", OS: "iOS", Connected: time.Date(2026, 10, 10, 9, 0, 0, 0, time.Local)}}
	before := a.tab().Focus.bounds
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	panel, ok := tt.Find("连接侧边栏")
	if !ok || panel.X != 616 || panel.Y != compactTitleH || panel.W != 384 || panel.H != 620-compactTitleH {
		t.Fatal("drawer is not attached to the right edge", panel)
	}
	if a.tab().Focus.bounds != before {
		t.Fatal("opening the drawer resized the terminal")
	}
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		// Let the pane's 160 ms color transition finish on the test clock.
		tt.Advance(180 * time.Millisecond)
		if dark {
			r, g, b, _ := tt.Image().At(100, 500).RGBA()
			if max(r, g, b) > 50*257 {
				t.Fatal("drawer theme leaked into the terminal background")
			}
		}
		saveDesktopImage(t, tt, map[bool]string{false: "desktop-connect-light.png", true: "desktop-connect-dark.png"}[dark])
	}
	tt.SetClipboard("invalid fixture")
	if err := tt.Click("粘贴桌面连接码"); err != nil {
		t.Fatal(err)
	}
	if a.desktops.err == "" || a.desktops.input != "invalid fixture" || len(a.desktops.hosts) != 1 {
		t.Fatal("invalid clipboard link was accepted or not read")
	}
	if err := tt.Click("查看电脑 Studio Mac"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开远端会话 one"); !ok || a.desktops.selected != h {
		t.Fatal("device row did not open its sessions")
	}
	saveDesktopImage(t, tt, "desktop-remote-sessions-dark.png")
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.desktops.selected != nil {
		t.Fatal("Back did not return to connection history")
	}
	code, err := qrcode.New("retty://connect?address=disposable-test-fixture", qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	a.phone = &phonePair{link: "fixture connection", qr: code.Bitmap()}
	a.desktops.showLocal = true
	tt.Frame()
	if _, ok := tt.Find("手机连接二维码"); !ok {
		t.Fatal("local QR missing from drawer")
	}
	saveDesktopImage(t, tt, "desktop-local-qr-dark.png")
	if err := tt.Click("复制连接码"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != a.phone.link {
		t.Fatal("local connection code was not copied")
	}
	tt.SetSize(560, 340)
	tt.Frame()
	for _, label := range []string{"返回", "关闭桌面连接"} {
		r, ok := tt.Find(label)
		if !ok || r.W < 28 || r.H < 28 || r.X < 0 || r.X+r.W > 560 || r.Y+r.H > 340 {
			t.Fatal("drawer navigation clipped at minimum window size", label, r)
		}
	}
	if err := tt.Click("关闭桌面连接"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.desktops.open || !tt.Focused("Terminal") {
		t.Fatal("closing the drawer did not restore terminal focus")
	}
	a.settingsOpen, a.settingsSection = true, 3
	tt.SetSize(1000, 620)
	tt.Frame()
	if _, ok := tt.Find("Connections"); ok || slices.Contains(tt.Texts(), "最近连接") {
		t.Fatal("connection management remains in settings")
	}
}

func TestDesktopDrawerClearsOnlySelectedConnections(t *testing.T) {
	a, tt := newStaticTestApp(t)
	first, second := &desktopHost{key: "one", recent: desktopRecent{Name: "First"}}, &desktopHost{key: "two", recent: desktopRecent{Name: "Second"}}
	a.desktops.hosts = []*desktopHost{first, second}
	a.desktops.recentSessions = []mobileRecentSession{{Desktop: "one", Session: "a"}, {Desktop: "two", Session: "b"}}
	a.showDesktopConnections(nil)
	tt.Frame()
	for _, label := range []string{"清除桌面连接记录", "选择电脑 First", "确认清除桌面连接记录"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if len(a.desktops.hosts) != 1 || a.desktops.hosts[0] != second || len(a.desktops.recentSessions) != 1 || a.desktops.recentSessions[0].Desktop != "two" || a.desktops.historySelection != nil {
		t.Fatal("selective removal deleted the wrong connection or session shortcut")
	}
}

func saveDesktopImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("MYGO_TEST_IMAGES")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopTailcatConnectAndReconnect(t *testing.T) {
	if os.Getenv("RETTY_REMOTE_E2E") != "1" {
		t.Skip("set RETTY_REMOTE_E2E=1 for private Tailcat fixture")
	}
	a, tt := newTestApp(t)
	desktop, socket := desktopFixtureClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bridge, err := remote.Start(ctx, socket, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	a.desktops.input = bridge.Link()
	a.submitDesktopConnection()
	waitFor(t, tt, "encrypted desktop connection", func() bool { return a.desktops.selected.connected() || a.desktops.selected.err != "" })
	h := a.desktops.selected
	if !h.connected() {
		t.Fatal(h.err)
	}
	if h.quality == nil {
		t.Fatal("connected host has no quality collector")
	}
	waitFor(t, tt, "automatic tunnel path discovery without popover", func() bool { return !h.quality.Snapshot(time.Now()).Path.At.IsZero() })
	if sample := h.quality.Snapshot(time.Now()).Path; sample.At.IsZero() || sample.Latency <= 0 {
		t.Fatal("encrypted tunnel did not provide real path latency", sample)
	}
	existing, err := desktop.Create(rex.CreateOptions{Dir: "/tmp", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	a.openDesktopSession(h, existing, true)
	a.createDesktopSession(h, "/tmp", nil, false)
	waitFor(t, tt, "session on connected computer", func() bool { return len(a.tabs) == 3 })
	a.selectTab(1)
	p := a.tab().Focus
	oldClient := h.client
	waitFor(t, tt, "existing poll records request metrics", func() bool { return h.quality.Snapshot(time.Now()).Successes > 0 })
	oldClient.Close()
	waitFor(t, tt, "automatically reconnected transport", func() bool { return h.connected() && h.client != oldClient && p.terminal().remoteView.inputReady() })
	if !h.connected() || p.terminal().info.PID != existing.PID || p.terminal().SID != existing.ID {
		t.Fatal("reconnect replaced session", h.err)
	}
	if sample := h.quality.Snapshot(time.Now()); sample.Reconnects != 1 || !sample.HasRecovery {
		t.Fatal("reconnect not recorded in host quality", sample)
	}
	tt.Frame()
	tt.Type("printf 'tunnel-%s\\n' continued")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "input after reconnect", func() bool { return strings.Contains(p.terminal().term.Text(), "tunnel-continued") })
	a.disconnectDesktop(h)
	live, err := desktop.List()
	if err != nil || len(live) != 2 {
		t.Fatal("disconnect destroyed remote sessions", err)
	}
	a.quitting = true
	a.tabs[0].Focus.closed = true
	a.tabs[0].Focus.terminal().term.Close()
}

func TestDesktopHistorySerializationDoesNotEnterLayout(t *testing.T) {
	host := &desktopHost{key: "remote"}
	a := &App{tabs: []*Tab{{Host: host, Root: &Node{Pane: &Pane{content: &terminalPane{SID: "remote-session"}}}}, {Root: &Node{Pane: &Pane{content: &terminalPane{SID: "local-session"}}}}}, active: 1}
	b, err := json.Marshal(a.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "remote-session") || a.snapshot().Active != 0 {
		t.Fatal("remote session persisted in local daemon layout")
	}
}

func TestDesktopRestoreSplitsAndAdaptActiveRemoteGrid(t *testing.T) {
	a, tt := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	a.createDesktopSession(h, "/tmp", nil, false)
	waitFor(t, tt, "remote shell", func() bool { return len(a.tabs) == 2 })
	p := a.tab().Focus
	waitFor(t, tt, "remote input attachment", func() bool { return p.terminal().remoteView.inputReady() })
	tt.Frame()
	tt.Type("printf 'grid-%s\\n' ready")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "remote stream attached", func() bool { return strings.Contains(p.terminal().term.Text(), "grid-ready") })
	before, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	tt.SetSize(560, 340)
	time.Sleep(600 * time.Millisecond) // allow the normal resize debounce to fire
	after, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	if before[0].Cols == after[0].Cols && before[0].Rows == after[0].Rows || before[0].PID != after[0].PID {
		t.Fatal("active remote pane did not adapt without replacing its shell", before, after)
	}
	a.split(false)
	waitFor(t, tt, "split", func() bool { return len(a.tab().panes()) == 2 })
	old := a.tab()
	old.Name = "远端项目"
	old.Zoom = old.Focus
	saved := a.snapshotHost(h)
	originalIDs := []string{old.panes()[0].terminal().SID, old.panes()[1].terminal().SID}
	a.quitting = true
	for _, p := range old.panes() {
		p.closed = true
		p.terminal().term.Close()
	}
	live, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	restoredHost := fixtureDesktopHost(t, client)
	restoredHost.sessions, restoredHost.layout = liveDesktopSessions(live), saved
	reopened := &App{client: a.client, desktops: desktopConnections{hosts: []*desktopHost{restoredHost}}}
	reopened.restoreDesktopLayout(restoredHost)
	if len(reopened.tabs) != 1 || reopened.tabs[0].Name != "远端项目" || len(reopened.tabs[0].panes()) != 2 || reopened.tabs[0].Zoom == nil {
		t.Fatal("remote layout was flattened or renamed")
	}
	for i, p := range reopened.tabs[0].panes() {
		if p.terminal().SID != originalIDs[i] {
			t.Fatal("restore replaced remote session")
		}
	}
	reopened.quitting = true
	for _, p := range reopened.tabs[0].panes() {
		p.closed = true
		p.terminal().term.Close()
	}
	// One terminated leaf collapses a split; restoration must not start shells.
	if err := client.Kill(originalIDs[0]); err != nil {
		t.Fatal(err)
	}
	live, err = client.List()
	if err != nil {
		t.Fatal(err)
	}
	restoredHost.sessions, restoredHost.layout = liveDesktopSessions(live), saved
	collapsed := &App{client: a.client}
	collapsed.restoreDesktopLayout(restoredHost)
	if len(collapsed.tabs) != 1 || len(collapsed.tabs[0].panes()) != 1 || collapsed.tabs[0].Focus.terminal().SID != originalIDs[1] {
		t.Fatal("missing remote leaf recreated a shell or lost the surviving session")
	}
	final, _ := client.List()
	if len(final) != len(live) {
		t.Fatal("restore created new remote sessions")
	}
	collapsed.quitting = true
	collapsed.tabs[0].Focus.closed = true
	collapsed.tabs[0].Focus.terminal().term.Close()
	a.tabs[0].Focus.closed = true
	a.tabs[0].Focus.terminal().term.Close()
}

func TestSharedDesktopHistoryStore(t *testing.T) {
	dir := t.TempDir()
	store := &desktopConnectionStore{path: filepath.Join(dir, "connections.json")}
	history := []desktopRecent{{Link: recentTestLink(), Name: "Fixture", ID: "machine:fixture"}}
	data, _ := json.Marshal(history)
	if err := store.Set("history", data); err != nil {
		t.Fatal(err)
	}
	first := readDesktopHistory(store)
	if len(first) != 1 || first[0].ID != history[0].ID || first[0].Link != history[0].Link {
		t.Fatal("shared history schema did not roundtrip")
	}
	if err := store.Set("desktop-layouts", []byte(`{"machine:fixture":{"tabs":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if len(readDesktopHistory(store)) != 1 {
		t.Fatal("layout write replaced capability history")
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("capability record is not private", info.Mode())
	}
	if err := store.Delete("history"); err != nil {
		t.Fatal(err)
	}
	if len(readDesktopHistory(store)) != 0 {
		t.Fatal("removed history resurrected")
	}
}
