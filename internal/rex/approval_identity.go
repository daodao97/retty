package rex

import "retty/internal/agents"

// ApprovalIdentity identifies the live agent. OSC 7501 IDs describe terminal
// programs, not Agent conversations: the private producer supplies the latter.
// Empty thread is allowed for discovery only; answers still name the exact
// producer-owned thread, pending request and immutable option.
func ApprovalIdentity(s SessionInfo) (agent, thread string) {
	if s.Exited || s.Idle {
		return "", ""
	}
	a := s.Agent
	if a.Source == ProgramStatusSource {
		if a.ID == "codex" || a.ID == "claude" {
			return a.ID, ""
		}
		return "", ""
	}
	detected, ok := agents.Detect(s.Program, s.Args)
	if !ok || (detected.ID != "codex" && detected.ID != "claude") {
		return "", ""
	}
	if a.ID == detected.ID && a.State != "" {
		return detected.ID, a.SessionID
	}
	return detected.ID, ""
}
