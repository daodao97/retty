package main

import (
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"retty/internal/rex"
)

func TestMobileInactiveSessionRestoresDesktopSize(t *testing.T) {
	for _, action := range []string{"back", "switch", "background", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			a, desktop := newTestApp(t)
			p := a.tabs[0].Focus
			waitFor(t, desktop, "desktop geometry", func() bool {
				refresh(a)
				cols, rows := p.term.Size()
				return cols > 60 && p.info.Cols == cols && p.info.Rows == rows
			})
			original := p.info
			phone, err := rex.Connect()
			if err != nil {
				t.Fatal(err)
			}
			defer phone.Close()
			m := &mobileApp{client: phone, sizeLock: true, link: "fixture"}
			m.hello.Version = rex.ProtocolVersion
			defer m.detach()
			defer m.stopPolling()
			m.openSession(original)
			tt := ui.NewTester(m.view, 390, 680)
			waitFor(t, tt, "phone ownership", func() bool {
				refresh(a)
				desktop.Frame()
				return p.locked && p.info.SizeLock == m.lockOwner && p.info.Cols < 60
			})
			owner, retainedTerm, retainedStream := m.lockOwner, m.term, m.stream
			switch action {
			case "back":
				m.navigation.Back()
				tt.Frame()
			case "switch":
				other, err := phone.Create(rex.CreateOptions{Dir: t.TempDir(), Cols: 80, Rows: 24})
				if err != nil {
					t.Fatal(err)
				}
				defer a.client.Kill(other.ID)
				m.openSession(other)
				tt.Frame()
			case "background":
				m.enterBackground()
			case "disconnect":
				m.disconnect(false)
			}
			waitFor(t, desktop, "automatic desktop unlock", func() bool {
				refresh(a)
				return !p.locked && p.info.SizeLock == "" && p.info.Cols == original.Cols && p.info.Rows == original.Rows
			})
			if p.info.PID != original.PID {
				t.Fatal("inactive phone restarted the shell")
			}
			if action != "background" {
				return
			}
			if m.term != retainedTerm || m.stream != retainedStream || m.client != phone || !m.lockSuspended {
				t.Fatal("background discarded the terminal or control connection")
			}
			// Exercise the retained-connection completion on the UI thread,
			// without requiring the native application's dispatch loop.
			sessions, err := phone.List()
			if err != nil {
				t.Fatal("background closed the control connection", err)
			}
			m.background = false
			m.finishRetainedConnection(phone, m.generation, sessions, nil)
			waitFor(t, tt, "foreground phone ownership", func() bool {
				refresh(a)
				desktop.Frame()
				return p.locked && p.info.SizeLock == m.lockOwner && p.info.Cols < 60
			})
			if m.lockOwner == owner || m.lockLost || m.lockSuspended || m.term != retainedTerm {
				t.Fatal("foreground did not reacquire a fresh lock on the retained terminal")
			}
		})
	}
}

