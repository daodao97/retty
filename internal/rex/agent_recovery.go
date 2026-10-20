//go:build darwin || linux

package rex

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"retty/internal/agents"
	"retty/internal/atomicfile"
)

// agentResume contains only lifecycle/launch metadata. Never save prompts,
// transcripts, arbitrary shell commands, credentials or temporary relay sockets.
type agentResume struct {
	Revision  uint64   `json:"revision,omitempty"`
	Agent     string   `json:"agent"`
	SessionID string   `json:"sessionID"`
	Dir       string   `json:"dir"`
	Options   []string `json:"options,omitempty"`
	Env       []string `json:"env,omitempty"`
	Error     string   `json:"error,omitempty"`
	pid       int
	seen      time.Time
}

func recoveryEnv(kv string) bool {
	k, _, _ := strings.Cut(kv, "=")
	return k == agents.ConfigEnv("codex") || k == agents.ConfigEnv("claude")
}

func resumeConfigEnv(agent string, config *string, inherited []string) []string {
	key := agents.ConfigEnv(agent)
	if config != nil {
		return []string{key + "=" + *config}
	}
	// Compatibility with lifecycle events from older hook launchers.
	var env []string
	for _, kv := range inherited {
		if strings.HasPrefix(kv, key+"=") {
			env = []string{kv}
		}
	}
	return env
}

// Value options are deliberately allowlisted. Unknown options stop parsing so
// their values (which might contain a prompt) cannot be mistaken for options.
func resumeValueOptions(agent string) []string {
	if agent == "codex" {
		return []string{"-m", "--model", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-c", "--config"}
	}
	return []string{"--model", "--effort", "--permission-mode"}
}

func resumeOptions(agent string, argv []string) (out []string, supported bool) {
	for _, arg := range argv {
		if arg == "--no-session-persistence" || arg == "--ephemeral" {
			return nil, false
		}
	}
	// Skip only a recognized launcher entry, not later prompt text.
	start := 1
	if len(argv) > 1 && interpreters[filepath.Base(argv[0])] {
		start = 2
	}
	for i := start; i < len(argv); i++ {
		arg, value, equal := strings.Cut(argv[i], "=")
		if arg == "--no-session-persistence" || arg == "--ephemeral" || arg == "-p" && agent == "claude" || arg == "--print" {
			return nil, false
		}
		if slices.Contains(resumeValueOptions(agent), arg) {
			if !equal {
				i++
				if i >= len(argv) {
					return out, true
				}
				value = argv[i]
			}
			if agent == "codex" && (arg == "-c" || arg == "--config") && !codexProviderConfig(value) {
				break
			}
			out = append(out, arg, value)
			continue
		}
		// These are launch transport/cwd selectors, not reusable configuration.
		if arg == "--remote" || arg == "-C" || arg == "--cd" || arg == "-r" || arg == "--resume" {
			if !equal && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
			}
			continue
		}
		if agent == "codex" && (arg == "--no-daemon" || arg == "--full-auto") {
			out = append(out, arg)
			continue
		}
		if arg == "resume" && agent == "codex" {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
			}
			continue
		}
		if arg == "--continue" || arg == "-c" && agent == "claude" || arg == "--last" || arg == "--all" {
			continue
		}
		break
	}
	return out, true
}

// Preserve only the provider selector, never arbitrary config overrides that
// may contain bearer tokens, headers or instructions.
func codexProviderConfig(value string) bool {
	raw, ok := strings.CutPrefix(value, "model_provider=")
	var provider string
	return ok && json.Unmarshal([]byte(raw), &provider) == nil && provider != "" && len(provider) <= 256 && !strings.ContainsAny(provider, "\x00\r\n")
}

