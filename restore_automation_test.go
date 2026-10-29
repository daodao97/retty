package main

import (
	"encoding/json"
	"testing"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

// Restarting the window must not give an automation's agent a tab, from the
// saved layout or as an untabbed session, unless the person opened one.
func TestRestoreKeepsAutomationSessionsOutOfTabs(t *testing.T) {
	a, _ := newTestApp(t)
	shell := a.tab().Focus.terminal().SID
	agent, err := a.client.Create(rex.CreateOptions{Dir: "/tmp", Command: []string{"/bin/sh", "-c", "exec sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.Kill(agent.ID)
	item, err := automation.New(automation.Automation{Goal: "每小时检查 CI", Every: "1h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = automation.Save(rex.Dir(), item); err != nil {
		t.Fatal(err)
	}
	// The last run finished; its agent stays for the next run to reuse.
	if err = automation.UpdateRun(automation.RunPath(rex.Dir(), item.ID), func(r *automation.Run) {
		*r = automation.Run{Started: time.Now().Add(-time.Minute), Agent: agent.ID, State: "done"}
	}); err != nil {
		t.Fatal(err)
	}
	restart := func(l savedLayout) {
		t.Helper()
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.client.SetLayout(data); err != nil {
			t.Fatal(err)
		}
		a.tabs, a.active, a.hiddenSessions, a.openedAutomation = nil, 0, nil, nil
		if !a.restore() {
			t.Fatal("layout not restored")
		}
	}
	shown := func(sid string) bool {
		for _, p := range a.tabs {
			for _, pane := range p.panes() {
				if pane.terminal() != nil && pane.terminal().SID == sid {
					return true
				}
			}
		}
		return false
	}

	// An earlier version saved the agent in a split beside the shell.
	restart(savedLayout{Tabs: []savedTab{{Root: &savedNode{Ratio: 0.5, A: &savedNode{SID: shell}, B: &savedNode{SID: agent.ID}}}}})
	if shown(agent.ID) || !shown(shell) || len(a.tabs) != 1 || len(a.tabs[0].panes()) != 1 {
		t.Fatal("saved automation agent restored as a tab")
	}
	// Untabbed, it is no orphan to give a tab either.
	restart(savedLayout{Tabs: []savedTab{{Root: &savedNode{SID: shell}}}})
	if shown(agent.ID) || len(a.tabs) != 1 {
		t.Fatal("automation agent adopted as a tab on restart")
	}
	// Opened in a tab on request, it stays one.
	restart(savedLayout{Tabs: []savedTab{{Root: &savedNode{SID: shell}}, {Root: &savedNode{SID: agent.ID, AutomationOpened: true}}}})
	if !shown(agent.ID) || !a.openedAutomation[agent.ID] || a.hiddenAutomationSession(agent) {
		t.Fatal("an automation tab the person opened was not restored")
	}
	var opened bool
	for _, tab := range a.tabs {
		if v := tab.Focus.terminal(); v != nil && v.SID == agent.ID {
			opened = tab.Focus.content.snapshot(tab.Focus).AutomationOpened
		}
	}
	if !opened {
		t.Fatal("the opened flag is not saved with the layout again")
	}
	// The session itself kept running throughout.
	infos, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.ID == agent.ID && in.Exited {
			t.Fatal("automation agent ended")
		}
	}
}
