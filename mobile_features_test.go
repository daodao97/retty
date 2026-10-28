package main

import (
	"github.com/egoist/mygo/ui"
	"io"
	"net"
	"retty/internal/agents"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/terminal"
	"strings"
	"testing"
	"time"
)

type testMobileTransport struct{ net.Conn }

func (s testMobileTransport) ReadScreen(p []byte) (int, int, int, error) {
	n, e := s.Read(p)
	return n, 0, 0, e
}
func (s testMobileTransport) Resize(int, int) error { return nil }
func (s testMobileTransport) HasScreenSize() bool   { return false }

func TestMobileStreamSurvivesLossWithoutReplayingInput(t *testing.T) {
	first, peer := net.Pipe()
	failed := make(chan struct{}, 2)
	stream := newSessionViewStream(testMobileTransport{first}, func() { failed <- struct{}{} })
	defer stream.Close()
	stream.geometry(111, 58)
	output := make(chan string, 1)
	go func() {
		p := make([]byte, 4096)
		n, c, r, e := stream.ReadScreen(p)
		if e != nil || c != 90 || r != 40 {
			output <- "incorrect geometry"
			return
		}
		output <- string(p[:n])
	}()
	peer.Close()
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("loss not detected")
	}
	if n, e := stream.Write([]byte("dangerous command\r")); n != 18 || e != nil {
		t.Fatal(n, e)
	}
	next, nextPeer := net.Pipe()
	defer nextPeer.Close()
	stream.geometry(90, 40)
	stream.replace(testMobileTransport{next})
	go nextPeer.Write([]byte("restored"))
	select {
	case got := <-output:
		if !strings.HasSuffix(got, "restored") {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("reader terminated during disconnection")
	}
	nextPeer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if n, _ := nextPeer.Read(make([]byte, 32)); n != 0 {
		t.Fatal("paused input was replayed")
	}
	nextPeer.SetReadDeadline(time.Time{})
	go stream.Write([]byte("safe"))
	buf := make([]byte, 4)
	if _, e := io.ReadFull(nextPeer, buf); e != nil || string(buf) != "safe" {
		t.Fatal("input worker did not survive", e)
	}
}

func TestMobileReconnectRetainsPageAndCancelReleasesTerminal(t *testing.T) {
	registerFonts()
	term, e := terminal.New(terminal.Options{Conn: nopConn{}, Font: terminal.Font{Family: termFont.Family, Size: 13}, Theme: lightTerm})
	if e != nil {
		t.Fatal(e)
	}
	m := &mobileApp{term: term, selected: rex.SessionInfo{ID: "session"}, sessions: []rex.SessionInfo{{ID: "session"}}, link: "fixture"}
	tt := ui.NewTester(m.view, 390, 750)
	m.pauseConnection()
	tt.Frame()
	if m.term != term || m.selected.ID != "session" || m.navigation.Path() != "/sessions/terminal" {
		t.Fatal("disconnect lost terminal/page")
	}
	if _, ok := tt.Find("正在重连"); !ok {
		t.Fatal("missing reconnection indicator")
	}
	tt.Click("连接恢复操作")
	tt.Frame()
	tt.Click("取消重连")
	tt.Frame()
	if m.term != nil || m.reconnecting || m.retryTimer != nil || m.navigation.Path() != "/connect" {
		t.Fatal("cancel retained connection resources")
	}
	for i := 0; i < 20; i++ {
		if d := remote.RetryDelay(i); d < time.Second || d > 5*time.Second {
			t.Fatal(d)
		}
	}
}

func TestMobileSessionNamesAreDeviceScopedAndPinsComeFirst(t *testing.T) {
	m := &mobileApp{}
	m.hello.Host.ID = "first"
	m.setSessionPreference("pinned", "我的任务", true)
	waiting := rex.SessionInfo{ID: "waiting", Program: "claude", Agent: rex.AgentState{ID: "claude", State: agents.Waiting}}
	m.sessions = []rex.SessionInfo{{ID: "ordinary"}, {ID: "pinned", Title: "original"}, waiting}
	got := m.orderedSessions()
	if got[0].ID != "pinned" || got[1].ID != "ordinary" || m.sessionTitle(got[0]) != "我的任务" {
		t.Fatal(got)
	}
	if m.sessions[0].ID != "ordinary" {
		t.Fatal("sorting changed source list")
	}
	m.hello.Host.ID = "second"
	if m.preference("pinned").Pinned || m.sessionTitle(got[0]) != "original" {
		t.Fatal("preferences leaked to another desktop")
	}
	m.hello.Host.ID = "first"
	m.setSessionPreference("pinned", "", false)
	if m.sessionTitle(got[0]) != "original" {
		t.Fatal("clearing name did not restore source title")
	}
	m.setSessionPreference("pinned", strings.Repeat("字", 90)+"\n", false)
	if len([]rune(m.preference("pinned").Name)) != 80 {
		t.Fatal("name not bounded")
	}
}

