package main

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"io"
	"net"
	"os/exec"
	"retty/internal/rex"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRemotePaneAndPhoneTransferActiveSession(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("requires Python PTY fixture")
	}
	a, desktop := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	script := `import os,fcntl,termios,struct,signal,tty,time
tty.setraw(0)
def draw(*_):
    h,w,_,_=struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,b'\0'*8))
    os.write(1,('\x1b[?1049h\x1b[2J\x1b[1;1HSIZE %d %d'%(w,h)).encode())
signal.signal(signal.SIGWINCH, draw)
draw()
while True:
    time.sleep(1)`
	in, err := client.Create(rex.CreateOptions{Command: []string{python, "-u", "-c", script}, Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	a.openDesktopSession(h, in, true)
	p := a.tab().Focus
	defer a.closePaneTerminal(p)
	refreshRemote := func() rex.SessionInfo {
		infos, err := client.List()
		if err != nil || len(infos) != 1 {
			t.Fatalf("session disappeared: %v", err)
		}
		a.applyHost(h, map[string]rex.SessionInfo{in.ID: infos[0]})
		return infos[0]
	}
	waitFor(t, desktop, "desktop-sized TUI", func() bool {
		s := refreshRemote()
		cols, rows := p.terminal().term.Size()
		return cols > 60 && s.SizeLock == p.terminal().remoteOwner && s.Cols == cols && s.Rows == rows && strings.Contains(p.terminal().term.Text(), fmt.Sprintf("SIZE %d %d", cols, rows))
	})
	retainedTerm, retainedView := p.terminal().term, p.terminal().remoteView
	retainedReader := p.terminal().remoteView.transport()
	phone, err := client.Redial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	m := &mobileApp{client: phone, hello: h.hello, sizeLock: true}
	m.openSession(p.terminal().info)
	defer m.detach()
	tt := ui.NewTester(m.view, 390, 680)
	waitFor(t, tt, "phone takes session and desktop keeps read-only viewer", func() bool {
		s := refreshRemote()
		m.followSizeLock(s)
		desktop.Frame()
		return s.SizeLock == m.lockOwner && s.Cols < 60 && p.terminal().remoteYielded && p.terminal().remoteView.hasTransport() && !p.terminal().remoteView.inputReady()
	})
	phoneOwner := m.lockOwner
	a.selectTab(0)
	desktop.Frame()
	a.selectTab(1)
	desktop.Frame()
	if p.terminal().remoteView.transport() != retainedReader || p.terminal().remoteView.inputReady() || p.terminal().stream != nil {
		t.Fatal("returning to a phone-owned tab replaced its reader or took control")
	}
	for range 10 {
		desktop.Frame()
		tt.Frame()
		s := refreshRemote()
		if s.SizeLock != phoneOwner || s.Cols >= 60 || s.PID != in.PID {
			t.Fatal("inactive desktop reclaimed phone size or restarted task")
		}
	}
	if p.terminal().term != retainedTerm || p.terminal().remoteView != retainedView || p.terminal().remoteView.transport() != retainedReader || a.tab().Focus != p {
		t.Fatal("handoff destroyed desktop tab or retained frame")
	}
	if err := desktop.Click("接入当前窗格"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, desktop, "desktop takes session and phone exits view", func() bool {
		s := refreshRemote()
		m.followSizeLock(s)
		tt.Frame()
		cols, rows := p.terminal().term.Size()
		return s.SizeLock == p.terminal().remoteOwner && s.Cols == cols && s.Rows == rows && cols > 60 && m.term == nil && strings.Contains(p.terminal().term.Text(), fmt.Sprintf("SIZE %d %d", cols, rows))
	})
	for range 10 {
		tt.Frame()
		desktop.Frame()
		s := refreshRemote()
		if s.SizeLock != p.terminal().remoteOwner || s.PID != in.PID {
			t.Fatal("inactive phone reclaimed desktop ownership")
		}
	}
	owner := p.terminal().remoteOwner
	a.selectTab(0)
	desktop.Frame()
	select {
	case <-p.terminal().remoteRelease:
	case <-time.After(2 * time.Second):
		t.Fatal("inactive tab did not release size")
	}
	if s := refreshRemote(); s.SizeLock != "" || s.PID != in.PID || !p.terminal().remoteView.hasTransport() || p.terminal().remoteView.inputReady() {
		t.Fatal("tab switch lost reader, kept input, or ended task")
	}
	a.selectTab(1)
	desktop.Frame()
	waitFor(t, desktop, "same session resumes in selected tab", func() bool {
		s := refreshRemote()
		return s.SizeLock == p.terminal().remoteOwner && p.terminal().remoteOwner != owner && s.PID == in.PID
	})
	desktop.SetSize(720, 480)
	waitFor(t, desktop, "active pane follows window size", func() bool {
		s := refreshRemote()
		cols, rows := p.terminal().term.Size()
		return s.Cols == cols && s.Rows == rows && strings.Contains(p.terminal().term.Text(), fmt.Sprintf("SIZE %d %d", cols, rows))
	})
	a.focusedWin = false
	a.syncRemotePaneActivity()
	select {
	case <-p.terminal().remoteRelease:
	case <-time.After(2 * time.Second):
		t.Fatal("inactive window did not release size")
	}
	if s := refreshRemote(); s.SizeLock != "" || !p.terminal().remoteView.hasTransport() || p.terminal().remoteView.inputReady() {
		t.Fatal("window blur lost reader or retained input")
	}
	a.focusedWin = true
	a.syncRemotePaneActivity()
	waitFor(t, desktop, "window focus resumes same shell", func() bool { s := refreshRemote(); return s.SizeLock == p.terminal().remoteOwner && s.PID == in.PID })
}

func TestRemotePaneBackgroundReaderAndExpiry(t *testing.T) {
	a, tt := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	in, err := client.Create(rex.CreateOptions{Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	a.openDesktopSession(h, in, true)
	p := a.tab().Focus
	defer a.closePaneTerminal(p)
	waitFor(t, tt, "foreground ready", func() bool { return p.terminal().remoteView.inputReady() })
	reader := p.terminal().remoteView.transport()
	a.selectTab(0)
	tt.Frame()
	select {
	case <-p.terminal().remoteRelease:
	case <-time.After(2 * time.Second):
		t.Fatal("did not release ownership")
	}
	writer := client.ViewStream(p.terminal().SID)
	defer writer.Close()
	go io.Copy(io.Discard, writer)
	if _, err := writer.Write([]byte("printf 'background-%s\\n' updated\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "hidden pane consumes output", func() bool { return strings.Contains(p.terminal().term.Text(), "background-updated") })
	infos, err := client.List()
	if err != nil || infos[0].SizeLock != "" || infos[0].PID != in.PID {
		t.Fatal("hidden reader took control or replaced shell", infos, err)
	}
	a.selectTab(1)
	tt.Frame()
	waitFor(t, tt, "warm return", func() bool { return p.terminal().remoteView.inputReady() })
	if p.terminal().remoteView.transport() != reader {
		t.Fatal("warm return reattached reader")
	}
	if slices.Contains(tt.Texts(), "正在载入会话…") {
		t.Fatal("warm return flashed loading")
	}
	a.expireRemotePane(p, reader)
	if !p.terminal().remoteView.hasTransport() {
		t.Fatal("stale expiry closed foreground reader")
	}
	a.selectTab(0)
	tt.Frame()
	if p.terminal().remoteIdleTimer == nil {
		t.Fatal("leaving did not schedule expiry")
	}
	p.terminal().remoteIdleTimer.Stop()
	a.expireRemotePane(p, reader)
	if p.terminal().remoteView.hasTransport() || p.terminal().remoteView.inputReady() || !strings.Contains(p.terminal().term.Text(), "background-updated") {
		t.Fatal("expiry failed to close reader and retain screen")
	}
	a.selectTab(1)
	tt.Frame()
	waitFor(t, tt, "cold return attaches same shell", func() bool { return p.terminal().remoteView.inputReady() })
	if p.terminal().remoteView.transport() == reader || p.terminal().info.PID != in.PID {
		t.Fatal("cold return reused closed reader or replaced shell")
	}
}

func TestRemotePaneLateAcquisitionCannotSurviveTabSwitch(t *testing.T) {
	a, tt := newTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	a.desktops.hosts = []*desktopHost{h}
	in, err := client.Create(rex.CreateOptions{Dir: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	a.openDesktopSession(h, in, true)
	p := a.tab().Focus
	defer a.closePaneTerminal(p)
	// Ownership's LIST completion is queued on the UI dispatcher.
	a.syncRemotePaneActivity()
	a.selectTab(0)
	a.syncRemotePaneActivity()
	waitFor(t, tt, "background snapshot", func() bool { return p.terminal().remoteView.readReady() })
	if p.terminal().stream != nil || p.terminal().remoteView.inputReady() {
		t.Fatal("late acquisition enabled background input")
	}
	infos, err := client.List()
	if err != nil || infos[0].SizeLock != "" || infos[0].PID != in.PID {
		t.Fatal("late acquisition took ownership", infos, err)
	}
}

func TestRemotePaneLoadingBannerDelay(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	view := newSessionViewStream(nil, nil)
	defer view.Close()
	view.setInput(nil)
	view.replace(testMobileTransport{conn})
	p := &Pane{content: &terminalPane{remoteView: view, remoteLoadingSince: time.Now()}}
	a := &App{activeRemote: p}
	tt := ui.NewTester(func(c *ui.Context) { ui.Text(c, a.remotePaneStatus(c, p)) }, 400, 100)
	if slices.Contains(tt.Texts(), "正在载入会话…") {
		t.Fatal("fast attachment flashes loading")
	}
	p.terminal().remoteLoadingSince = tt.Now()
	tt.Frame()
	tt.Advance(remoteLoadingDelay - time.Millisecond)
	if slices.Contains(tt.Texts(), "正在载入会话…") {
		t.Fatal("loading feedback appeared before its delay")
	}
	tt.Advance(time.Millisecond)
	if !slices.Contains(tt.Texts(), "正在载入会话…") {
		t.Fatal("slow attachment has no feedback")
	}
	// A cached screen is ready even before the new input owner is confirmed.
	view.mu.Lock()
	view.paused = false
	view.mu.Unlock()
	tt.Frame()
	if slices.Contains(tt.Texts(), "正在载入会话…") {
		t.Fatal("input handshake masks retained screen")
	}
}
