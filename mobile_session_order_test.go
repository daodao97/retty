package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"retty/internal/agents"
	"retty/internal/rex"
)

func TestMobileSessionsOrderByLatestUseWithinPinnedGroups(t *testing.T) {
	base := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	at := func(hour int) time.Time { return base.Add(time.Duration(hour) * time.Hour) }
	m := &mobileApp{}
	m.hello.Host.ID = "desktop"
	m.sessions = []rex.SessionInfo{
		{ID: "background", LastInput: at(2), LastOutput: at(20)},
		{ID: "waiting", Program: "codex", LastInput: at(3), Agent: rex.AgentState{ID: "codex", State: agents.Waiting}},
		{ID: "tie-a", LastInput: at(1)},
		{ID: "pinned", LastInput: at(0)},
		{ID: "host-input", LastInput: at(6)},
		{ID: "phone-opened", LastInput: at(0)},
		{ID: "new-unused", Created: at(4)},
		{ID: "tie-b", LastInput: at(1)},
	}
	before := append([]rex.SessionInfo(nil), m.sessions...)
	m.setSessionPreference("pinned", "", true)
	m.markSessionUsed("phone-opened", at(5))
	// A same-named session on another desktop must not affect this list.
	m.hello.Host.ID = "other"
	m.markSessionUsed("background", at(30))
	m.hello.Host.ID = "desktop"
	ids := func() []string {
		var result []string
		for _, s := range m.orderedSessions() {
			result = append(result, s.ID)
		}
		return result
	}
	want := []string{"pinned", "host-input", "phone-opened", "new-unused", "waiting", "background", "tie-a", "tie-b"}
	if got := ids(); !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if !reflect.DeepEqual(m.sessions, before) {
		t.Fatal("sorting mutated the host snapshot")
	}
	m.markSessionUsed("phone-opened", at(7))
	if got := ids(); got[1] != "phone-opened" {
		t.Fatal("opening an older session did not move it up", got)
	}
	m.sessions[4].LastInput = at(8)
	if got := ids(); got[1] != "host-input" {
		t.Fatal("fresh host input was not reflected", got)
	}
}

func TestMobileLastUseSurvivesDelayedPreferencesAndNameEdits(t *testing.T) {
	base := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	m := &mobileApp{}
	m.hello.Host.ID = "desktop"
	key := m.preferenceKey("session")
	saved := map[string]mobileSessionPreference{key: {Name: "旧名称", Pinned: true, LastUsed: base}}
	epoch := m.preferenceEpoch
	m.markSessionUsed("session", base.Add(time.Hour))
	m.applyLoadedSessionPreferences(saved, epoch)
	p := m.preference("session")
	if p.Name != "旧名称" || !p.Pinned || !p.LastUsed.Equal(base.Add(time.Hour)) {
		t.Fatal("opening before the Keychain load lost settings or recency", p)
	}
	m.setSessionPreference("session", "", false)
	if !m.preference("session").LastUsed.Equal(p.LastUsed) {
		t.Fatal("clearing name/pin cleared last use")
	}
	data, err := json.Marshal(m.sessionPreferences)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]mobileSessionPreference
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	reopened := &mobileApp{}
	reopened.hello.Host.ID = "desktop"
	reopened.applyLoadedSessionPreferences(restored, 0)
	if !reopened.preference("session").LastUsed.Equal(p.LastUsed) {
		t.Fatal("reopening lost saved usage")
	}
	// Explicitly ending the session also removes its usage-only preference;
	// an old asynchronous load cannot recreate it.
	epoch = m.preferenceEpoch
	m.closedSessions = map[string]bool{key: true}
	m.setSessionPreference("session", "", false)
	m.applyLoadedSessionPreferences(saved, epoch)
	if _, exists := m.sessionPreferences[key]; exists {
		t.Fatal("closed session usage was resurrected")
	}
}
