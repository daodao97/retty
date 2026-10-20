//go:build darwin || linux

package rex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"retty/internal/agents"
)

func recoveryServer(t *testing.T) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rex-resume-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude-config"))
	s := &Server{sessions: map[string]*session{}, quit: make(chan struct{})}
	if err := s.loadRecovery(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, ss := range s.sessions {
			ss.kill()
		}
		os.RemoveAll(dir)
	})
	return s
}

func fakeResumeCLI(t *testing.T, agent string, fail bool) string {
	t.Helper()
	bin := filepath.Join(Dir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bin, agent)
	script := "#!/bin/sh\nprintf 'cwd=%s\\n' \"$PWD\"\nprintf 'arg=%s\\n' \"$@\"\nprintf 'config=%s\\n' \"${CODEX_HOME}${CLAUDE_CONFIG_DIR}\"\n"
	if fail {
		script += "exit 42\n"
	} else {
		script += "while IFS= read -r line; do :; done\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestResumeLaunchMetadata(t *testing.T) {
	for _, tc := range []struct {
		agent      string
		argv, want []string
		supported  bool
	}{
		{"codex", []string{"codex", "--remote", "unix:///old-private.sock", "-C", "/old", "-p", "work", "--model=o3", "resume", "old-id", "old prompt --model secret"}, []string{"-p", "work", "--model", "o3"}, true},
		{"codex", []string{"node", "/opt/bin/codex.js", "--no-daemon", "--full-auto", "hello"}, []string{"--no-daemon", "--full-auto"}, true},
		{"codex", []string{"codex", "resume", "exact", "-c", `model_provider="gateway"`}, []string{"-c", `model_provider="gateway"`}, true},
		{"codex", []string{"codex", "--config", `model_providers.gateway.experimental_bearer_token="fixture-secret"`}, nil, true},
		{"claude", []string{"claude", "--model", "sonnet", "--effort=high", "--resume", "old-id", "a prompt"}, []string{"--model", "sonnet", "--effort", "high"}, true},
		{"claude", []string{"claude", "--settings", "private settings", "--no-session-persistence"}, nil, false},
		{"codex", []string{"codex", "--ephemeral"}, nil, false},
		{"claude", []string{"claude", "--print", "private prompt"}, nil, false},
	} {
		got, supported := resumeOptions(tc.agent, tc.argv)
		if !slices.Equal(got, tc.want) || supported != tc.supported {
			t.Fatalf("%v: %v %v", tc.argv, got, supported)
		}
	}
	dir := t.TempDir()
	for _, agent := range []string{"codex", "claude"} {
		r := agentResume{Agent: agent, SessionID: "exact-conversation", Dir: dir, Options: []string{"--model", "chosen"}}
		cmd, err := r.command()
		if err != nil || !slices.Contains(cmd, r.SessionID) || slices.Contains(cmd, "--last") {
			t.Fatal(cmd, err)
		}
		if agent == "codex" && !slices.Equal(cmd[len(cmd)-2:], []string{"-C", dir}) {
			t.Fatal("wrong working directory", cmd)
		}
		r.SessionID = "--last"
		if _, err := r.command(); err == nil {
			t.Fatal("accepted an option as conversation ID")
		}
		r.SessionID, r.Options = "exact", []string{"arbitrary prompt"}
		if _, err := r.command(); err == nil {
			t.Fatal("accepted persisted prompt")
		}
		r.Options, r.Env = nil, []string{"API_KEY=secret"}
		if _, err := r.command(); err == nil {
			t.Fatal("accepted credentials")
		}
	}
}

func TestRecoveryProviderOverrideRejectsOtherConfig(t *testing.T) {
	r := agentResume{Agent: "codex", SessionID: "exact", Dir: t.TempDir(), Options: []string{"--config", `model_provider="gateway"`}}
	if _, err := r.command(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`model_provider=""`, `model_provider=123`, `model_provider="gateway"\nmodel="injected"`, `model_providers.gateway.env_key="SECRET"`} {
		r.Options[1] = value
		if _, err := r.command(); err == nil {
			t.Fatal("unsafe persisted config accepted")
		}
	}
}

func TestAgentLifecyclePreservesConfigurationHome(t *testing.T) {
	var tracker agentTracker
	home := t.TempDir()
	tracker.apply(AgentEvent{Agent: "codex", At: time.Now(), Input: agents.HookInput{SessionID: "exact", Event: "SessionStart", ConfigDir: &home}})
	if tracker.state.ConfigDir != home {
		t.Fatal("hook configuration home lost")
	}
	tracker.apply(AgentEvent{Agent: "codex", At: time.Now(), Input: agents.HookInput{SessionID: "exact", Event: "UserPromptSubmit"}})
	if tracker.state.ConfigDir != home {
		t.Fatal("later lifecycle event erased configuration identity")
	}
}

func TestAgentRecoveryPersistsLifecycleWithoutViewer(t *testing.T) {
	s := recoveryServer(t)
	t.Setenv("RESUME_FIXTURE_API_KEY", "never-save-this-secret")
	ss, err := newSession("pane", CreateOptions{Command: []string{"/bin/sh"}, Dir: Dir()})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[ss.id] = ss
	var config *string
	report := func(id, event, subagent string) {
		t.Helper()
		e := &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{Event: event, SessionID: id, CWD: Dir(), AgentID: subagent, ConfigDir: config}}
		if err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: e}); err != nil && subagent == "" {
			t.Fatal(err)
		}
	}
	report("first", "SessionStart", "")
	if !slices.Contains(s.recovery[ss.id].Env, "CLAUDE_CONFIG_DIR="+os.Getenv("CLAUDE_CONFIG_DIR")) {
		t.Fatal("legacy hook lost inherited configuration")
	}
	custom := filepath.Join(Dir(), "different-profile")
	config = &custom
	report("second", "SessionStart", "")
	report("first", "SessionEnd", "") // stale end must not forget the new /new
	report("second", "SessionEnd", "subagent")
	loaded := &Server{}
	if err := loaded.loadRecovery(Dir()); err != nil {
		t.Fatal(err)
	}
	if r := loaded.recovery[ss.id]; r.SessionID != "second" || r.Dir != Dir() {
		t.Fatal("wrong persisted conversation", r)
	}
	if !slices.Contains(loaded.recovery[ss.id].Env, "CLAUDE_CONFIG_DIR="+custom) {
		t.Fatal("hook lost actual Agent configuration directory")
	}
	st, _ := os.Stat(s.recoveryPath)
	if st.Mode().Perm() != 0o600 {
		t.Fatal("recovery file is not private", st.Mode())
	}
	data, _ := os.ReadFile(s.recoveryPath)
	if strings.Contains(string(data), "never-save-this-secret") {
		t.Fatal("persisted a credential")
	}
	if !strings.Contains(string(data), "CLAUDE_CONFIG_DIR=") {
		t.Fatal("lost Agent configuration directory")
	}
	report("second", "Stop", "") // completed response is still resumable
	if s.recovery[ss.id].Revision != ss.agent.state.CompletionRevision || s.recovery[ss.id].Revision == 0 {
		t.Fatal("completion event sequence was not persisted")
	}
	if s.recovery[ss.id].SessionID != "second" {
		t.Fatal("completion lost conversation")
	}
	report("second", "SessionEnd", "")
	if err := loaded.loadRecovery(Dir()); err != nil || len(loaded.recovery) != 0 {
		t.Fatal("explicit end resurrected conversation", err)
	}
}