func (r agentResume) command() ([]string, error) {
	if r.Agent != "codex" && r.Agent != "claude" {
		return nil, errors.New("unsupported Agent")
	}
	if r.SessionID == "" || len(r.SessionID) > 128 || strings.HasPrefix(r.SessionID, "-") ||
		strings.IndexFunc(r.SessionID, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
		}) >= 0 {
		return nil, errors.New("invalid Agent conversation ID")
	}
	if !filepath.IsAbs(r.Dir) {
		return nil, errors.New("Agent working directory must be absolute")
	}
	st, err := os.Stat(r.Dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("Agent working directory is not a directory")
	}
	// Validate persisted options as well; never allow extra positional input.
	for i := 0; i < len(r.Options); i++ {
		option := r.Options[i]
		if slices.Contains(resumeValueOptions(r.Agent), option) {
			i++
			if i >= len(r.Options) {
				return nil, errors.New("missing Agent option value")
			}
			if r.Agent == "codex" && (option == "-c" || option == "--config") && !codexProviderConfig(r.Options[i]) {
				return nil, errors.New("unsupported Agent config override")
			}
		} else if r.Agent != "codex" || option != "--no-daemon" && option != "--full-auto" {
			return nil, errors.New("unsupported Agent resume option")
		}
	}
	for _, kv := range r.Env {
		if !recoveryEnv(kv) {
			return nil, errors.New("unsupported Agent environment")
		}
	}
	cmd := []string{r.Agent}
	if r.Agent == "codex" {
		cmd = append(cmd, "resume")
	} else {
		cmd = append(cmd, "--resume")
	}
	cmd = append(cmd, r.SessionID)
	cmd = append(cmd, r.Options...)
	if r.Agent == "codex" {
		cmd = append(cmd, "-C", r.Dir)
	}
	return cmd, nil
}

// Old hooks used an empty value to mean the user's default configuration.
// Passing that empty value to Claude instead selects the wrong history root.
// A bare key means unset; even an explicit ~/.claude can change global settings.
func (r agentResume) configEnv() ([]string, error) {
	env := slices.Clone(r.Env)
	for i, kv := range env {
		if !recoveryEnv(kv) {
			return nil, errors.New("unsupported Agent environment")
		}
		key, value, _ := strings.Cut(kv, "=")
		if value == "" {
			env[i] = key
			continue
		} else if !filepath.IsAbs(value) {
			value = filepath.Join(r.Dir, value)
		}
		env[i] = key + "=" + value
	}
	return env, nil
}

type recoveryFile struct {
	Version  int                    `json:"version"`
	Sessions map[string]agentResume `json:"sessions"`
}

