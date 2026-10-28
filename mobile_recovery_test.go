package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/terminal"
)

// The recovery integration test needs real UI-thread dispatch even though it
// renders offscreen. Ordinary unit tests continue without starting AppKit.
func TestMain(tests *testing.M) {
	if os.Getenv("RETTY_MOBILE_RECOVERY_E2E") != "1" {
		// Sidebar history must never read the developer's real conversations.
		dir, err := os.MkdirTemp("", "retty-test-agent-homes-")
		if err != nil {
			panic(err)
		}
		os.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
		os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
		code := tests.Run()
		os.RemoveAll(dir)
		os.Exit(code)
	}
	code := 1
	mygo.App.WhenReady(func() { go func() { code = tests.Run(); mygo.App.Quit() }() })
	if err := mygo.App.Run(); err != nil {
		os.Exit(1)
	}
	os.Exit(code)
}

func framedRecoveryStream(t *testing.T) (*rex.Stream, net.Conn) {
	t.Helper()
	control, controlPeer := net.Pipe()
	data, peer := net.Pipe()
	client := rex.NewClient(control, func(context.Context) (net.Conn, error) { return data, nil })
	t.Cleanup(func() { client.Close(); controlPeer.Close(); peer.Close() })
	go func() {
		var attach rex.Attach
		if json.NewDecoder(peer).Decode(&attach) == nil {
			peer.Write([]byte("{\"ok\":true,\"screen_frames\":true}\n"))
		}
	}()
	s := client.ViewStream("fixture")
	if err := s.Resize(0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, peer
}

func recoveryFrame(data string) []byte {
	b := make([]byte, 8+len(data))
	binary.BigEndian.PutUint32(b, uint32(len(data)))
	binary.BigEndian.PutUint16(b[4:], 80)
	binary.BigEndian.PutUint16(b[6:], 24)
	copy(b[8:], data)
	return b
}

func TestMobileFailedPartialSnapshotKeepsScreenAndDoesNotReplayInput(t *testing.T) {
	registerFonts()
	failed := make(chan struct{}, 2)
	stream := newSessionViewStream(nil, func() { failed <- struct{}{} })
	term, err := terminal.New(terminal.Options{Conn: stream, FixedCols: 80, FixedRows: 24, ReflowView: true, Font: terminal.Font{Family: termFont.Family, Size: 13}, NoBlink: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte("\x1b[?25lkept complete screen 中文🙂"))
	tt := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Fill() }, 390, 620)
	before := term.Text()
	first, peer := framedRecoveryStream(t)
	stream.replace(first)
	frame := recoveryFrame("\x1b[2Jincomplete replacement")
	go func() { peer.Write(frame[:len(frame)-5]); peer.Close() }()
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("truncated snapshot did not fail")
	}
	tt.Frame()
	if term.Text() != before {
		t.Fatal("failed snapshot erased retained screen")
	}
	next, nextPeer := framedRecoveryStream(t)
	stream.replace(next)
	if stream.inputReady() {
		t.Fatal("input enabled before replacement snapshot")
	}
	stream.Write([]byte("do not replay\r"))
	go nextPeer.Write(recoveryFrame("\x1b[?25lrestored complete screen 中文🙂"))
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(term.Text(), "restored complete screen") && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(term.Text(), "restored complete screen") || strings.Contains(term.Text(), "kept complete screen") || !stream.inputReady() {
		t.Fatal("recovery did not replace complete screen")
	}
	nextPeer.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _ := nextPeer.Read(buf); n != 0 {
		t.Fatal("offline input replayed")
	}
}

