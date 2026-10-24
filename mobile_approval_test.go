package main

import (
	"context"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"net"
	"retty/internal/agents"
	"retty/internal/approval"
	"retty/internal/rex"
	"retty/internal/terminal"
	"strings"
	"sync"
	"testing"
	"time"
)

type structuredApprovalFixture struct {
	m        *mobileApp
	tt       *ui.Tester
	peer     net.Conn
	dialogs  chan mygo.MessageOptions
	answers  chan int
	posts    chan func()
	sent     chan string
	mu       sync.Mutex
	requests []approval.Request
}

func newStructuredApprovalFixture(t *testing.T) *structuredApprovalFixture {
	t.Helper()
	return newStructuredApprovalFixtureState(t, rex.AgentState{ID: "codex", SessionID: "thread", State: agents.Waiting, Reason: "permission"})
}
func newStructuredApprovalFixtureState(t *testing.T, state rex.AgentState) *structuredApprovalFixture {
	t.Helper()
	registerFonts()
	control, cp := net.Pipe()
	client := rex.NewClient(control, nil)
	conn, peer := net.Pipe()
	term, err := terminal.New(terminal.Options{Conn: conn, FixedCols: 100, FixedRows: 30, ReflowView: true, Font: terminal.Font{Family: termFont.Family, Size: 13}})
	if err != nil {
		t.Fatal(err)
	}
	term.Resize(100, 30)
	f := &structuredApprovalFixture{peer: peer, dialogs: make(chan mygo.MessageOptions, 8), answers: make(chan int, 8), posts: make(chan func(), 16), sent: make(chan string, 8)}
	done := make(chan struct{})
	f.requests = []approval.Request{{ID: "producer/request", SID: "owned", Agent: "codex", ThreadID: "thread", Title: "执行命令", Detail: "echo structured-data", Expires: time.Now().Add(time.Hour), Options: []approval.Option{{ID: "accept", Label: "允许本次"}, {ID: "decline", Label: "拒绝"}}}}
	f.m = &mobileApp{client: client, term: term, selected: rex.SessionInfo{ID: "owned", Program: "codex", Agent: state}}
	f.m.approval = &mobileApproval{list: func(context.Context) ([]approval.Request, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]approval.Request(nil), f.requests...), nil
	}, answer: func(_ context.Context, r approval.Request, choice string) error {
		f.sent <- r.ID + ":" + choice
		return nil
	}, show: func(opts mygo.MessageOptions) (mygo.MessageResult, error) {
		f.dialogs <- opts
		select {
		case i := <-f.answers:
			return mygo.MessageResult{Button: i}, nil
		case <-done:
			return mygo.MessageResult{Button: opts.CancelButton}, nil
		}
	}, post: func(fn func()) {
		select {
		case f.posts <- fn:
		case <-done:
		}
	}}
	// There is deliberately no parseable menu on the terminal screen.
	term.Feed([]byte("No permission menu exists here.\r\n"))
	f.tt = ui.NewTester(f.m.view, 390, 750)
	f.tt.Frame()
	t.Cleanup(func() { close(done); term.Close(); peer.Close(); client.Close(); cp.Close() })
	return f
}
func (f *structuredApprovalFixture) dispatch() {
	for {
		select {
		case fn := <-f.posts:
			fn()
		default:
			return
		}
	}
}
func (f *structuredApprovalFixture) dialog(t *testing.T) mygo.MessageOptions {
	t.Helper()
	waitFor(t, f.tt, "structured native dialog", func() bool { f.dispatch(); return len(f.dialogs) > 0 })
	return <-f.dialogs
}
func (f *structuredApprovalFixture) answer(t *testing.T, index int) {
	t.Helper()
	f.answers <- index
	waitFor(t, f.tt, "native reply", func() bool { f.dispatch(); return !f.m.approval.showing && !f.m.approval.sending })
}
func (f *structuredApprovalFixture) noInput(t *testing.T) {
	t.Helper()
	f.peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if n, _ := f.peer.Read(make([]byte, 256)); n > 0 {
		t.Fatal("approval wrote terminal bytes")
	}
	f.peer.SetReadDeadline(time.Time{})
}
func TestMobileStructuredApprovalNoTerminalParsingOrInput(t *testing.T) {
	f := newStructuredApprovalFixture(t)
	opts := f.dialog(t)
	if opts.Style != mygo.MessageActionSheet || len(opts.Buttons) != 3 || opts.Buttons[1] != "拒绝" || !strings.Contains(opts.Message, "echo structured-data") {
		t.Fatal(opts)
	}
	f.answer(t, opts.CancelButton)
	select {
	case <-f.sent:
		t.Fatal("cancel made a decision")
	default:
	}
	f.noInput(t)
	if err := f.tt.Click("处理授权"); err != nil {
		t.Fatal(err)
	}
	f.dialog(t)
	f.answer(t, 1)
	select {
	case got := <-f.sent:
		if got != "producer/request:decline" {
			t.Fatal(got)
		}
	default:
		t.Fatal("no structured answer")
	}
	f.noInput(t)
}
func TestMobileStructuredApprovalRejectsStaleReplies(t *testing.T) {
	for _, scenario := range []string{"background", "reconnect", "session", "thread", "generation", "resolved", "left page"} {
		t.Run(scenario, func(t *testing.T) {
			f := newStructuredApprovalFixture(t)
			f.dialog(t)
			switch scenario {
			case "background":
				f.m.background = true
			case "reconnect":
				f.m.reconnecting = true
			case "session":
				f.m.selected.ID = "another"
			case "thread":
				f.m.selected.Agent.SessionID = "new"
			case "generation":
				f.m.generation++
			case "resolved":
				f.m.approval.requests = nil
			case "left page":
				f.m.navigation.Back()
			}
			f.answer(t, 0)
			select {
			case got := <-f.sent:
				t.Fatal("stale reply sent", got)
			default:
			}
			f.noInput(t)
		})
	}
}

func TestMobileStructuredApprovalWithOSCOrMissingHooks(t *testing.T) {
	for _, state := range []rex.AgentState{{ID: "codex", SessionID: "osc7501:", State: agents.Waiting, Source: rex.ProgramStatusSource}, {}} {
		t.Run(state.Source, func(t *testing.T) {
			f := newStructuredApprovalFixtureState(t, state)
			if approvalThread(f.m.selected) != "" {
				t.Fatal("program identity used as conversation")
			}
			opts := f.dialog(t)
			if !strings.Contains(opts.Message, "structured-data") {
				t.Fatal(opts)
			}
			f.answer(t, 1)
			select {
			case got := <-f.sent:
				if got != "producer/request:decline" {
					t.Fatal(got)
				}
			default:
				t.Fatal("structured answer missing")
			}
			f.noInput(t)
		})
	}
}