// All access is serialized by Server.agentMu, including process creation and
// explicit termination. The disk record is authoritative, not a stale layout.
func (s *Server) loadRecovery(dir string) error {
	s.recoveryPath = filepath.Join(dir, "agent-resume.json")
	s.recovery = map[string]agentResume{}
	b, err := os.ReadFile(s.recoveryPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var file recoveryFile
	if err := json.Unmarshal(b, &file); err != nil {
		return err
	}
	if file.Version != 1 {
		return errors.New("unsupported Agent recovery file version")
	}
	if file.Sessions != nil {
		s.recovery = file.Sessions
	}
	for sid, r := range s.recovery {
		// Retry a missing executable once after a daemon restart. A failed CLI
		// still retains its output and requires an explicit retry.
		if strings.HasPrefix(r.Error, "exec: ") && strings.HasSuffix(r.Error, ": executable file not found in $PATH") {
			r.Error = ""
		}
		if env, err := r.configEnv(); err == nil {
			r.Env = env
		}
		s.recovery[sid] = r
	}
	return nil
}

func (s *Server) saveRecovery() error {
	if s.recoveryPath == "" {
		return nil
	} // in-memory server fixtures
	b, err := json.Marshal(recoveryFile{Version: 1, Sessions: s.recovery})
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.recoveryPath, b); err != nil {
		return err
	}
	// The record must survive a crash right after a rename that is not yet
	// on disk, so sync the directory too.
	if dir, err := os.Open(filepath.Dir(s.recoveryPath)); err == nil {
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}

func (s *Server) forgetRecovery(sid string) error {
	if _, ok := s.recovery[sid]; !ok {
		return nil
	}
	delete(s.recovery, sid)
	return s.saveRecovery()
}

func (s *Server) systemShuttingDown() bool {
	if s.shuttingDown != nil {
		return s.shuttingDown()
	}
	return SystemShuttingDown()
}

// recordAgent updates the recovery record from an accepted, normalized hook.
func (s *Server) recordAgent(ss *session, agent string, h agents.HookInput) error {
	ss.mu.Lock()
	revision := max(ss.agent.state.WaitRevision, ss.agent.state.CompletionRevision, ss.programStatus.serial)
	ss.mu.Unlock()
	if h.Event == "SessionEnd" || agent != "codex" && agent != "claude" {
		// Shutdown can terminate the Agent before this daemon. Its end hook
		// is not a user ending the conversation; retain the durable record.
		if s.systemShuttingDown() {
			return nil
		}
		return s.forgetRecovery(ss.id)
	}
	if ss.p == nil || ss.p.cmd.Process == nil {
		return nil
	}
	fg := ss.p.foregroundOrShell()
	p := inspect(fg)
	options, supported := resumeOptions(agent, p.args)
	if !supported {
		return s.forgetRecovery(ss.id)
	}
	r := agentResume{Revision: revision, Agent: agent, SessionID: h.SessionID, Dir: h.CWD, Options: options, Env: resumeConfigEnv(agent, h.ConfigDir, ss.p.cmd.Env), seen: time.Now()}
	if r.Dir == "" {
		r.Dir = p.dir
	}
	if a, ok := agents.Detect(p.programName(), p.args); ok && a.ID == agent {
		r.pid = fg
	}
	if previous, ok := s.recovery[ss.id]; ok && previous.Agent == r.Agent && previous.SessionID == r.SessionID {
		r.Revision = max(r.Revision, previous.Revision)
		// Metadata events may arrive while a tool owns the foreground group.
		if r.pid == 0 {
			r.pid, r.Options = previous.pid, previous.Options
			if h.ConfigDir == nil {
				r.Env = previous.Env
			}
		}
		if r.Dir == "" {
			r.Dir = previous.Dir
		}
	}
	if env, err := r.configEnv(); err == nil {
		r.Env = env
	} else {
		return err
	}
	if s.recovery == nil {
		s.recovery = map[string]agentResume{}
	}
	previous := s.recovery[ss.id]
	s.recovery[ss.id] = r
	if previous.Revision == r.Revision && previous.Agent == r.Agent && previous.SessionID == r.SessionID && previous.Dir == r.Dir && previous.Error == "" && slices.Equal(previous.Options, r.Options) && slices.Equal(previous.Env, r.Env) {
		return nil
	}
	return s.saveRecovery()
}

func (s *Server) restoreAgent(req Request) (RestoreResult, error) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.ending {
		return RestoreResult{}, errors.New("session server is ending")
	}
	if s.systemShuttingDown() {
		return RestoreResult{}, errors.New("system is shutting down")
	}
	if req.SID == "" {
		return RestoreResult{}, errors.New("missing pane session ID")
	}
	ss, _ := s.session(req.SID)
	if ss != nil {
		info := ss.info()
		if !info.Exited {
			return RestoreResult{Session: &info}, nil
		}
		s.updateRecoveryExit(ss)
	}
	r, ok := s.recovery[req.SID]
	if !ok {
		return RestoreResult{}, nil
	}
	result := RestoreResult{Agent: r.Agent}
	if r.Error != "" && !req.Retry {
		result.Error = r.Error
		if ss != nil {
			info := ss.info()
			result.Session = &info
		} // replay failed CLI output
		return result, nil
	}
	cmd, err := r.command()
	var env []string
	var configEnv []string
	if err == nil {
		configEnv, err = r.configEnv()
	}
	if err == nil {
		cmd[0], env, err = ResumeExecutable(cmd[0])
	}
	if err == nil {
		ss, err = newSessionForServer(req.SID, CreateOptions{Command: cmd, Dir: r.Dir, Env: append(env, configEnv...), Cols: req.Cols, Rows: req.Rows, resumed: true}, s.agentToken)
	}
	if err != nil {
		r.Error = err.Error()
		s.recovery[req.SID] = r
		if saveErr := s.saveRecovery(); saveErr != nil {
			return result, saveErr
		}
		result.Error = r.Error
		return result, nil
	}
	r.Error, r.pid, r.seen = "", ss.p.cmd.Process.Pid, time.Now()
	r.Env = configEnv
	ss.mu.Lock()
	// Recovery keeps the original pane/conversation IDs. Continue its event
	// sequence so persisted push and read receipts cannot hide the next turn.
	// Older recovery files have no sequence; use a fresh floor for migration.
	if r.Revision == 0 {
		r.Revision = uint64(time.Now().UnixMilli())
	}
	ss.agent.state = AgentState{ID: r.Agent, SessionID: r.SessionID, State: agents.Ready, Updated: time.Now(), WaitRevision: r.Revision, CompletionRevision: r.Revision}
	ss.programStatus.serial = r.Revision
	ss.mu.Unlock()
	s.recovery[req.SID] = r
	s.mu.Lock()
	if s.sessions[req.SID] == nil {
		s.order = append(s.order, req.SID)
	}
	s.sessions[req.SID] = ss
	s.mu.Unlock()
	if err := s.saveRecovery(); err != nil {
		log.Printf("rex: save Agent recovery: %v", err)
	}
	info := ss.info()
	result.Session = &info
	return result, nil
}