func TestAgentRecoveryContinuesNoticeSequence(t *testing.T) {
	for _, floor := range []uint64{0, 9} {
		t.Run(fmt.Sprint(floor), func(t *testing.T) {
			s := recoveryServer(t)
			fakeResumeCLI(t, "claude", false)
			s.recovery["pane"] = agentResume{Agent: "claude", SessionID: "thread", Dir: Dir(), Revision: floor}
			r, err := s.restoreAgent(Request{SID: "pane", Cols: 80, Rows: 24})
			if err != nil || r.Session == nil {
				t.Fatal(r, err)
			}
			ss := s.sessions["pane"]
			before := ss.info()
			if before.Agent.CompletionRevision < floor || before.Agent.CompletionRevision == 0 {
				t.Fatal("restore lost sequence floor", before.Agent)
			}
			old := before
			old.Agent.State = agents.Completed
			for _, event := range []string{"UserPromptSubmit", "Stop"} {
				err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{SessionID: "thread", Event: event, CWD: Dir()}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			next := ss.info()
			if next.Agent.CompletionRevision <= before.Agent.CompletionRevision || AgentNoticeID("source", next) == AgentNoticeID("source", old) {
				t.Fatal("restored turn reused a read notification", next.Agent)
			}
		})
	}
}

func TestAgentRecoveryLeavesStoppedAgentAndForgetsMissingProcess(t *testing.T) {
	s := recoveryServer(t)
	path := filepath.Join(Dir(), "claude")
	if err := os.Symlink("/bin/cat", path); err != nil {
		t.Fatal(err)
	}
	ss, err := newSession("shell", CreateOptions{Command: []string{"/bin/sh"}, Dir: Dir()})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[ss.id] = ss
	ss.input([]byte("'" + path + "'\r"))
	deadline := time.Now().Add(3 * time.Second)
	for ss.info().Program != "claude" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if ss.info().Program != "claude" {
		t.Fatal("fixture did not become foreground Agent")
	}
	e := &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{Event: "SessionStart", SessionID: "exact", CWD: Dir()}}
	if err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: e}); err != nil {
		t.Fatal(err)
	}
	r := s.recovery[ss.id]
	if r.pid == 0 || r.pid == ss.p.cmd.Process.Pid {
		t.Fatal("did not bind to Agent child process", r.pid)
	}
	t.Cleanup(func() {
		if p := inspect(r.pid); slices.Contains(p.args, path) {
			syscall.Kill(r.pid, syscall.SIGKILL)
		}
	}) // owned child fixture only, checked again against PID reuse
	if err := syscall.Kill(r.pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	s.checkRecovery()
	if len(s.recovery) != 1 {
		t.Fatal("a suspended Agent was forgotten")
	}
	if err := syscall.Kill(r.pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// The owned Agent child ends without SessionEnd; its login shell stays alive.
	deadline = time.Now().Add(3 * time.Second)
	for !ss.info().Idle && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !ss.info().Idle {
		t.Fatal("fixture did not return to shell")
	}
	r.seen = time.Now().Add(-time.Minute)
	s.recovery[ss.id] = r
	s.checkRecovery()
	deadline = time.Now().Add(3 * time.Second)
	for len(s.recovery) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		s.checkRecovery()
	}
	if len(s.recovery) != 0 {
		t.Fatal("ended Agent was left resumable")
	}
}

