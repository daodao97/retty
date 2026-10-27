package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func TestRemoteSessionAppearsWithoutChangingDesktopFocus(t *testing.T) {
	a, tt := newTestApp(t)
	tt.Frame()
	active, focus, request := a.tab(), a.tab().Focus, a.focusReq
	// The first view schedules its stream attachment/resize asynchronously.
	// Record the settled desktop grid, rather than racing the initial 80×24.
	waitFor(t, tt, "initial desktop size", func() bool {
		cols, rows := focus.terminal().term.Size()
		infos, err := a.client.List()
		if err != nil {
			return false
		}
		for _, in := range infos {
			if in.ID == focus.terminal().SID {
				return in.Cols == cols && in.Rows == rows
			}
		}
		return false
	})
	before, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	created, err := phone.Create(rex.CreateOptions{Dir: "/tmp", Cols: 48, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	phoneStream := phone.ViewStream(created.ID)
	defer phoneStream.Close()
	go io.Copy(io.Discard, phoneStream)
	if _, err := phoneStream.Write([]byte("export RETTY_CONTINUATION=mobile; printf 'phone-%s\\n' created\n")); err != nil {
		t.Fatal(err)
	}
	refresh(a)
	refresh(a)
	if len(a.tabs) != 2 || a.tab() != active || active.Focus != focus || a.focusReq != request {
		t.Fatal("remote session duplicated or changed desktop focus")
	}
	p := a.tabs[1].Focus
	if p.terminal().SID != created.ID || p.Tab != a.tabs[1] || p.Node != a.tabs[1].Root {
		t.Fatal("remote pane was not attached correctly")
	}
	after, err := phone.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range after {
		if in.ID == before[0].ID && (in.PID != before[0].PID || in.Cols != before[0].Cols || in.Rows != before[0].Rows) {
			t.Fatal("existing terminal changed", before, after)
		}
		if in.ID == created.ID && (in.PID != created.PID || in.Cols != 48 || in.Rows != 24) {
			t.Fatal("remote terminal resized while hidden", in)
		}
	}
	waitFor(t, tt, "mobile output in the desktop tab", func() bool {
		return strings.Contains(p.terminal().term.Text(), "phone-created")
	})
	a.selectTab(1)
	tt.Frame()
	tt.Type("printf 'desktop-%s\\n' \"$RETTY_CONTINUATION\"")
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, "desktop input continuing the mobile shell", func() bool {
		return strings.Contains(p.terminal().term.Text(), "desktop-mobile")
	})
	layout, err := json.Marshal(a.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.client.SetLayout(layout); err != nil {
		t.Fatal(err)
	}
	reopened := &App{client: a.client}
	if !reopened.restore() {
		t.Fatal("desktop could not restore the mobile session tab")
	}
	t.Cleanup(func() {
		reopened.quitting = true
		for _, tab := range reopened.tabs {
			for _, pane := range tab.panes() {
				pane.terminal().term.Close()
			}
		}
	})
	if len(reopened.tabs) != 2 || reopened.tab().Focus.terminal().SID != created.ID || reopened.tab().Focus.terminal().info.PID != created.PID {
		t.Fatal("desktop reopening duplicated or replaced the mobile session")
	}
	stale := map[string]rex.SessionInfo{created.ID: created}
	a.closePane(p)
	a.apply(stale)
	if len(a.tabs) != 1 {
		t.Fatal("stale poll resurrected a closed tab")
	}
	if err := phone.Kill(focus.terminal().SID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "externally ended session to disappear", func() bool { return len(a.tabs) == 0 })
}

func TestRestoreAdoptsSessionsWithoutSavedLayout(t *testing.T) {
	a, _ := newTestApp(t)
	other := &App{client: a.client}
	if !other.restore() || len(other.tabs) != 1 || other.tabs[0].Focus.terminal().SID != a.tab().Focus.terminal().SID {
		t.Fatal("sessions without a layout stayed invisible")
	}
	other.quitting = true
	other.tabs[0].Focus.terminal().term.Close()
}

func TestMobileSessionEndRejectsStaleResultsAndLists(t *testing.T) {
	client := &rex.Client{}
	sessions := []rex.SessionInfo{{ID: "target"}, {ID: "keep"}}
	m := &mobileApp{client: client, generation: 3, closingSession: "target", sessions: sessions}
	m.hello.Host.ID = "desktop"
	m.history = []desktopRecent{{ID: "desktop"}}
	m.rememberSession(sessions[0])
	m.setSessionPreference("target", "name", true)
	m.finishSessionEnd(client, 2, "target", nil)
	if len(m.sessions) != 2 || m.closingSession == "" {
		t.Fatal("stale callback changed a new connection")
	}
	m.finishSessionEnd(client, 3, "target", errors.New("offline"))
	if len(m.sessions) != 2 || m.error == "" {
		t.Fatal("failed end removed the session")
	}
	m.closingSession = "target"
	m.finishSessionEnd(client, 3, "target", nil)
	m.updateSessions(sessions, false)
	if len(m.sessions) != 1 || m.sessions[0].ID != "keep" || len(m.recentSessions) != 0 || m.preference("target").Name != "" {
		t.Fatal("ended session or its shortcuts returned", m.sessions, m.recentSessions)
	}
	m.hello.Host.ID = "other-desktop"
	m.updateSessions(sessions, true)
	if len(m.sessions) != 2 {
		t.Fatal("closed-session filter leaked to another desktop")
	}
	m.closingSession, m.endingOpen, m.client = "target", true, nil
	m.pauseConnection()
	if m.closingSession != "" || m.endingOpen {
		t.Fatal("reconnection retained a stale close operation")
	}
}

func TestMobileEndSessionThroughIconControls(t *testing.T) {
	if os.Getenv("RETTY_MOBILE_RECOVERY_E2E") != "1" {
		t.Skip("requires UI-thread dispatch")
	}
	a, desktop := newTestApp(t)
	phone, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	created, err := phone.Create(rex.CreateOptions{Dir: "/tmp", Cols: 48, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := phone.List()
	if err != nil {
		t.Fatal(err)
	}
	m := &mobileApp{client: phone, sessions: sessions, link: "fixture"}
	m.hello.Host.ID = "desktop"
	var tt *ui.Tester
	mygo.RunOnMain(func() {
		tt = ui.NewTester(m.view, 390, 750)
		tt.Click("会话设置 " + created.ID)
		if !m.editingOpen || m.term != nil {
			t.Error("settings icon opened the terminal")
		}
		saveSettingsImage(t, tt, "mobile-session-icons")
		tt.Click("结束会话")
		saveSettingsImage(t, tt, "mobile-session-end-confirm")
		tt.Click("取消")
	})
	if current, _ := phone.List(); len(current) != 2 {
		t.Fatal("cancel ended a session")
	}
	mygo.RunOnMain(func() {
		m.openSession(created)
		m.editSession(created)
		tt.Frame()
		tt.Click("结束会话")
		tt.Click("确认结束会话")
	})
	deadline := time.Now().Add(5 * time.Second)
	finished := false
	for time.Now().Before(deadline) && !finished {
		mygo.RunOnMain(func() { tt.Frame(); finished = m.closingSession == "" && len(m.sessions) == 1 && m.term == nil })
		time.Sleep(20 * time.Millisecond)
	}
	mygo.RunOnMain(func() {
		if !finished || m.reconnecting || m.generation != 0 || m.selected.ID != "" || m.error != "" {
			t.Error("ending viewed terminal triggered recovery", finished, m.error)
		}
		m.detach()
	})
	current, err := phone.List()
	if err != nil || len(current) != 1 || current[0].ID != a.tab().Focus.terminal().SID {
		t.Fatal("end affected another session", current, err)
	}
	refresh(a)
	desktop.Frame()
}
