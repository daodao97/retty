package rex

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"retty/internal/agents"
)

// The Codex app-server can outlive its first pane and the Retty server.
// Keep its capability private and stable instead of tying it to that pane.
// Serve holds the server lock while loading/creating this file.
func loadAgentToken(dir string) (string, error) {
	path := filepath.Join(dir, "agent-token")
	data, err := os.ReadFile(path)
	if err == nil {
		decoded, decodeErr := hex.DecodeString(string(data))
		if decodeErr != nil || len(decoded) != 32 {
			return "", errors.New("invalid stored agent token")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(secret[:])
	return token, os.WriteFile(path, []byte(token), 0o600)
}

type codexCandidate struct {
	sid, cwd, session string
	retired           bool
}

func sameAgentDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	a, errA := filepath.EvalSymlinks(a)
	b, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && a == b
}

// A shared daemon inherits the first CLI's pane environment. That pane ID
// cannot break ties safely: use an established conversation owner, then cwd
// and whether this is a new conversation or /new in an existing one.
func codexTarget(candidates []codexCandidate, h agents.HookInput) string {
	var owners []codexCandidate
	for _, c := range candidates {
		if c.session == h.SessionID {
			owners = append(owners, c)
		}
		if c.retired && h.Event != "SessionStart" {
			return "" // late events must not claim another pane
		}
	}
	if len(owners) == 1 {
		return owners[0].sid
	}
	if len(owners) > 1 || (h.Event != "SessionStart" && h.Event != "UserPromptSubmit") {
		return ""
	}
	var here []codexCandidate
	for _, c := range candidates {
		if sameAgentDir(c.cwd, h.CWD) {
			here = append(here, c)
		}
	}
	if len(here) > 0 {
		candidates = here
	}
	var likely []codexCandidate
	for _, c := range candidates {
		if (c.session != "") == (h.Source == "clear") {
			likely = append(likely, c)
		}
	}
	if len(likely) > 0 {
		candidates = likely
	}
	if len(candidates) == 1 {
		return candidates[0].sid
	}
	return "" // indistinguishable starts: do not notify the wrong pane
}

func (s *Server) reportAgent(req Request) (err error) {
	var capture func()
	s.agentMu.Lock()
	defer func() {
		s.agentMu.Unlock()
		if capture != nil {
			capture()
		}
	}()
	if s.ending {
		return errors.New("session server is ending")
	}
	if req.AgentEvent == nil || req.AgentEvent.Agent != "codex" || s.agentToken == "" ||
		subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.agentToken)) != 1 {
		ss, err := s.session(req.SID)
		if err != nil {
			return err
		}
		capture, err = s.deliverAgent(ss, req.Token, req.AgentEvent)
		return err
	}
	if err := validateAgentEvent(req.AgentEvent); err != nil {
		return err
	}
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		all = append(all, ss)
	}
	s.mu.Unlock()
	var candidates []codexCandidate
	for _, ss := range all {
		info := ss.info()
		a, ok := agents.Detect(info.Program, info.Args)
		if !ok || a.ID != "codex" || info.Idle || info.Exited {
			continue
		}
		ss.mu.Lock()
		state := ss.agent.state
		retired := ss.agent.retired[req.AgentEvent.Input.SessionID]
		ss.mu.Unlock()
		candidates = append(candidates, codexCandidate{sid: ss.id, cwd: info.Dir, session: state.SessionID, retired: retired})
	}
	id := codexTarget(candidates, req.AgentEvent.Input)
	if id == "" {
		return errors.New("cannot identify Codex pane")
	}
	ss, err := s.session(id)
	if err != nil {
		return err
	}
	capture, err = s.deliverAgent(ss, ss.agentToken, req.AgentEvent)
	return err
}

// deliverAgent applies an event to its pane, then records what recovery
// needs when the event became the pane's current state.
func (s *Server) deliverAgent(ss *session, token string, event *AgentEvent) (func(), error) {
	h, accepted, err := ss.reportAgent(token, event)
	if err != nil || !accepted {
		return nil, err
	}
	capture := s.prepareAgentTurn(ss, event, h)
	return capture, s.recordAgent(ss, event.Agent, h)
}

// The accepted root turn event defines the baseline. A viewer never invents a
// baseline by noticing a spinner later. Capture is bounded and leaves PTY I/O
// and terminal dimensions alone; failure does not fail the Agent's hook.
func (s *Server) prepareAgentTurn(ss *session, event *AgentEvent, h agents.HookInput) func() {
	if s.turnSnapshots == nil || h.Event != "UserPromptSubmit" {
		return nil
	}
	if event.Agent == "codex" && (h.Source != agents.CodexLifecycleSource || h.TurnID == "") {
		ss.mu.Lock()
		ss.gitTurn = ""
		ss.mu.Unlock()
		return nil // native hooks/ephemeral helpers are not the canonical root turn
	}
	identity := h.SessionID + "\x00" + h.TurnID
	if h.TurnID == "" {
		identity += strconv.FormatInt(event.At.UnixNano(), 10)
	}
	if s.turnStarts[ss.id] == identity {
		ss.mu.Lock()
		ss.gitTurn = identity
		ss.mu.Unlock()
		return nil
	}
	if s.turnStarts == nil {
		s.turnStarts = make(map[string]string)
	}
	s.turnStarts[ss.id] = identity
	dir := h.CWD
	if dir == "" {
		dir = ss.info().Dir
	}
	budget := time.Second // fit the existing native hook's two-second timeout
	if event.Agent == "codex" {
		budget = 2 * time.Second // canonical observer reports independently of hooks
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	ss.mu.Lock()
	if ss.gitCaptureCancel != nil {
		ss.gitCaptureCancel()
	}
	ss.gitTurn, ss.gitCaptureCancel = identity, cancel
	ss.mu.Unlock()
	return func() {
		defer cancel()
		// Disk/Git work runs after agentMu is released. A slow worktree must
		// never delay lifecycle reports for the server's other sessions.
		ss.gitCaptureMu.Lock()
		defer ss.gitCaptureMu.Unlock()
		ss.mu.Lock()
		current := ss.gitTurn == identity && !ss.exited
		ss.mu.Unlock()
		if !current || ctx.Err() != nil {
			return
		}
		if err := s.turnSnapshots.Invalidate(ctx, dir, ss.id, "本轮快照未记录成功；工作区可能过大或在记录时发生变化"); err != nil {
			return
		}
		_, _ = s.turnSnapshots.CaptureTurn(ctx, dir, ss.id, identity)
	}
}