func TestAgentRecoveryFreshDaemonAndConcurrentReattach(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			s := recoveryServer(t)
			fakeResumeCLI(t, agent, false)
			dir := filepath.Join(Dir(), "original project")
			os.Mkdir(dir, 0o700)
			r := agentResume{Agent: agent, SessionID: "exact-thread", Dir: dir, Options: []string{"--model", "chosen"}, Env: []string{"CODEX_HOME=" + filepath.Join(Dir(), "profile")}}
			s.recovery["original-pane"] = r
			if err := s.saveRecovery(); err != nil {
				t.Fatal(err)
			}
			// An empty session map loaded from disk models a new daemon after reboot.
			if err := s.loadRecovery(Dir()); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan RestoreResult, 8)
			for range 8 {
				wg.Go(func() {
					result, err := s.restoreAgent(Request{SID: "original-pane", Cols: 93, Rows: 31})
					if err != nil {
						t.Error(err)
					}
					results <- result
				})
			}
			wg.Wait()
			close(results)
			pid := 0
			for result := range results {
				if result.Session == nil || result.Error != "" {
					t.Fatal(result)
				}
				info := *result.Session
				if pid == 0 {
					pid = info.PID
				}
				if info.ID != "original-pane" || info.PID != pid || info.Cols != 93 || info.Rows != 31 || !info.Resumed {
					t.Fatal("duplicate/wrong PTY", info)
				}
			}
			if len(s.order) != 1 || len(s.sessions) != 1 {
				t.Fatal("restore was not idempotent")
			}
			ss := s.sessions["original-pane"]
			deadline := time.Now().Add(5 * time.Second)
			var text string
			for time.Now().Before(deadline) {
				ss.mu.Lock()
				text = ss.vt.Text()
				ss.mu.Unlock()
				if strings.Contains(text, "arg=chosen") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			canonicalDir, _ := filepath.EvalSymlinks(dir)
			if !strings.Contains(text, "cwd="+canonicalDir) || !strings.Contains(text, "arg=exact-thread") || !strings.Contains(text, "arg=chosen") || strings.Contains(text, "--last") {
				info := ss.info()
				p := inspect(ss.p.cmd.Process.Pid)
				var launcher []string
				for _, kv := range ss.p.cmd.Env {
					if strings.HasPrefix(kv, "RETTY_CODEX_EXE=") || strings.HasPrefix(kv, "RETTY_CODEX_BRIDGE=") {
						launcher = append(launcher, kv)
					}
				}
				t.Fatalf("wrong resume launch %q (program=%s exited=%t code=%d bytes=%d process=%s argv=%v launch=%v)", text, info.Program, info.Exited, info.ExitCode, info.Output, p.name, p.args, launcher)
			}
			if err := s.reportAgent(Request{SID: ss.id, Token: "invalid", AgentEvent: &AgentEvent{}}); err == nil {
				t.Fatal("unauthorized recovery report")
			}
		})
	}
}

