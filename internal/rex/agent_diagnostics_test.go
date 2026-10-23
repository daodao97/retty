package rex

import (
	"testing"
	"time"

	"retty/internal/agents"
)

func TestAgentDiagnosticsRetainsCWDOnlyForSameConversation(t *testing.T) {
	var tracker agentTracker
	at := time.Now()
	tracker.apply(AgentEvent{Agent: "claude", At: at, Input: agents.HookInput{SessionID: "first", Event: "SessionStart", CWD: "/old/project"}})
	tracker.apply(AgentEvent{Agent: "claude", At: at.Add(time.Second), Input: agents.HookInput{SessionID: "first", Event: "PreToolUse"}})
	if tracker.state.CWD != "/old/project" || tracker.state.HookEvent != "PreToolUse" {
		t.Fatal("lost lifecycle diagnostic context", tracker.state)
	}
	tracker.apply(AgentEvent{Agent: "claude", At: at.Add(2 * time.Second), Input: agents.HookInput{SessionID: "second", Event: "SessionStart"}})
	if tracker.state.CWD != "" || tracker.state.HookEvent != "SessionStart" {
		t.Fatal("borrowed old conversation cwd", tracker.state)
	}
}