func (s *Server) updateRecoveryExit(ss *session) {
	if s.systemShuttingDown() {
		return
	}
	r, ok := s.recovery[ss.id]
	if !ok {
		return
	}
	ss.mu.Lock()
	exited, code, resumed := ss.exited, ss.code, ss.resumed
	ss.mu.Unlock()
	if !exited {
		return
	}
	if resumed && code != 0 {
		message := fmt.Sprintf("%s 恢复进程已退出（%d）", r.Agent, code)
		if r.Error == message {
			return
		}
		r.Error, r.pid = message, 0
		s.recovery[ss.id] = r
	} else {
		delete(s.recovery, ss.id)
	}
	if err := s.saveRecovery(); err != nil {
		log.Printf("rex: save Agent exit: %v", err)
	}
}

func (s *Server) watchRecovery() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
		}
		s.checkRecovery()
	}
}

func (s *Server) checkRecovery() {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.systemShuttingDown() {
		return
	}
	for sid, r := range s.recovery {
		ss, _ := s.session(sid)
		if ss == nil {
			continue
		} // previous boot; await a GUI restore request
		ss.mu.Lock()
		exited := ss.exited
		ss.mu.Unlock()
		if exited {
			s.updateRecoveryExit(ss)
			continue
		}
		// Until the Agent is found in the foreground, look for it there.
		pid := r.pid
		if pid == 0 {
			pid = ss.p.foreground()
		}
		p := inspect(pid)
		if a, ok := agents.Detect(p.programName(), p.args); ok && a.ID == r.Agent {
			if r.pid == 0 {
				r.pid = pid
				s.recovery[sid] = r
			}
			continue
		}
		if time.Since(r.seen) < 5*time.Second {
			continue
		}
		// An Agent killed from outside cannot emit SessionEnd. A stopped
		// or backgrounded Agent remains recorded while its process lives.
		// Process inspection can span the start of shutdown; check again at
		// the destructive boundary rather than relying on the loop's entry.
		if s.systemShuttingDown() {
			return
		}
		if err := s.forgetRecovery(sid); err != nil {
			log.Printf("rex: forget Agent recovery: %v", err)
		}
	}
}