func TestMobileSessionEditorFitsPhone(t *testing.T) {
	registerFonts()
	for _, size := range [][2]int{{320, 568}, {375, 620}, {390, 750}, {390, 360}, {750, 310}} {
		m := &mobileApp{client: &rex.Client{}, sessions: []rex.SessionInfo{{ID: "test", Title: "shell"}}}
		m.hello.Host.ID = "desktop"
		m.editSession(m.sessions[0])
		tt := ui.NewTester(m.view, size[0], size[1])
		if tt.Focused("会话名称") {
			t.Fatal("opening settings should not focus the name and summon a keyboard")
		}
		panel, ok := tt.Find("会话编辑面板")
		if !ok || panel.X < 0 || panel.Y < 0 || panel.X+panel.W > float32(size[0]) || panel.Y+panel.H > float32(size[1]) {
			t.Fatalf("editor is clipped at %v: %v", size, panel)
		}
		for _, label := range []string{"取消", "会话名称", "置顶会话", "结束会话", "保存"} {
			r, ok := tt.Find(label)
			if !ok || r.X < panel.X || r.Y < panel.Y || r.X+r.W > panel.X+panel.W+.01 || r.Y+r.H > panel.Y+panel.H+.01 {
				t.Fatalf("editor control %s is clipped at %v: %v", label, size, r)
			}
			if label != "会话名称" && (r.W < 44 || r.H < 44) {
				t.Fatalf("action %s has a small touch target: %v", label, r)
			}
		}
		if size == [2]int{390, 750} {
			saveSettingsImage(t, tt, "session-editor-light")
			tt.SetDark(true)
			saveSettingsImage(t, tt, "session-editor-dark")
		}
		tt.Click("会话名称")
		tt.Type("移动开发")
		tt.Click("置顶会话")
		if size == [2]int{390, 750} {
			saveSettingsImage(t, tt, "session-editor-edited-dark")
			tt.SetSize(390, 360)
			saveSettingsImage(t, tt, "session-editor-keyboard-viewport")
			tt.SetSize(390, 750)
		}
		tt.Click("保存")
		if m.editingOpen || m.preference("test").Name != "移动开发" || !m.preference("test").Pinned {
			t.Fatal("editor did not apply name/pin", m.preference("test"))
		}
		if size == [2]int{390, 750} {
			saveSettingsImage(t, tt, "session-editor-saved")
		}
		m.editSession(m.sessions[0])
		tt.Frame()
		tt.Click("置顶会话")
		tt.Click("取消")
		if m.editingOpen || m.preference("test").Name != "移动开发" || !m.preference("test").Pinned {
			t.Fatal("cancel changed saved preferences", m.preference("test"))
		}
	}
}

func TestMobileAgentNotificationsDeduplicateAndCoverCommonAgents(t *testing.T) {
	for _, id := range agents.Integrated {
		t.Run(id, func(t *testing.T) {
			m := &mobileApp{background: true}
			m.hello.Version = 5
			m.hello.Host.ID = "desktop"
			count := 0
			m.agentNotify = func(n mobileAgentNotice) {
				count++
				if n.Session != "test" || !strings.Contains(n.Title, programOf(id).Name) {
					t.Fatal(n)
				}
			}
			s := rex.SessionInfo{ID: "test", Program: id, Agent: rex.AgentState{ID: id, SessionID: "thread", State: agents.Running}}
			m.updateSessions([]rex.SessionInfo{s}, true)
			s.Agent.State = agents.Waiting
			s.Agent.Reason = "permission"
			s.Agent.WaitRevision = 1
			m.updateSessions([]rex.SessionInfo{s}, false)
			m.updateSessions([]rex.SessionInfo{s}, false)
			if count != 1 || m.sessionStatus(s) != "等待授权" {
				t.Fatal("waiting duplicate/missing", count)
			}
			s.Agent.State = agents.Completed
			s.Agent.CompletionRevision = 1
			m.updateSessions([]rex.SessionInfo{s}, false)
			m.updateSessions([]rex.SessionInfo{s}, false)
			if count != 2 {
				t.Fatal("completion duplicate/missing", count)
			}
			s.Agent.CompletionRevision = 2
			m.updateSessions([]rex.SessionInfo{s}, false)
			if count != 3 {
				t.Fatal("completion during gap lost", count)
			}
			s.Program = "sh"
			s.Agent.State = agents.Waiting
			s.Agent.WaitRevision = 2
			m.updateSessions([]rex.SessionInfo{s}, false)
			if count != 3 {
				t.Fatal("stale hook on shell notified")
			}
			s.Program = id
			s.Agent.State = agents.Completed
			s.Agent.CompletionRevision = 3
			m.updateSessions([]rex.SessionInfo{s}, true)
			if count != 3 {
				t.Fatal("historical completion notified")
			}
		})
	}
}

