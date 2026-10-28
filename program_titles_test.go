package main

import (
	"testing"

	"retty/internal/agents"
	"retty/internal/rex"
)

func TestCodexTitleSpinnerDoesNotOverrideAgentLifecycle(t *testing.T) {
	for _, frame := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
		for _, state := range []string{agents.Running, agents.Ready, agents.Waiting, agents.Completed, agents.Failed, ""} {
			s := rex.SessionInfo{Program: "codex", Title: string(frame) + " 继续优化面板UI | retty", Agent: rex.AgentState{ID: "codex", State: state}}
			p := &Pane{content: &terminalPane{info: s, title: s.Title}}
			want := "继续优化面板UI | retty"
			if name, _ := p.terminal().label(p); name != want {
				t.Fatalf("desktop title conflicts with %s: %q", state, name)
			}
			if name := mobileSessionTitle(s); name != want {
				t.Fatalf("phone title conflicts with %s: %q", state, name)
			}
			if p.terminal().title != s.Title || p.terminal().info.Agent.State != state {
				t.Fatal("title cleanup changed terminal data or lifecycle state")
			}
		}
	}
}

func TestCodexTitleCleanupPreservesMeaningfulTextAndOtherPrograms(t *testing.T) {
	for _, tc := range []struct{ agent, title, want string }{
		{"codex", "⠋\u00a0Review login", "Review login"},
		{"codex", "Review ⠋ login", "Review ⠋ login"},
		{"codex", "⠋⠙点阵文本", "⠋⠙点阵文本"},
		{"codex", "⠿ 点阵示例", "⠿ 点阵示例"},
		{"codex", "🧪 Review tests", "🧪 Review tests"},
		{"codex", "⠋", ""},
		{"codex", "Review login", "Review login"},
		{"node", "⠋ Build task", "⠋ Build task"},
		{"claude", "✳\ufe0f Review login", "Review login"},
	} {
		if got := programTitle(tc.title, programOf(tc.agent)); got != tc.want {
			t.Fatalf("%s %q => %q, want %q", tc.agent, tc.title, got, tc.want)
		}
	}
}
