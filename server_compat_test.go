package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/agents"
	"retty/internal/rex"
)

func TestServerProtocolCompatibility(t *testing.T) {
	for _, version := range []int{0, 1, 3, 4, 5, 6, 7} {
		compatible := version >= 4 && version <= 6
		if got := rex.CompatibleProtocol(version); got != compatible {
			t.Fatalf("version %d compatibility = %v", version, got)
		}
		message := serverVersionError(version)
		if compatible && message != "" {
			t.Fatalf("compatible version %d prompted to end sessions: %s", version, message)
		}
		if !compatible && (!strings.Contains(message, "普通退出会保留后台服务") || !strings.Contains(message, "会结束现有会话")) {
			t.Fatalf("incompatible version %d has misleading recovery: %s", version, message)
		}
	}
}

func TestReopenReusesServerAndSessions(t *testing.T) {
	a, tt := newTestApp(t)
	a.split(false)
	a.newTab("/tmp")
	tt.Frame()
	current := a
	t.Cleanup(func() { current.client.Shutdown() })
	hello, err := a.client.Hello()
	if err != nil {
		t.Fatal(err)
	}
	infos, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]int{}
	for _, info := range infos {
		live[info.ID] = info.PID
	}
	for range 2 {
		current.saveNow()
		for _, tab := range current.tabs {
			for _, pane := range tab.panes() {
				pane.closed = true
				pane.terminal().term.Close() // Normal quit detaches; it does not kill.
			}
		}
		current.client.Close()
		client, err := rex.Connect()
		if err != nil {
			t.Fatal(err)
		}
		current = &App{client: client}
		current.hello, err = client.Hello()
		if err != nil || current.hello.PID != hello.PID || !current.hello.Started.Equal(hello.Started) {
			t.Fatalf("reopen replaced the server: %+v, %v", current.hello, err)
		}
		if message := serverVersionError(current.hello.Version); message != "" {
			t.Fatal(message)
		}
		if !current.restore() || len(current.tabs) != 2 {
			t.Fatal("reopen did not restore tabs")
		}
		count := 0
		for _, tab := range current.tabs {
			for _, pane := range tab.panes() {
				count++
				if live[pane.terminal().SID] != pane.terminal().info.PID || pane.terminal().restored {
					t.Fatal("reopen replaced a live shell")
				}
			}
		}
		if count != len(live) {
			t.Fatal("reopen lost sessions")
		}
	}
}

func TestLegacyServerCompletionNotifications(t *testing.T) {
	previousPrefs := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previousPrefs })
	clock := time.Now().Add(-time.Minute)
	p := &Pane{content: &terminalPane{SID: "legacy", info: rex.SessionInfo{ID: "legacy", Program: "codex", LastInput: clock.Add(-time.Second), Agent: rex.AgentState{ID: "codex", SessionID: "thread", State: agents.Completed, Updated: clock}}}}
	tab := &Tab{Focus: p, Root: &Node{Pane: p}}
	p.Tab = tab
	a := &App{hello: rex.Hello{Version: 4}, tabs: []*Tab{tab}, agentFinishedNotified: map[string]uint64{}}
	notices := 0
	a.agentNotify = func(mygo.NotificationOptions, func()) func() {
		notices++
		return func() {}
	}
	info := p.terminal( // Simulate version 4 JSON, always lacking the counter.
	).info
	apply := func(state string) {
		clock = clock.Add(time.Second)
		info.Agent.State, info.Agent.Updated = state, clock
		a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	}
	apply(agents.Completed)
	if notices != 0 {
		t.Fatal("restoring a historical completion notified")
	}
	apply(agents.Running)
	apply(agents.Completed)
	apply(agents.Completed) // Duplicate Stop with a different Updated time.
	if notices != 1 {
		t.Fatal("legacy completion was missing or duplicated")
	}
	info.LastInput = clock.Add(time.Second)
	clock = clock.Add(2 * time.Second)
	apply(agents.Completed) // Running happened between polls, after new input.
	apply(agents.Completed)
	if notices != 2 {
		t.Fatal("new input's completion did not notify once")
	}
	apply(agents.Running)
	apply(agents.Failed)
	apply(agents.Failed)
	if notices != 3 {
		t.Fatal("legacy failure did not notify once")
	}
	// Reopening the window replaces its snapshots but keeps the App's
	// notification history. The next completion must still be delivered.
	p.terminal().info = info
	apply(agents.Failed)
	if notices != 3 {
		t.Fatal("reopening replayed the historical failure")
	}
	apply(agents.Running)
	apply(agents.Completed)
	if notices != 4 {
		t.Fatal("retained notice history swallowed the next turn")
	}
	// Native counters, including a zero/nonzero transition on version 5,
	// must not be synthesized from terminal input.
	info.Agent.CompletionRevision = 42
	a.hello.Version = 5
	a.apply(map[string]rex.SessionInfo{p.terminal().SID: info})
	if p.terminal().info.Agent.CompletionRevision != 42 {
		t.Fatal("version 5 server's counter was replaced")
	}
}