func TestAgentRecoveryFailureRequiresRetryAndCloseForgets(t *testing.T) {
	s := recoveryServer(t)
	s.recovery["failed"] = agentResume{Agent: "claude", SessionID: "conversation", Dir: filepath.Join(Dir(), "missing")}
	r, err := s.restoreAgent(Request{SID: "failed"})
	if err != nil || r.Error == "" || r.Session != nil {
		t.Fatal(r, err)
	}
	os.Mkdir(s.recovery["failed"].Dir, 0o700)
	fakeResumeCLI(t, "claude", true)
	r, _ = s.restoreAgent(Request{SID: "failed"})
	if r.Error == "" || r.Session != nil {
		t.Fatal("automatically retried a failed resume")
	}
	r, err = s.restoreAgent(Request{SID: "failed", Retry: true})
	if err != nil || r.Session == nil {
		t.Fatal(r, err)
	}
	ss := s.sessions["failed"]
	select {
	case <-ss.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture did not exit")
	}
	s.updateRecoveryExit(ss)
	r, _ = s.restoreAgent(Request{SID: "failed"})
	if r.Error == "" || r.Session == nil || !r.Session.Exited || r.Session.ExitCode != 42 {
		t.Fatal("CLI failure was not retained", r)
	}
	fakeResumeCLI(t, "claude", false)
	r, err = s.restoreAgent(Request{SID: "failed", Retry: true})
	if err != nil || r.Session == nil || r.Session.PID == ss.p.cmd.Process.Pid {
		t.Fatal("retry did not replace exited fixture", r, err)
	}
	if _, err := s.do(Request{Op: "kill", SID: "failed"}); err != nil {
		t.Fatal(err)
	}
	loaded := &Server{}
	if err := loaded.loadRecovery(Dir()); err != nil || len(loaded.recovery) != 0 {
		t.Fatal("closed pane remained recoverable", err)
	}
	// Closing a pane that never successfully started must forget it as well.
	s.recovery["missing-pty"] = agentResume{Agent: "claude", SessionID: "conversation", Dir: Dir()}
	if _, err := s.do(Request{Op: "kill", SID: "missing-pty"}); err != nil || len(s.recovery) != 0 {
		t.Fatal(err)
	}
}