func TestMobileSnapshotCanReplaceExistingTerminal(t *testing.T) {
	registerFonts()
	makeTerm := func() *terminal.Terminal {
		term, e := terminal.New(terminal.Options{Conn: nopConn{}, FixedCols: 40, FixedRows: 6, Font: terminal.Font{Family: termFont.Family, Size: 13}, Theme: lightTerm})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { term.Close() })
		return term
	}
	source, phone := makeTerm(), makeTerm()
	for i := 0; i < 10; i++ {
		source.Feed([]byte("line of output\r\n"))
	}
	snapshot := source.Snapshot()
	phone.Feed(snapshot)
	before := phone.Text()
	phone.Feed([]byte("\x18\x1b[?6l\x1b[r\x1b[H\x1b[2J\x1b[3J"))
	phone.Feed(snapshot)
	if phone.Text() != before {
		t.Fatal("reattach snapshot duplicated prior scrollback")
	}
}

func TestMobilePreferenceLoadPreservesEditsAndDeletions(t *testing.T) {
	m := &mobileApp{}
	m.hello.Host.ID = "desktop"
	saved := map[string]mobileSessionPreference{
		m.preferenceKey("edited"): {Name: "old"}, m.preferenceKey("deleted"): {Name: "old", Pinned: true},
		"other-desktop\x00session": {Name: "preserved"},
	}
	epoch := m.preferenceEpoch
	m.setSessionPreference("edited", "new", true)
	m.setSessionPreference("deleted", "", false)
	m.applyLoadedSessionPreferences(saved, epoch)
	if m.preference("edited").Name != "new" || m.preference("deleted").Name != "" || m.preference("deleted").Pinned || m.sessionPreferences["other-desktop\x00session"].Name != "preserved" {
		t.Fatal(m.sessionPreferences)
	}
}

func TestMobileNotificationClickOpensTargetAndLegacyCompletionNotifiesOnce(t *testing.T) {
	registerFonts()
	conn, peer := net.Pipe()
	defer peer.Close()
	client := rex.NewClient(conn, nil)
	defer client.Close()
	m := &mobileApp{client: client, background: true}
	defer m.detach()
	m.hello.Host.ID = "desktop"
	m.hello.Version = 4
	count := 0
	m.agentNotify = func(mobileAgentNotice) { count++ }
	s := rex.SessionInfo{ID: "target", Title: "Task", Program: "claude", Cols: 80, Rows: 24, Agent: rex.AgentState{ID: "claude", SessionID: "thread", State: agents.Running}}
	m.updateSessions([]rex.SessionInfo{s}, true)
	s.Agent.State = agents.Completed
	m.updateSessions([]rex.SessionInfo{s}, false)
	m.updateSessions([]rex.SessionInfo{s}, false)
	if count != 1 || m.sessions[0].Agent.CompletionRevision != 1 {
		t.Fatal("legacy completion repeated", count)
	}
	m.background = false
	m.openNotifiedSession("desktop", "target")
	if m.term == nil || m.selected.ID != "target" {
		t.Fatal("notification did not enter its session")
	}
	term := m.term
	m.background = false
	m.openNotifiedSession("desktop", "target")
	if m.term != term {
		t.Fatal("repeat click reset current terminal")
	}
	m.openNotifiedSession("desktop", "gone")
	if m.error == "" || m.term != term {
		t.Fatal("missing session discarded current terminal")
	}
}

func TestMobileSessionEditorDismissalDiscardsDraft(t *testing.T) {
	for _, action := range []string{"escape", "outside"} {
		t.Run(action, func(t *testing.T) {
			registerFonts()
			m := &mobileApp{client: &rex.Client{}, sessions: []rex.SessionInfo{{ID: "test", Title: "shell"}}}
			m.hello.Host.ID = "desktop"
			m.editSession(m.sessions[0])
			tt := ui.NewTester(m.view, 390, 750)
			tt.Click("会话名称")
			tt.Type("discard this draft")
			tt.Click("置顶会话")
			if action == "escape" {
				tt.Key(0, ui.KeyEscape)
			} else {
				tt.ClickAt(10, 10)
			}
			tt.Frame()
			if m.editingOpen || m.preference("test").Name != "" || m.preference("test").Pinned || tt.Focused("会话名称") {
				t.Fatal("dismissal saved preferences or left the editor focused")
			}
		})
	}
}