// While a phone holds the session's size, the pane shows the session at
// that size and its own resizes are ignored; Unlock gives it back.
func TestPaneFollowsPhoneSizeLockUntilUnlocked(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tabs[0].Focus
	// Let the desktop view submit its actual size before remembering it. The
	// session's 80x24 creation size is already >48, but is not its drawn size.
	tt.Frame()
	waitFor(t, tt, "desktop size", func() bool {
		refresh(a)
		cols, rows := p.term.Size()
		return p.info.Cols > 48 && p.info.Cols == cols && p.info.Rows == rows && p.info.SizeLock == ""
	})
	desktop := [2]int{p.info.Cols, p.info.Rows}
	phone := a.client.LockStream(p.SID, "phone-run", "Test iPhone", 48, 35)
	defer phone.Close()
	phone.Resize(48, 35)
	waitFor(t, tt, "phone lock", func() bool {
		refresh(a)
		return p.locked && p.info.Cols == 48 && p.info.Rows == 35
	})
	tt.Frame()
	if !tt.HasText("Unlock Size") {
		t.Fatalf("no unlock control; texts %q", tt.Texts())
	}
	// Window frames keep the phone's size.
	for range 5 {
		tt.Frame()
		time.Sleep(50 * time.Millisecond)
	}
	refresh(a)
	if p.info.Cols != 48 || p.info.Rows != 35 {
		t.Fatalf("pane took the size back: %dx%d", p.info.Cols, p.info.Rows)
	}
	if cols, rows := p.term.Size(); cols != 48 || rows != 35 {
		t.Fatalf("locked pane decodes at %dx%d", cols, rows)
	}
	if dir := os.Getenv("MYGO_TEST_IMAGES"); dir != "" {
		os.MkdirAll(dir, 0o755)
		f, err := os.Create(filepath.Join(dir, "size-lock-desktop.png"))
		if err == nil {
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
	if err := tt.Click("Unlock Size"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "unlock", func() bool {
		refresh(a)
		return !p.locked && p.info.SizeLock == "" && p.info.Cols > 48
	})
	tt.Frame()
	if tt.HasText("Unlock Size") {
		t.Fatal("unlock control remained")
	}
	// The pane has the size it had before the lock, which its stream
	// already sent once; it must still take it back.
	if p.info.Cols != desktop[0] || p.info.Rows != desktop[1] {
		t.Fatalf("pane did not take its size back: %dx%d, want %dx%d", p.info.Cols, p.info.Rows, desktop[0], desktop[1])
	}
}

// With the setting on, an opened full-screen program redraws at the phone's
// size and the phone shows it natively; once a window unlocks, the phone
// falls back to reflowing the desktop's screen without retaking the size.
func TestMobileSizeLockRedrawsAtPhoneSizeUntilUnlocked(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("requires Python PTY fixture")
	}
	a, _ := newTestApp(t)
	script := `import os,fcntl,termios,struct,signal,tty,time
tty.setraw(0)
def draw(*_):
    h,w,_,_=struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,b'\0'*8))
    os.write(1,('\x1b[?1049h\x1b[2J\x1b[1;1HSIZE %d %d'%(w,h)).encode())
signal.signal(signal.SIGWINCH, draw)
draw()
while True:
    time.sleep(1)`
	info, err := a.client.Create(rex.CreateOptions{Command: []string{python, "-u", "-c", script}, Dir: t.TempDir(), Cols: 180, Rows: 58})
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.Kill(info.ID)
	phone, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	hello, err := phone.Hello()
	if err != nil {
		t.Fatal(err)
	}
	m := &mobileApp{client: phone, hello: hello, sessions: []rex.SessionInfo{info}, sizeLock: true}
	m.openSession(info)
	defer m.detach()
	tt := ui.NewTester(m.view, 390, 680)
	var cols, rows int
	waitFor(t, tt, "redraw at the phone's size", func() bool {
		cols, rows = m.term.Size()
		return cols < 60 && strings.Contains(m.term.Text(), fmt.Sprintf("SIZE %d %d", cols, rows))
	})
	list, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	var locked rex.SessionInfo
	for _, in := range list {
		if in.ID == info.ID {
			locked = in
		}
	}
	if locked.SizeLock != m.lockOwner || locked.Cols != cols || locked.Rows != rows {
		t.Fatalf("server %dx%d lock %q, phone %dx%d", locked.Cols, locked.Rows, locked.SizeLock, cols, rows)
	}
	if err := a.client.UnlockSize(info.ID, 180, 58); err != nil {
		t.Fatal(err)
	}
	m.selected = locked
	waitFor(t, tt, "fallback to the desktop's screen", func() bool {
		list, _ := phone.List()
		for _, in := range list {
			if in.ID == info.ID {
				m.followSizeLock(in)
			}
		}
		return m.lockLost && m.term != nil && strings.Contains(m.term.Text(), "SIZE 180 58")
	})
	for range 10 {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	list, _ = a.client.List()
	for _, in := range list {
		if in.ID == info.ID && (in.SizeLock != "" || in.Cols != 180 || in.Rows != 58) {
			t.Fatalf("phone retook the size: %dx%d lock %q", in.Cols, in.Rows, in.SizeLock)
		}
	}
}
