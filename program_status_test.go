package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/agents"
	"retty/internal/rex"
)

func TestProgramStatusDisplaySupportsAnyProgram(t *testing.T) {
	m := &mobileApp{}
	for _, test := range []struct{ state, reason, label string }{
		{agents.Ready, "", "就绪"}, {agents.Running, "", "执行中"}, {agents.Waiting, "auth", "等待登录"},
		{agents.Waiting, "permission", "等待授权"}, {agents.Waiting, "question", "等待回答"},
		{agents.Waiting, "", ""}, {agents.Completed, "", ""}, {agents.Failed, "", "执行失败"},
	} {
		s := rex.SessionInfo{Program: "sh", Shell: "sh", Idle: true, Agent: rex.AgentState{ID: "terraform", Source: rex.ProgramStatusSource, State: test.state, Reason: test.reason}}
		if got := sessionAgentState(s); got.State != test.state {
			t.Fatal("generic status filtered as an unsupported agent")
		}
		if got := m.sessionStatus(s); got != test.label {
			t.Fatalf("%s/%s: %q", test.state, test.reason, got)
		}
		if name := sessionProgramName(s); name != "terraform" {
			t.Fatal("reported app identity lost", name)
		}
	}
	progress := 0
	s := rex.AgentState{Source: rex.ProgramStatusSource, State: agents.Running, Progress: &progress}
	if agentStateLabel(s) != "执行中 0%" {
		t.Fatal("zero progress treated as indeterminate")
	}
	progress = 65
	if agentStateLabel(s) != "执行中 65%" {
		t.Fatal("progress not displayed")
	}
	progress2 := 65
	copy := s
	copy.Progress = &progress2
	if !s.Equal(copy) {
		t.Fatal("JSON allocations changed status equality")
	}
	copy.Progress = nil
	if s.Equal(copy) {
		t.Fatal("indeterminate progress equals a percentage")
	}
}

func TestProgramStatusDesktopNoticesRespectFocusAndClear(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	p := a.tab().Focus
	var notices []mygo.NotificationOptions
	closed := 0
	a.agentNotify = func(opts mygo.NotificationOptions, click func()) func() {
		notices = append(notices, opts)
		return func() { closed++ }
	}
	in := rex.SessionInfo{ID: p.terminal().SID, Program: "sh", Shell: "sh", Idle: true, Agent: rex.AgentState{ID: "terraform", Source: rex.ProgramStatusSource, SessionID: "osc7501:", State: agents.Running, Updated: time.Now()}}
	apply := func() { a.apply(map[string]rex.SessionInfo{p.terminal().SID: in}) }
	apply()
	in.Agent.State, in.Agent.WaitRevision, in.Agent.Reason = agents.Waiting, 1, "permission"
	apply()
	if len(notices) != 0 {
		t.Fatal("viewed pane generated a protocol notice")
	}
	a.newTab("/tmp")
	tt.Frame()
	in.Agent.State = agents.Running
	apply()
	in.Agent.State, in.Agent.WaitRevision, in.Agent.Message = agents.Waiting, 2, "Apply these changes?"
	apply()
	apply()
	if len(notices) != 1 || !strings.Contains(notices[0].Title, "terraform · 等待授权") || notices[0].Body != "Apply these changes?" {
		t.Fatal("generic wait missing or duplicated", notices)
	}
	in.Agent.State, in.Agent.CompletionRevision, in.Agent.Message = agents.Completed, 3, "Deployment complete"
	apply()
	apply()
	if len(notices) != 2 || notices[1].Body != "Deployment complete" {
		t.Fatal("fast wait-to-completion lost or duplicated", notices)
	}
	if paneAgentState(p).State != agents.Completed {
		t.Fatal("completion disappeared on the shell prompt")
	}
	in.Agent = rex.AgentState{}
	apply()
	if closed != 2 || len(a.agentNotices) != 0 {
		t.Fatal("clear retained a protocol notice", closed)
	}
}

func TestProgramStatusMobileBackgroundAndReconnectNotices(t *testing.T) {
	m := &mobileApp{hello: rex.Hello{Version: 6, Host: rex.HostInfo{ID: "fixture"}}, background: true}
	var notices []mobileAgentNotice
	m.agentNotify = func(n mobileAgentNotice) { notices = append(notices, n) }
	in := rex.SessionInfo{ID: "pane", Program: "sh", Idle: true, Agent: rex.AgentState{ID: "brew", Source: rex.ProgramStatusSource, SessionID: "osc7501:", State: agents.Running, Updated: time.Now()}}
	update := func(initial bool) { m.updateSessions([]rex.SessionInfo{in}, initial) }
	update(true)
	in.Agent.State, in.Agent.Reason, in.Agent.WaitRevision, in.Agent.Message = agents.Waiting, "auth", 1, "Sign in to continue"
	update(false)
	update(false)
	if len(notices) != 1 || notices[0].Body != "Sign in to continue" || !strings.Contains(notices[0].Title, "等待登录") {
		t.Fatal("background protocol wait not delivered", notices)
	}
	in.Agent.State, in.Agent.CompletionRevision, in.Agent.Message = agents.Completed, 2, "Updated 12 packages"
	update(false)
	update(true)
	update(false)
	if len(notices) != 2 || notices[1].Body != "Updated 12 packages" || notices[1].Session != "pane" {
		t.Fatal("completion missing or replayed after reconnect", notices)
	}
	in.Agent.CompletionRevision = 3
	m.background = false
	update(false)
	if len(notices) != 2 {
		t.Fatal("foreground emitted protocol notice")
	}
	if m.rememberNotice(m.taskNoticeID(in)) {
		t.Fatal("foreground result not acknowledged for APNs")
	}
}

func TestProgramStatusDoesNotInferCompletionFromShellReturn(t *testing.T) {
	a, tt := newTestApp(t)
	p := a.tab().Focus
	a.newTab("/tmp")
	tt.Frame()
	a.agentNotify = func(mygo.NotificationOptions, func()) func() {
		t.Fatal("shell return guessed a completion")
		return nil
	}
	in := rex.SessionInfo{ID: p.terminal().SID, Program: "cargo", Idle: false, LastInput: time.Now().Add(-time.Minute), Agent: rex.AgentState{ID: "cargo", Source: rex.ProgramStatusSource, State: agents.Running}}
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: in})
	in.Program, in.Idle, in.Agent = "zsh", true, rex.AgentState{}
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: in})
}
