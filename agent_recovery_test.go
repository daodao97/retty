package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func newRecoveryTestApp(t *testing.T, missingDir, failCLI bool) (*App, *ui.Tester, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "retty-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude-config"))
	project := filepath.Join(dir, "original project")
	if !missingDir {
		os.Mkdir(project, 0o700)
	}
	l := savedLayout{Tabs: []savedTab{{Name: "Work", Focus: 1, Zoom: true, Root: &savedNode{Vertical: true, Ratio: 0.4,
		A: &savedNode{SID: "agent-pane", Dir: project, Cols: 90, Rows: 30}, B: &savedNode{SID: "shell-pane", Dir: dir}}}}}
	data, _ := json.Marshal(l)
	if err := os.WriteFile(filepath.Join(dir, "layout.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(map[string]any{"version": 1, "sessions": map[string]any{"agent-pane": map[string]any{"agent": "claude", "sessionID": "exact-thread", "dir": project}}})
	if err := os.WriteFile(filepath.Join(dir, "agent-resume.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o700)
	script := "#!/bin/sh\nprintf 'resumed=%s\\n' \"$@\"\nwhile IFS= read -r line; do :; done\n"
	if failCLI {
		t.Setenv("RESUME_FIXTURE_EXIT_GATE", filepath.Join(dir, "allow-cli-exit"))
		script = "#!/bin/sh\nwhile [ ! -e \"$RESUME_FIXTURE_EXIT_GATE\" ]; do sleep 0.01; done\necho 'conversation unavailable'\nexit 42\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	done := make(chan error, 1)
	go func() { done <- rex.Serve() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(rex.SocketPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{client: client}
	a.hello, err = client.Hello()
	if err != nil || !a.hello.AgentRecovery {
		t.Fatal("missing recovery capability", err)
	}
	registerFonts()
	if !a.restore() {
		t.Fatal("layout did not restore")
	}
	t.Cleanup(func() {
		a.quitting = true
		for _, tab := range a.tabs {
			for _, p := range tab.panes() {
				p.closed = true
				a.closePaneTerminal(p)
			}
		}
		client.Shutdown()
		client.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("owned daemon did not stop")
		}
		os.RemoveAll(dir)
	})
	return a, ui.NewTester(a.view, 1000, 620), project
}

func allowRecoveryFixtureExit(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("RESUME_FIXTURE_EXIT_GATE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRecoveryRestoresLayoutAndReusesLiveProcess(t *testing.T) {
	a, tt, project := newRecoveryTestApp(t, false, false)
	tab := a.tab()
	ps := tab.panes()
	if len(ps) != 2 || tab.Name != "Work" || !tab.Root.Vertical || tab.Root.Ratio != 0.4 || tab.Focus != ps[1] || tab.Zoom != ps[1] {
		t.Fatal("layout changed")
	}
	p := ps[0]
	if p.terminal().SID != "agent-pane" || !p.terminal().info.Resumed || p.terminal().startDir != project {
		t.Fatal("Agent pane lost its identity")
	}
	pid := p.terminal().info.PID
	waitFor(t, tt, "resume exact conversation", func() bool {
		return p.terminalView() != nil && strings.Contains(p.terminal().term.Text(), "resumed=exact-thread")
	})
	a.saveNow()
	for _, p := range ps {
		p.closed = true
		a.closePaneTerminal(p)
	}
	b := &App{client: a.client, hello: a.hello}
	if !b.restore() {
		t.Fatal("GUI reopen failed")
	}
	if in := b.tab().panes()[0].terminal().info; in.PID != pid || in.ID != "agent-pane" {
		t.Fatal("GUI reopen relaunched Agent", in)
	}
	t.Cleanup(func() {
		b.quitting = true
		for _, p := range b.tab().panes() {
			p.closed = true
			b.closePaneTerminal(p)
		}
	})
}

func TestAgentRecoveryKeepsFailedPaneAndRetries(t *testing.T) {
	a, tt, project := newRecoveryTestApp(t, true, false)
	p := a.tab().panes()[0]
	if p.terminal().SID != "" || p.terminal().recoverySID != "agent-pane" || p.terminal().recoveryError == "" {
		t.Fatal("missing directory silently replaced Agent with shell")
	}
	if node := a.snapshot().Tabs[0].Root.A; node.SID != "agent-pane" || node.Dir != project {
		t.Fatal("failed recovery lost saved identity", node)
	}
	a.tab().Zoom = nil
	tt.Frame()
	if !slices.Contains(tt.Texts(), "恢复失败") {
		t.Fatal("missing recovery feedback", tt.Texts())
	}
	os.Mkdir(project, 0o700)
	a.retryAgentRestore(p)
	a.retryAgentRestore(p) // repeated clicks must not create multiple requests/PTYs
	waitFor(t, tt, "explicit recovery retry", func() bool { return !p.terminal().recoveryBusy && p.terminal().SID == "agent-pane" })
	if p.terminal().recoveryError != "" || !p.terminal().info.Resumed || p.closed || len(a.tab().panes()) != 2 {
		t.Fatal("retry did not retain pane/layout")
	}
	a.closePane(p)
	if len(a.tab().panes()) != 1 {
		t.Fatal("failed pane could not close")
	}
	// Pane close ends the process asynchronously, as in the normal UI.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		infos, _ := a.client.List()
		found := false
		for _, in := range infos {
			if in.ID == "agent-pane" {
				found = true
			}
		}
		if !found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, err := a.client.RestoreAgent("agent-pane", 90, 30, false)
	if err != nil || r.Session != nil || r.Error != "" {
		t.Fatal("closed recovery remained resumable", r, err)
	}
}

func TestAgentRecoveryCLIExitKeepsPaneAndOutput(t *testing.T) {
	a, tt, _ := newRecoveryTestApp(t, false, true)
	p := a.tab().panes()[0]
	a.tab().Zoom = nil
	tt.Frame()
	allowRecoveryFixtureExit(t)
	waitFor(t, tt, "failed CLI feedback", func() bool { return p.terminal().recoveryError != "" })
	if p.closed || len(a.tab().panes()) != 2 || !slices.Contains(tt.Texts(), "恢复失败") {
		t.Fatal("failed CLI removed pane/layout", tt.Texts())
	}
	if p.terminalView() == nil || !strings.Contains(p.terminal().term.Text(), "conversation unavailable") {
		t.Fatal("failed CLI output was lost")
	}
	oldPID := p.terminal().info.PID
	if err := os.WriteFile(filepath.Join(rex.Dir(), "bin", "claude"), []byte("#!/bin/sh\nwhile IFS= read -r line; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.retryAgentRestore(p)
	waitFor(t, tt, "retry after CLI exit", func() bool { return !p.terminal().recoveryBusy && p.terminal().recoveryError == "" })
	if p.terminal().info.PID == oldPID || p.terminal().SID != "agent-pane" || p.closed {
		t.Fatal("CLI retry did not reuse pane")
	}
}

func TestAgentRecoveryGUIQuitDuringRetryPreservesSession(t *testing.T) {
	a, tt, project := newRecoveryTestApp(t, true, false)
	p := a.tab().panes()[0]
	os.Mkdir(project, 0o700)
	a.retryAgentRestore(p)
	a.quitting = true
	waitFor(t, tt, "pending restore response after GUI quit", func() bool { return !p.terminal().recoveryBusy })
	infos, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.ID == "agent-pane" && !in.Exited && in.Resumed {
			return
		}
	}
	t.Fatal("ordinary GUI quit killed the restored session")
}

func TestAgentRecoveryReopenFailureRetainsOutput(t *testing.T) {
	a, tt, _ := newRecoveryTestApp(t, false, true)
	allowRecoveryFixtureExit(t)
	waitFor(t, tt, "CLI failure", func() bool { return a.tab().panes()[0].terminal().recoveryError != "" })
	a.saveNow()
	for _, p := range a.tab().panes() {
		p.closed = true
		a.closePaneTerminal(p)
	}
	b := &App{client: a.client, hello: a.hello}
	if !b.restore() {
		t.Fatal("failed workspace did not reopen")
	}
	t.Cleanup(func() {
		b.quitting = true
		for _, p := range b.tab().panes() {
			p.closed = true
			b.closePaneTerminal(p)
		}
	})
	p := b.tab().panes()[0]
	if p.terminal().recoveryError == "" || p.terminal().SID != "agent-pane" {
		t.Fatal("reopen lost failed pane")
	}
	b.tab().Zoom = nil
	waitFor(t, ui.NewTester(b.view, 1000, 620), "failed CLI screen replay", func() bool { return strings.Contains(p.terminal().term.Text(), "conversation unavailable") })
}

func TestAgentRecoveryShutdownKeepsLayoutAfterProcessExit(t *testing.T) {
	a, tt, _ := newRecoveryTestApp(t, false, true)
	a.shuttingDown = func() bool { return true }
	a.tab().Zoom = nil
	allowRecoveryFixtureExit(t)
	panes := a.tab().panes()
	p := panes[0]
	waitFor(t, tt, "owned resumed process exits during shutdown", func() bool {
		infos, _ := a.client.List()
		for _, in := range infos {
			if in.ID == p.terminal().SID {
				return in.Exited
			}
		}
		return false
	})
	// Also exercise the pending asynchronous exit-check response.
	a.checkAgentRestoreExit(p, p.terminal().stream)
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.postMu.Lock()
		ready := len(a.posted) > 0
		a.postMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown exit callback did not arrive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	a.saveNow()
	if p.closed || p.terminal().recoveryError != "" || len(a.tab().panes()) != 2 {
		t.Fatal("shutdown changed workspace or recorded a retry failure")
	}
	raw, err := a.client.Layout()
	var layout savedLayout
	if err != nil || json.Unmarshal(raw, &layout) != nil || layout.Tabs[0].Root.A.SID != "agent-pane" {
		t.Fatal("shutdown lost durable pane identity", err)
	}
}

func TestShutdownKeepsOrdinaryShellPane(t *testing.T) {
	a, tt := newTestApp(t)
	a.shuttingDown = func() bool { return true }
	p := a.tab().Focus
	p.terminal().term.Send([]byte("exit 0\r"))
	waitFor(t, tt, "owned shell exits during shutdown", func() bool {
		infos, _ := a.client.List()
		return len(infos) == 1 && infos[0].Exited
	})
	tt.Frame()
	if p.closed || len(a.tabs) != 1 || a.snapshot().Tabs[0].Root.SID != p.terminal().SID {
		t.Fatal("automatic shell exit removed shutdown layout")
	}
}
