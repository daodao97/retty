package rex

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"retty/internal/agents"
)

// AgentState is owned by the session server, so reconnecting a window does
// not lose a pending question. WaitRevision counts distinct waiting periods.
type AgentState struct {
	CWD                string    `json:"cwd,omitempty"`
	HookEvent          string    `json:"hookEvent,omitempty"`
	ID                 string    `json:"id,omitempty"`
	SessionID          string    `json:"sessionID,omitempty"`
	ConfigDir          string    `json:"configDir,omitempty"`
	State              string    `json:"state,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	Updated            time.Time `json:"updated,omitzero"`
	WaitRevision       uint64    `json:"waitRevision,omitempty"`
	CompletionRevision uint64    `json:"completionRevision,omitempty"`
	Source             string    `json:"source,omitempty"`
	Label              string    `json:"label,omitempty"`
	Message            string    `json:"message,omitempty"`
	Progress           *int      `json:"progress,omitempty"`
	Summary            string    `json:"summary,omitempty"`
}

type AgentEvent struct {
	Agent string           `json:"agent"`
	Input agents.HookInput `json:"input"`
	At    time.Time        `json:"at"`
}

// Equal compares progress values, not their allocation after JSON decoding.
func (a AgentState) Equal(b AgentState) bool {
	if (a.Progress == nil) != (b.Progress == nil) || a.Progress != nil && *a.Progress != *b.Progress {
		return false
	}
	a.Progress, b.Progress = nil, nil
	return a == b
}

type agentTracker struct {
	state   AgentState
	lastAt  time.Time
	retired map[string]bool
	waiting map[string]string // tool -> reason; parallel tools must not clear another question
	turnID  string
	// Only the app-server observer can confirm a Codex completion together
	// with its final answer. Native terminal status carries neither turn ID
	// nor reply text.
	codexLifecycle bool
}

// reportAgent applies an event to the pane. It returns the normalized hook
// and whether the event became the pane's current state, which is all the
// recovery record needs.
func (s *session) reportAgent(token string, event *AgentEvent) (agents.HookInput, bool, error) {
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.agentToken)) != 1 {
		return agents.HookInput{}, false, errors.New("invalid agent token")
	}
	if err := validateAgentEvent(event); err != nil {
		return agents.HookInput{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return agents.HookInput{}, false, errors.New("session exited")
	}
	// Revisions share a monotonic space when a session switches between
	// generic terminal status and an agent integration.
	previous := s.agent.state
	previousView := previous
	if len(s.programStatus.records) > 0 {
		previousView = s.programStatus.agentStateWithLifecycle(&s.agent)
	}
	s.agent.apply(*event)
	if s.agent.state.WaitRevision > previous.WaitRevision {
		if previousView.State == agents.Waiting && previousView.Source != ProgramStatusSource &&
			previousView.ID == s.agent.state.ID && previousView.SessionID == s.agent.state.SessionID &&
			s.agent.turnID == event.Input.TurnID && event.Input.TurnID != "" {
			// OSC already exposed this block. The structured confirmation is
			// the same notice, rather than a second permission notification.
			s.agent.state.WaitRevision = max(s.agent.state.WaitRevision, previousView.WaitRevision)
		} else {
			s.agent.state.WaitRevision = max(s.agent.state.WaitRevision, s.programStatus.serial+1)
		}
	}
	if s.agent.state.CompletionRevision > previous.CompletionRevision {
		s.agent.state.CompletionRevision = max(s.agent.state.CompletionRevision, s.programStatus.serial+1)
	}
	h := agents.NormalizeHook(event.Agent, event.Input)
	accepted := s.agent.lastAt.Equal(event.At) && s.agent.state.ID == event.Agent && s.agent.state.SessionID == h.SessionID
	if accepted && !event.At.Before(s.programStatus.lastReport) && (h.Event == "UserPromptSubmit" || h.Event == "SessionStart") {
		s.programStatus.acknowledge()
		if len(s.programStatus.records) == 0 {
			s.programStatus.lastReport = time.Time{}
		}
	}
	return h, accepted, nil
}

func validateAgentEvent(event *AgentEvent) error {
	if event == nil {
		return errors.New("missing agent event")
	}
	if !agents.ValidHook(event.Agent, event.Input) || event.At.IsZero() || event.At.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid agent event")
	}
	return nil
}

func (t *agentTracker) apply(e AgentEvent) {
	h := agents.NormalizeHook(e.Agent, e.Input)
	if e.Agent == "codex" && h.Event == "Stop" && h.Source != agents.CodexLifecycleSource {
		// A Stop hook is a proposal to finish, before other hooks may continue
		// the task. Only turn/completed or a terminal, idle metadata snapshot confirms it.
		return
	}
	previousTurn := t.turnID
	state, reason := agents.HookStatus(e.Agent, h)
	if state == "" || e.At.Before(t.lastAt) {
		return
	}
	if t.retired[h.SessionID] {
		// Resuming an older conversation is a legitimate new start. Late
		// tool/Stop/SessionEnd events still cannot take the pane back.
		if h.Event != "SessionStart" {
			return
		}
		delete(t.retired, h.SessionID)
	}
	if t.state.SessionID != "" && t.state.SessionID != h.SessionID {
		// Only a session start or a new user turn can claim this pane.
		// Late SessionEnd/Stop events from an old conversation are ignored.
		if h.Event != "SessionStart" && h.Event != "UserPromptSubmit" {
			return
		}
		if t.retired == nil {
			t.retired = map[string]bool{}
		}
		if len(t.retired) >= 64 {
			clear(t.retired)
		}
		t.retired[t.state.SessionID] = true
		t.waiting = nil
		t.turnID = ""
		t.codexLifecycle = false
	}
	if h.Event == "SessionStart" {
		t.turnID = ""
		t.codexLifecycle = false
	}
	if h.TurnID != "" {
		if h.Event != "UserPromptSubmit" && t.turnID != "" && h.TurnID != t.turnID {
			return
		}
		t.turnID = h.TurnID
	}
	// An idle reminder after a completed response is not an unanswered
	// question. Keep the completed/ready state instead of notifying again.
	if h.Event == "Notification" && h.Notification == "idle_prompt" && (t.state.State == agents.Completed || t.state.State == agents.Ready) {
		return
	}
	if t.state.State == "" && t.state.SessionID == h.SessionID && h.Event != "SessionStart" && h.Event != "UserPromptSubmit" {
		return
	}
	t.lastAt = e.At
	if e.Agent == "codex" && h.Source == agents.CodexLifecycleSource {
		t.codexLifecycle = true
	}
	key := h.Tool
	if h.ToolID != "" {
		key += ":" + h.ToolID
	}
	if key == "" {
		key = reason
	}
	switch {
	case h.Event == "SessionStart" || h.Event == "UserPromptSubmit" || state == agents.Completed || state == agents.Ready || state == agents.Failed || state == agents.Ended:
		t.waiting = nil
	case state == agents.Waiting:
		if t.waiting == nil {
			t.waiting = map[string]string{}
		}
		// Notification repeats a permission already reported by its tool.
		duplicate := false
		if h.Event == "Notification" {
			for _, r := range t.waiting {
				if r == reason {
					duplicate = true
				}
			}
		}
		if !duplicate {
			t.waiting[key] = reason
		}
	case h.Event == "PostToolUse" || h.Event == "PostToolUseFailure" || h.Event == "ElicitationResult":
		delete(t.waiting, key)
		// Codex PermissionRequest does not include tool_use_id.
		delete(t.waiting, h.Tool)
		// Legacy notifications carry no tool ID; any subsequent completed
		// tool or new user turn resolves those notifications.
		delete(t.waiting, "permission")
		delete(t.waiting, "input")
		if h.Event == "ElicitationResult" {
			delete(t.waiting, "question")
		}
	}
	if len(t.waiting) > 0 && state != agents.Ended {
		state, reason = agents.Waiting, "input"
		for _, r := range t.waiting {
			if r == "permission" {
				reason = r
				break
			}
			if r == "question" {
				reason = r
			}
		}
	}
	revision := t.state.WaitRevision
	if state == agents.Waiting && (t.state.State != agents.Waiting || t.state.SessionID != h.SessionID) {
		revision++
	}
	completion := t.state.CompletionRevision
	finished := agents.Finished(state)
	wasFinished := agents.Finished(t.state.State)
	if finished && (!wasFinished || t.state.SessionID != h.SessionID || (h.TurnID != "" && h.TurnID != previousTurn)) {
		completion++
	}
	summary := ""
	if finished {
		summary = agents.SummaryText(h.Summary)
		if summary == "" && wasFinished && completion == t.state.CompletionRevision && t.state.SessionID == h.SessionID {
			summary = t.state.Summary
		}
	}
	configDir := t.state.ConfigDir
	if t.state.ID != e.Agent || t.state.SessionID != h.SessionID {
		configDir = ""
	}
	if h.ConfigDir != nil {
		configDir = *h.ConfigDir
		if configDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				configDir = filepath.Join(home, "."+e.Agent)
			}
		} else if !filepath.IsAbs(configDir) && filepath.IsAbs(h.CWD) {
			configDir = filepath.Join(h.CWD, configDir)
		}
	}
	cwd := h.CWD
	if cwd == "" && t.state.ID == e.Agent && t.state.SessionID == h.SessionID {
		cwd = t.state.CWD
	}
	t.state = AgentState{CWD: cwd, HookEvent: h.Event, ID: e.Agent, SessionID: h.SessionID, ConfigDir: configDir, State: state, Reason: reason, Updated: e.At, WaitRevision: revision, CompletionRevision: completion, Summary: summary}
	if state == agents.Ended {
		t.state.State = ""
	}
}

// Lifecycle hooks are silent and bounded. A Claude PermissionRequest can also
// return the user's explicit structured decision while a phone views its pane.
func RunAgentHook(agent string, stdin io.Reader) {
	RunAgentHookWithOutput(agent, stdin, os.Stdout)
}

func RunAgentHookWithOutput(agent string, stdin io.Reader, out io.Writer) {
	sid, token, socket := os.Getenv("RETTY_SESSION"), os.Getenv("RETTY_AGENT_TOKEN"), os.Getenv("RETTY_AGENT_SOCKET")
	if agent == "codex" && os.Getenv("RETTY_AGENT_SERVER_TOKEN") != "" {
		token = os.Getenv("RETTY_AGENT_SERVER_TOKEN")
	}
	if sid == "" || token == "" || socket == "" {
		return
	}
	type inputResult struct {
		data []byte
		err  error
	}
	input := make(chan inputResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
		input <- inputResult{data, err}
	}()
	var data []byte
	var err error
	select {
	case result := <-input:
		data, err = result.data, result.err
	case <-time.After(500 * time.Millisecond):
		return
	}
	if err != nil || len(data) > 1<<20 {
		return
	}
	h, valid := agents.ParseHook(agent, data)
	if !valid {
		return
	}
	h.ConfigDir = agentConfigDir(agent)
	event := AgentEvent{Agent: agent, Input: h, At: time.Now()}
	if ReportAgent(socket, sid, token, event) == nil {
		_ = agents.SaveHookTask(Dir(), agent, h, data, event.At)
	}
	if agent == "claude" {
		runClaudeApproval(data, out)
	}
}

func agentConfigDir(agent string) *string {
	key := agents.ConfigEnv(agent)
	if key == "" {
		return nil
	}
	value := os.Getenv(key)
	if value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil
		}
		value = absolute
	}
	return &value
}

func ReportAgent(socket, sid, token string, event AgentEvent) error {
	conn, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := 750 * time.Millisecond
	if agents.NormalizeHook(event.Agent, event.Input).Event == "UserPromptSubmit" {
		deadline = 3 * time.Second // allow the bounded pre-turn Git baseline
	}
	conn.SetDeadline(time.Now().Add(deadline))
	if err := json.NewEncoder(conn).Encode(Request{ID: 1, Op: "agentEvent", SID: sid, Token: token, AgentEvent: &event}); err != nil {
		return err
	}
	var res Response
	if err := json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&res); err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}
