//go:build darwin || linux

package rex

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/agents"
)

func waitProgramStatus(t *testing.T, s *session, state string) SessionInfo {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		in := s.info()
		if in.Agent.State == state {
			return in
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not reach %q: %+v", state, s.info().Agent)
	return SessionInfo{}
}

func TestProgramStatusPTYQueryStateAndViewerReconnect(t *testing.T) {
	t.Setenv("RETTY_DIR", t.TempDir())
	dir := t.TempDir()
	// No viewer is attached when the program asks for support. Canonical
	// processing and echo are disabled so dd receives exactly the fixed reply.
	script := fmt.Sprintf(`stty -echo -icanon min 1 time 0
printf '\033]7501;?\033\\'
dd bs=1 count=%d 2>/dev/null > reply
printf '\033]7501;state=working:app=fixture:progress=0\007'
IFS= read -r command
printf '\033]7501;state=blocked:app=fixture:kind=auth\033\\'
IFS= read -r command
printf '\033]7501;state=done:app=fixture:msg=RmluaXNoZWQ=\033\\\033]133;A\007'
IFS= read -r command
printf '\033]7501;state=clear\033\\'
IFS= read -r command
`, len(programStatusReply))
	s, err := newSession("osc-fixture", CreateOptions{Dir: dir, Command: []string{"/bin/sh", "-c", script}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.kill)
	in := waitProgramStatus(t, s, agents.Running)
	if !in.LastInput.IsZero() || in.Attached != 0 || in.Agent.Source != ProgramStatusSource || in.Agent.Progress == nil || *in.Agent.Progress != 0 {
		t.Fatal("query counted as editing, or status lost", in.Agent)
	}
	if reply, err := os.ReadFile(filepath.Join(dir, "reply")); err != nil || string(reply) != programStatusReply {
		t.Fatalf("capability reply: %q / %v", reply, err)
	}
	s.input([]byte("next\n"))
	waiting := waitProgramStatus(t, s, agents.Waiting)
	if waiting.Agent.Reason != "auth" || len(waiting.ProgramStatuses) != 1 {
		t.Fatal("blocked metadata lost")
	}
	if text := s.vt.Text(); strings.Contains(text, "7501") || strings.Contains(text, "state=") {
		t.Fatal("status escape sequence leaked into the terminal grid", text)
	}
	// Reattach twice. No query/report is replayed into program input, and
	// reconnect preserves the pending notice's identity.
	for i := 0; i < 2; i++ {
		server, viewer := net.Pipe()
		go s.attach(server, 80, 24)
		viewer.SetReadDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 32<<10)
		if _, err := viewer.Read(buf); err != nil {
			t.Fatal(err)
		}
		if next := s.info(); !next.Agent.Equal(waiting.Agent) || AgentNoticeTransition(waiting, next) {
			t.Fatal("reconnect changed pending status")
		}
		viewer.Close()
	}
	s.input([]byte("next\n"))
	done := waitProgramStatus(t, s, agents.Completed)
	if done.Agent.Message != "Finished" || len(done.ProgramStatuses) != 1 || !AgentNoticeTransition(waiting, done) {
		t.Fatal("completion did not survive next prompt", done.Agent)
	}
	// A newer hook for this same task cannot duplicate the protocol result.
	if _, _, err := s.reportAgent(s.agentToken, &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{SessionID: "hook-thread", Event: "Stop"}}); err != nil {
		t.Fatal(err)
	}
	if !s.info().Agent.Equal(done.Agent) {
		t.Fatal("hook displaced the authoritative protocol record")
	}
	s.input([]byte("next\n"))
	waitProgramStatus(t, s, "")
	if s.info().Agent.State != "" {
		t.Fatal("clear resurrected stale hook completion")
	}
	// Starting another integrated program can use hooks if it emits no OSC.
	if _, _, err := s.reportAgent(s.agentToken, &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{SessionID: "new-thread", Event: "UserPromptSubmit"}}); err != nil {
		t.Fatal(err)
	}
	next := waitProgramStatus(t, s, agents.Running)
	if next.Agent.Source != "" || next.Agent.ID != "claude" {
		t.Fatal("hook fallback failed", next.Agent)
	}
}