func TestAgentRecoveryNormalExitAndEndAll(t *testing.T) {
	s := recoveryServer(t)
	ss, err := newSession("normal", CreateOptions{Command: []string{"/bin/sh", "-c", "exit 0"}, Dir: Dir(), resumed: true})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[ss.id] = ss
	s.recovery[ss.id] = agentResume{Agent: "claude", SessionID: "conversation", Dir: Dir()}
	<-ss.done
	s.updateRecoveryExit(ss)
	if len(s.recovery) != 0 {
		t.Fatal("normal exit remained resumable")
	}
	s.recovery["old"] = agentResume{Agent: "codex", SessionID: "thread", Dir: Dir()}
	if _, err := s.do(Request{Op: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	loaded := &Server{}
	if err := loaded.loadRecovery(Dir()); err != nil || len(loaded.recovery) != 0 {
		t.Fatal("End All resurrected an agent", err)
	}
	if _, err := s.restoreAgent(Request{SID: "old"}); err == nil {
		t.Fatal("restore raced with End All")
	}
	if err := s.reportAgent(Request{SID: ss.id}); err == nil {
		t.Fatal("late hook accepted after End All")
	}
}

func TestAgentRecoveryUnknownFilePreserved(t *testing.T) {
	s := recoveryServer(t)
	data, _ := json.Marshal(recoveryFile{Version: 999, Sessions: map[string]agentResume{}})
	os.WriteFile(s.recoveryPath, data, 0o600)
	if err := s.loadRecovery(Dir()); err == nil {
		t.Fatal("unknown format accepted")
	}
	got, _ := os.ReadFile(s.recoveryPath)
	if string(got) != string(data) {
		t.Fatal("unknown data overwritten")
	}
}

func TestAgentRecoveryShutdownRetainsConversation(t *testing.T) {
	for _, mode := range []string{"end-hook", "missing-process", "shell-exit", "resume-exit"} {
		t.Run(mode, func(t *testing.T) {
			s := recoveryServer(t)
			fakeResumeCLI(t, "claude", false)
			ss, err := newSession("pane", CreateOptions{Command: []string{"/bin/sh"}, Dir: Dir(), resumed: mode == "resume-exit"})
			if err != nil {
				t.Fatal(err)
			}
			s.sessions[ss.id] = ss
			report := func(event string) {
				t.Helper()
				if err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{Event: event, SessionID: "shutdown-thread", CWD: Dir()}}}); err != nil {
					t.Fatal(err)
				}
			}
			report("SessionStart")
			report("Stop")
			original := s.recovery[ss.id]
			s.shuttingDown = func() bool { return true }
			switch mode {
			case "end-hook":
				report("SessionEnd")
			case "missing-process":
				// The Agent has already vanished, but its shell/daemon still live.
				r := original
				r.seen = time.Now().Add(-time.Minute)
				s.recovery[ss.id] = r
			case "shell-exit", "resume-exit":
				ss.input([]byte("exit 0\r"))
				select {
				case <-ss.done:
				case <-time.After(3 * time.Second):
					t.Fatal("owned shell did not exit")
				}
				s.updateRecoveryExit(ss)
			}
			s.checkRecovery()
			if _, err := s.restoreAgent(Request{SID: ss.id}); err == nil {
				t.Fatal("launched Agent during shutdown")
			}
			// Read the actual on-disk record in a new daemon, then resume it.
			reopened := &Server{sessions: map[string]*session{}}
			if err := reopened.loadRecovery(Dir()); err != nil {
				t.Fatal(err)
			}
			if got := reopened.recovery[ss.id]; got.SessionID != original.SessionID || got.Error != "" || got.Revision != original.Revision {
				t.Fatal("shutdown damaged durable conversation", got)
			}
			result, err := reopened.restoreAgent(Request{SID: ss.id, Cols: 80, Rows: 24})
			if err != nil || result.Session == nil || !result.Session.Resumed {
				t.Fatal("next boot could not resume", result, err)
			}
			t.Cleanup(func() { reopened.sessions[ss.id].kill() })
		})
	}
}

func TestAgentRecoveryExplicitCloseDuringShutdown(t *testing.T) {
	for _, op := range []string{"kill", "shutdown"} {
		t.Run(op, func(t *testing.T) {
			s := recoveryServer(t)
			s.recovery["pane"] = agentResume{Agent: "claude", SessionID: "closed-thread", Dir: Dir()}
			s.shuttingDown = func() bool { return true }
			if _, err := s.do(Request{Op: op, SID: "pane"}); err != nil {
				t.Fatal(err)
			}
			loaded := &Server{}
			if err := loaded.loadRecovery(Dir()); err != nil || len(loaded.recovery) != 0 {
				t.Fatal("explicit close survived shutdown", err)
			}
		})
	}
}