// Exercise lifecycle callbacks and main-thread completion with a transport
// still blocked inside its old dial. No real desktop or phone is involved.
func TestMobileForegroundAbandonsStalledTunnelWithoutLeavingPage(t *testing.T) {
	if os.Getenv("RETTY_MOBILE_RECOVERY_E2E") != "1" {
		t.Skip("set RETTY_MOBILE_RECOVERY_E2E=1 for UI dispatch")
	}
	registerFonts()
	for _, page := range []string{"terminal", "sessions"} {
		t.Run(page, func(t *testing.T) {
			conn, peer := net.Pipe()
			defer peer.Close()
			release := make(chan struct{})
			defer close(release)
			client := rex.NewClient(conn, func(context.Context) (net.Conn, error) {
				<-release
				return nil, context.Canceled
			})
			defer client.Close()
			cached := rex.SessionInfo{ID: "retained", PID: 123, Cols: 80, Rows: 24}
			// An invalid fixture link stops the fresh-connect phase before it
			// reaches any network; its diagnostic proves that phase was entered.
			m := &mobileApp{client: client, link: "fixture", sessions: []rex.SessionInfo{cached}}
			var retained *terminal.Terminal
			mygo.RunOnMain(func() {
				if page == "terminal" {
					stream := newSessionViewStream(nil, nil)
					var err error
					retained, err = terminal.New(terminal.Options{Conn: stream, Font: terminal.Font{Family: termFont.Family, Size: 13}})
					if err != nil {
						t.Error(err)
						return
					}
					retained.Feed([]byte("retained screen"))
					m.term, m.stream, m.selected = retained, stream, cached
				}
				m.enterBackground()
				m.enterForeground()
			})
			defer mygo.RunOnMain(func() { m.disconnect(false) })
			deadline := time.Now().Add(mobileResumeTimeout + 2*time.Second)
			recovered := false
			for time.Now().Before(deadline) {
				mygo.RunOnMain(func() { recovered = m.generation > 0 && m.connectionIssue != nil })
				if recovered {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !recovered {
				t.Fatal("foreground recovery never abandoned the suspended tunnel")
			}
			mygo.RunOnMain(func() {
				if m.home || !m.reconnecting || m.term != retained || !reflect.DeepEqual(m.sessions, []rex.SessionInfo{cached}) {
					t.Error("fresh-connect fallback lost the current page or cached session")
				}
				if retained != nil && !strings.Contains(retained.Text(), "retained screen") {
					t.Error("fresh-connect fallback erased terminal contents")
				}
				if len(m.connectionDiagnostics) == 0 || m.connectionDiagnostics[0].Stage != remote.StageLink {
					t.Error("fallback did not reach the fresh connection path")
				}
			})
			select {
			case <-client.Closed():
			case <-time.After(time.Second):
				t.Error("expired retained control was left open")
			}
		})
	}
}

// Uses only a private fixture: no user's terminal, daemon or desktop bridge is
// interrupted. Exercise the real Tailcat, control polling and viewer recovery.
func TestMobileRecoveryAcrossDesktopBridgeRestart(t *testing.T) {
	if os.Getenv("RETTY_MOBILE_RECOVERY_E2E") != "1" {
		t.Skip("set RETTY_MOBILE_RECOVERY_E2E=1 for live Tailcat and UI dispatch")
	}
	app, _ := newTestApp(t)
	existing, err := app.client.List()
	if err != nil || len(existing) == 0 {
		t.Fatal("fixture has no session", err)
	}
	dir := rex.Dir()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	bridge, err := remote.Start(ctx, rex.SocketPath(), remote.Options{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	link := bridge.Link()
	phone, closeTunnel, err := remote.Connect(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := phone.Hello()
	if err != nil {
		t.Fatal(err)
	}
	m := &mobileApp{client: phone, closeTunnel: closeTunnel, hello: hello, link: link, sessions: existing}
	defer mygo.RunOnMain(func() { m.disconnect(false) })
	var tt *ui.Tester
	mygo.RunOnMain(func() { m.openSession(existing[0]); tt = ui.NewTester(m.view, 390, 620) })
	wait := func(timeout time.Duration, condition func() bool) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			ready := false
			mygo.RunOnMain(func() { tt.Frame(); ready = condition() })
			if ready {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	if !wait(10*time.Second, func() bool { return m.stream != nil && m.stream.HasScreenSize() }) {
		t.Fatal("fixture viewer did not attach")
	}
	var retainedTerm *terminal.Terminal
	var retainedStream *sessionViewStream
	mygo.RunOnMain(func() { retainedTerm, retainedStream = m.term, m.stream; m.startPolling(phone, m.generation) })
	bridge.Close()
	if !wait(5*time.Second, func() bool { return m.reconnecting && m.term == retainedTerm }) {
		t.Fatal("connection loss ended the reader or lost its page")
	}
	newBridge, err := remote.Start(ctx, rex.SocketPath(), remote.Options{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer newBridge.Close()
	if !wait(35*time.Second, func() bool {
		return !m.reconnecting && m.stream == retainedStream && m.stream.inputReady() && m.term == retainedTerm && m.selected.ID == existing[0].ID && m.navigation.Path() == "/sessions/terminal"
	}) {
		t.Fatal("automatic recovery did not retain the same terminal/session")
	}
	after, err := app.client.List()
	if err != nil || len(after) != len(existing) || after[0].Cols != existing[0].Cols || after[0].Rows != existing[0].Rows {
		t.Fatal("recovery created sessions or resized the desktop PTY", err)
	}
	// A long locked/background visit restores the same screen and session too.
	mygo.RunOnMain(func() {
		m.enterBackground()
		// Simulate the control socket expiring while iOS suspended its
		// timers; the authenticated tunnel and terminal must stay reusable.
		m.client.Close()
		m.enterForeground()
	})
	if !wait(35*time.Second, func() bool {
		return !m.reconnecting && m.term == retainedTerm && m.selected.ID == existing[0].ID && m.stream.inputReady()
	}) {
		t.Fatal("long background visit did not resume the retained terminal")
	}
	// Returning to home must not resume an old connection. Only clicking the
	// recent session establishes a fresh connection to that session's desktop.
	mygo.RunOnMain(func() {
		m.goHome()
		m.enterBackground()
		m.enterForeground()
	})
	if !wait(time.Second, func() bool {
		return m.home && m.client == nil && !m.busy && !m.reconnecting && m.navigation.Path() == "/connect"
	}) {
		t.Fatal("home restored the old connection without an explicit selection")
	}
	mygo.RunOnMain(func() {
		tt.Click("进入最近会话 " + m.desktopKey() + " " + existing[0].ID)
	})
	if !wait(35*time.Second, func() bool {
		return !m.busy && !m.reconnecting && m.client != nil && m.term != nil && m.selected.ID == existing[0].ID && m.stream.inputReady() && m.navigation.Path() == "/sessions/terminal"
	}) {
		t.Fatal("explicit recent-session selection did not reconnect to the original session")
	}
	after, err = app.client.List()
	if err != nil || len(after) != len(existing) || after[0].ID != existing[0].ID || after[0].PID != existing[0].PID || after[0].Cols != existing[0].Cols || after[0].Rows != existing[0].Rows {
		t.Fatal("on-demand reconnect changed the fixture session or its dimensions", err)
	}
}