func TestProgramStatusPTYExitLifetimes(t *testing.T) {
	t.Setenv("RETTY_DIR", t.TempDir())
	for _, state := range []string{"working", "blocked", "done", "error", "idle"} {
		t.Run(state, func(t *testing.T) {
			body := statusOSC("state=" + state + ":app=fixture")
			s, err := newSession("osc-exit-"+state, CreateOptions{Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "printf '%s' '" + body + "'"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.kill)
			select {
			case <-s.done:
			case <-time.After(4 * time.Second):
				t.Fatal("fixture did not exit")
			}
			in := s.info()
			if !in.Exited {
				t.Fatal("exit missing")
			}
			if strings.Contains("working blocked", state) {
				if len(in.ProgramStatuses) != 0 || in.Agent.State != "" {
					t.Fatal("transient record survived exit")
				}
			} else if len(in.ProgramStatuses) != 1 || in.Agent.Source != ProgramStatusSource {
				t.Fatal("persistent record lost on exit")
			}
		})
	}
}

func TestProgramStatusRejectedHookCannotClearRecords(t *testing.T) {
	s := &session{agentToken: "fixture"}
	now := time.Now()
	for i, thread := range []string{"retired", "current"} {
		e := &AgentEvent{Agent: "claude", At: now.Add(time.Duration(i) * time.Millisecond), Input: agents.HookInput{SessionID: thread, Event: "UserPromptSubmit"}}
		if _, _, err := s.reportAgent("fixture", e); err != nil {
			t.Fatal(err)
		}
	}
	s.programStatus.feed([]byte(statusOSC("state=done:app=fixture")), now.Add(2*time.Millisecond))
	e := &AgentEvent{Agent: "claude", At: now.Add(3 * time.Millisecond), Input: agents.HookInput{SessionID: "retired", Event: "UserPromptSubmit"}}
	if _, _, err := s.reportAgent("fixture", e); err != nil {
		t.Fatal(err)
	}
	if len(s.programStatus.records) != 1 || s.agent.state.SessionID != "current" {
		t.Fatal("rejected old hook cleared a new protocol result")
	}
}

func TestCodexLifecycleSummarySurvivesProgramStatus(t *testing.T) {
	t.Setenv("RETTY_DIR", t.TempDir())
	s, err := newSession("codex-summary", CreateOptions{Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.kill)
	now := time.Now()
	report := func(event, turn, summary string) {
		t.Helper()
		now = now.Add(time.Millisecond)
		_, accepted, err := s.reportAgent(s.agentToken, &AgentEvent{Agent: "codex", At: now, Input: agents.HookInput{
			Event: event, SessionID: "root-thread", TurnID: turn, Source: agents.CodexLifecycleSource, Summary: summary,
		}})
		if err != nil || !accepted {
			t.Fatal("lifecycle event rejected", accepted, err)
		}
	}
	status := func(state string) {
		now = now.Add(time.Millisecond)
		s.mu.Lock()
		s.programStatus.feed([]byte(statusOSC("state="+state+":app=codex")), now)
		s.mu.Unlock()
	}
	report("UserPromptSubmit", "first", "")
	status("working")
	status("done")
	if in := s.info(); agents.Finished(in.Agent.State) {
		t.Fatal("OSC completion raced ahead of the structured final answer", in.Agent)
	}
	report("Stop", "first", "已修复通知摘要。测试通过。")
	completed := s.info()
	if body := AgentNoticeBody("", completed); body != "已修复通知摘要。" {
		t.Fatal("OSC status discarded the final answer", body, completed.Agent)
	}
	if completed.Agent.SessionID != "root-thread" || completed.Agent.HookEvent != "Stop" {
		t.Fatal("structured completion identity was lost", completed.Agent)
	}
	// Text-only OSC updates and idle must not create a second completion.
	for _, state := range []string{"done", "idle"} {
		status(state)
		if next := s.info(); AgentNoticeID("host", next) != AgentNoticeID("host", completed) || AgentNoticeTransition(completed, next) {
			t.Fatal("OSC update repeated the completion", next.Agent)
		}
	}
	// The terminal stream can precede the asynchronous lifecycle report.
	// Without a structured new turn it must not announce the old reply
	// again, even when OSC has already reported another completion.
	status("working")
	status("done")
	if in := s.info(); AgentNoticeTransition(completed, in) || AgentNoticeID("host", in) != AgentNoticeID("host", completed) {
		t.Fatal("new OSC output repeated an older completion", in.Agent)
	}
	report("UserPromptSubmit", "second", "")
	if in := s.info(); in.Agent.Summary != "" || in.Agent.State != agents.Running {
		t.Fatal("new structured turn retained an older reply", in.Agent)
	}
	status("working")
	report("Stop", "second", "第二轮已完成。")
	status("done")
	next := s.info()
	if AgentNoticeBody("", next) != "第二轮已完成。" || AgentNoticeID("host", next) == AgentNoticeID("host", completed) {
		t.Fatal("new turn did not get its own summary and notice", next.Agent)
	}
	// The two transports can be read in either order, including a delayed
	// working report arriving after the structured completion.
	status("working")
	status("done")
	if delayed := s.info(); !delayed.Agent.Equal(next.Agent) {
		t.Fatal("delayed PTY reports displaced a confirmed final answer", delayed.Agent)
	}
}

func TestCodexLifecycleProgramStatusScope(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, app, id, source string
		reset                 bool
		structured            bool
	}{
		{name: "connected root", app: "codex", source: agents.CodexLifecycleSource, structured: true},
		{name: "native only", app: "codex"},
		{name: "other program", app: "fixture", source: agents.CodexLifecycleSource},
		{name: "child status", app: "codex", id: "child", source: agents.CodexLifecycleSource},
		{name: "new CLI without observer", app: "codex", source: agents.CodexLifecycleSource, reset: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tracker agentTracker
			apply := func(event, source string, at time.Time) {
				tracker.apply(AgentEvent{Agent: "codex", At: at, Input: agents.HookInput{
					Event: event, SessionID: "root", TurnID: "turn", Source: source, Summary: "最终回复。",
				}})
			}
			apply("UserPromptSubmit", tc.source, now)
			var statuses programStatusTracker
			statuses.apply(ProgramStatus{State: "working", App: tc.app, ID: tc.id}, now.Add(time.Millisecond))
			apply("Stop", tc.source, now.Add(2*time.Millisecond))
			if tc.reset {
				apply("SessionStart", "", now.Add(3*time.Millisecond))
			}
			statuses.apply(ProgramStatus{State: "done", App: tc.app, ID: tc.id}, now.Add(4*time.Millisecond))
			got := statuses.agentStateWithLifecycle(&tracker)
			if tc.structured {
				if got.Summary != "最终回复。" || got.Source == ProgramStatusSource {
					t.Fatal("connected root lost its structured completion", got)
				}
			} else if !got.Equal(statuses.agentState("")) {
				t.Fatal("unrelated OSC status changed", got)
			}
		})
	}
}