func TestAgentRecoveryPersistsRepeatedTurnRevision(t *testing.T) {
	s := recoveryServer(t)
	ss, err := newSession("pane", CreateOptions{Command: []string{"/bin/sh"}, Dir: Dir()})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[ss.id] = ss
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "UserPromptSubmit", "Stop"} {
		if err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{Event: event, SessionID: "thread", CWD: Dir()}}}); err != nil {
			t.Fatal(err)
		}
	}
	loaded := &Server{}
	if err := loaded.loadRecovery(Dir()); err != nil {
		t.Fatal(err)
	}
	if got := loaded.recovery[ss.id].Revision; got != ss.agent.state.CompletionRevision || got < 2 {
		t.Fatal("later completion revision was only updated in memory", got)
	}
}

// After a reboot the daemon's PATH comes from a non-interactive login shell
// and lacks what ~/.zshrc adds, such as Claude Code's ~/.local/bin.
func TestAgentRecoveryFindsCLIOnInteractivePath(t *testing.T) {
	s := recoveryServer(t)
	cli := fakeResumeCLI(t, "claude", false)
	userPath := filepath.Dir(cli) + string(os.PathListSeparator) + "/usr/bin:/bin"
	t.Setenv("PATH", "/usr/bin:/bin")
	previous := interactivePath
	interactivePath = func() string { return userPath }
	t.Cleanup(func() { interactivePath = previous })
	s.recovery["pane"] = agentResume{Agent: "claude", SessionID: "conversation", Dir: Dir()}
	r, err := s.restoreAgent(Request{SID: "pane"})
	if err != nil || r.Error != "" || r.Session == nil {
		t.Fatal(r, err)
	}
	ss := s.sessions["pane"]
	// Every pane's PATH starts with its private Codex launcher.
	path := ""
	for _, kv := range ss.p.cmd.Env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = value
		}
	}
	if ss.p.cmd.Path != cli || !strings.HasSuffix(path, string(os.PathListSeparator)+userPath) {
		t.Fatal("resumed without the interactive PATH", ss.p.cmd.Path, path)
	}
}

func TestAgentRecoveryMigratesDefaultConfigAndRetriesLookupAfterReboot(t *testing.T) {
	s := recoveryServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	fakeResumeCLI(t, "claude", false)
	// The daemon's own setting must not replace the original default home.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "wrong-daemon-profile"))
	s.recovery["pane"] = agentResume{Agent: "claude", SessionID: "exact-conversation", Dir: Dir(), Env: []string{"CLAUDE_CONFIG_DIR="}, Error: `exec: "claude": executable file not found in $PATH`}
	s.recovery["failed-cli"] = agentResume{Agent: "claude", SessionID: "other-conversation", Dir: Dir(), Error: "claude 恢复进程已退出（1）"}
	if err := s.saveRecovery(); err != nil {
		t.Fatal(err)
	}
	rebooted := &Server{sessions: map[string]*session{}, quit: make(chan struct{})}
	if err := rebooted.loadRecovery(Dir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, ss := range rebooted.sessions {
			ss.kill()
		}
	})
	if rebooted.recovery["pane"].Error != "" || rebooted.recovery["failed-cli"].Error == "" {
		t.Fatal("launch and CLI failures were not distinguished")
	}
	r, err := rebooted.restoreAgent(Request{SID: "pane"})
	if err != nil || r.Session == nil || r.Error != "" {
		t.Fatal(r, err)
	}
	ss := rebooted.sessions["pane"]
	config, found := "", false
	for _, kv := range ss.p.cmd.Env {
		if value, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
			config, found = value, true
		}
	}
	if found || !slices.Contains(ss.p.cmd.Args, "exact-conversation") {
		t.Fatal("default profile or exact conversation changed", config, ss.p.cmd.Args)
	}
}

func TestAgentConfigDirCapturesDefaultSentinelAndAbsoluteCustomHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, agent := range []string{"claude", "codex"} {
		for _, value := range []string{"", "relative-config", filepath.Join(home, "custom")} {
			t.Setenv(agents.ConfigEnv(agent), value)
			got := agentConfigDir(agent)
			want := ""
			if value != "" {
				want, _ = filepath.Abs(value)
			}
			if got == nil || *got != want {
				t.Fatal("hook lost effective configuration", agent, value, got)
			}
		}
	}
}
