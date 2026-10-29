package main

import "github.com/egoist/mygo/ui"

func mobileHistoryCheckbox(c *ui.Context, checked bool) {
	ui.Box(c).Size(32, 32).Shrink(0).Center().PassThrough().Children(func() {
		box := ui.Box(c).Size(22, 22).Radius(radiusS).Center().PassThrough()
		if checked {
			box.Background(c.Theme().Accent).Children(func() {
				ui.Icon(c, icon("check")).Size(18, 18).TextColor(c.Theme().AccentText).PassThrough()
			})
		} else {
			box.Border(1.5, c.Theme().Border.Mix(c.Theme().Text, 0.25))
		}
	})
}

func (m *mobileApp) clearSelectedDesktopHistory() {
	selection := m.historySelection
	m.historySelection = nil
	removed := make(map[string]bool)
	var kept []desktopRecent
	activeRemoved := false
	for _, entry := range m.history {
		if !selection[entry.Link] {
			kept = append(kept, entry)
			continue
		}
		removed[entry.Link] = true
		if entry.ID != "" {
			removed[entry.ID] = true
		}
		activeRemoved = activeRemoved || entry.Link == m.link || entry.ID != "" && entry.ID == m.hello.Host.ID
	}
	if len(removed) == 0 {
		return
	}
	if removed[m.live.desktop] {
		m.stopLiveActivity()
		m.saveLivePreference()
	}
	// Close only the phone's control tunnel if its record was selected. This
	// never ends the desktop's sessions, and other desktops keep their records.
	if activeRemoved {
		m.disconnect(false)
		m.link = ""
	} else {
		m.stopRecentPresence()
	}
	m.historyEpoch++ // Reject storage loads that predate this removal.
	m.history = kept
	var sessions []mobileRecentSession
	for _, entry := range m.recentSessions {
		if !removed[entry.Desktop] {
			sessions = append(sessions, entry)
		}
	}
	m.recentSessions = sessions
	for key := range removed {
		delete(m.presence, key)
	}
	m.persistDesktopHistory()
	m.persistRecentSessions()
	if m.store != nil && m.storage != nil {
		// Older releases saved the last capability separately. It must not
		// resurrect a removed desktop when the history becomes empty.
		m.storage <- func() { m.store.Delete("recent") }
	}
	m.invalidate()
}
