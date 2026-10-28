package main

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

const mobileRecentSessionLimit = 8

// Keep only identity and display metadata. Connection capabilities live in
// desktop history so a shortcut always uses the latest link for that device.
type mobileRecentSession struct {
	Desktop string `json:"desktop"`
	Session string `json:"session"`
	Title   string `json:"title"`
	Program string `json:"program,omitempty"`
}

func mergeRecentSessions(first, second []mobileRecentSession) []mobileRecentSession {
	var result []mobileRecentSession
	seen := map[string]bool{}
	for _, entries := range [][]mobileRecentSession{first, second} {
		for _, entry := range entries {
			key := sessionKey(entry.Desktop, entry.Session)
			if entry.Desktop == "" || entry.Session == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, entry)
			if len(result) == mobileRecentSessionLimit {
				return result
			}
		}
	}
	return result
}

func (m *mobileApp) recentDesktop(key string) (desktopRecent, bool) {
	for _, desktop := range m.history {
		if desktop.ID == key || desktop.Link == key {
			return desktop, true
		}
	}
	return desktopRecent{}, false
}

func (m *mobileApp) recentSessionTitle(entry mobileRecentSession) string {
	if name := m.sessionPreferences[sessionKey(entry.Desktop, entry.Session)].Name; name != "" {
		return name
	}
	return positionTitleFolder(cmp.Or(entry.Title, "终端会话"), m.titleFolderPosition)
}

func (m *mobileApp) rememberSession(s rex.SessionInfo) {
	if s.Exited {
		return
	}
	m.markSessionUsed(s.ID, time.Now())
	entry := mobileRecentSession{Desktop: m.desktopKey(), Session: s.ID, Title: mobileSessionTitle(s), Program: sessionProgramName(s)}
	m.recentSessions = mergeRecentSessions([]mobileRecentSession{entry}, m.recentSessions)
	m.persistRecentSessions()
}

func (m *mobileApp) persistRecentSessions() {
	saved, _ := json.Marshal(m.recentSessions)
	epoch := m.historyEpoch
	m.save("recent-sessions", saved, func() {
		if m.historyEpoch == epoch {
			m.error = "无法保存最近会话，请重试"
			m.invalidate()
		}
	})
}

func (m *mobileApp) applyLoadedRecentSessions(saved []mobileRecentSession, epoch int) {
	if m.historyEpoch != epoch {
		return // Clearing connection history also clears its session shortcuts.
	}
	m.recentSessions = mergeRecentSessions(m.recentSessions, saved)
	m.refreshRecentSessions()
	// A session may have been opened before the Keychain load completed.
	// Save the merged snapshot last, preserving other desktops' history.
	m.persistRecentSessions()
}

func (m *mobileApp) applyLoadedConnectionHistory(history []desktopRecent, sessions []mobileRecentSession, epoch int) {
	if m.historyEpoch != epoch {
		return
	}
	// A delayed Keychain read must not restore an alias cleared in settings.
	history = slices.Clone(history)
	for i, saved := range history {
		for _, current := range m.history {
			if m.deviceNameTouched[current.ID] || m.deviceNameTouched[current.Link] {
				if saved.Link == current.Link || sameDesktop(saved, current) {
					history[i].Alias = ""
					break
				}
			}
		}
	}
	m.history = mergeDesktopHistory(m.history, history)
	m.persistDesktopHistory()
	// Resolve old link-based identities before discarding rotated links.
	for i, session := range sessions {
		for _, previous := range history {
			if previous.ID != session.Desktop && previous.Link != session.Desktop {
				continue
			}
			for _, current := range m.history {
				if current.Link == previous.Link || sameDesktop(current, previous) {
					sessions[i].Desktop = current.key()
					break
				}
			}
			break
		}
	}
	m.applyLoadedRecentSessions(sessions, epoch)
}

// Refresh display metadata and discard ended sessions only for the connected desktop.
// An unreachable desktop keeps its shortcuts so the user can retry later.
func (m *mobileApp) refreshRecentSessions() {
	var next []mobileRecentSession
	for _, entry := range m.recentSessions {
		if _, ok := m.recentDesktop(entry.Desktop); !ok {
			continue
		}
		if m.client != nil && entry.Desktop == m.desktopKey() {
			found := false
			for _, session := range m.sessions {
				if session.ID == entry.Session && !session.Exited {
					entry.Title = mobileSessionTitle(session)
					entry.Program = sessionProgramName(session)
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		next = append(next, entry)
	}
	if !slices.Equal(next, m.recentSessions) {
		m.recentSessions = next
		m.persistRecentSessions()
	}
}

func (m *mobileApp) migrateRecentDesktop(current desktopRecent) {
	previousSessions := slices.Clone(m.recentSessions)
	key := current.key()
	for i, entry := range m.recentSessions {
		previous, ok := m.recentDesktop(entry.Desktop)
		if ok && (previous.Link == current.Link || sameDesktop(previous, current)) {
			m.recentSessions[i].Desktop = key
		}
	}
	m.recentSessions = mergeRecentSessions(m.recentSessions, nil)
	if !slices.Equal(previousSessions, m.recentSessions) {
		m.persistRecentSessions()
	}
}

func (m *mobileApp) openSessionID(sid string) {
	for _, s := range m.sessions {
		if s.ID == sid && !s.Exited {
			m.openSession(s)
			return
		}
	}
	m.refreshRecentSessions()
	m.error = "该会话已结束，请选择其他会话"
	m.invalidate()
}

func (m *mobileApp) openRecentSession(entry mobileRecentSession) {
	m.historySelection = nil
	desktop, ok := m.recentDesktop(entry.Desktop)
	if !ok {
		m.refreshRecentSessions()
		m.error = "请重新扫码连接该桌面"
		m.invalidate()
		return
	}
	if m.connectionUsable() && entry.Desktop == m.desktopKey() {
		m.home = false
		m.openSessionID(entry.Session)
		return
	}
	m.detach()
	m.connect(desktop.Link)
	if m.busy || m.reconnecting {
		m.resumeSID = entry.Session
	}
}

func (m *mobileApp) recentSessionsView(c *ui.Context) {
	ui.Column(c).FillWidth().Children(func() {
		mobileListSectionHeader(c, "最近会话", nil)
		mobileListGroup(c).Children(func() {
			if len(m.recentSessions) == 0 {
				mobileListEmpty(c, "打开过的会话会显示在这里")
			}
			for i, entry := range m.recentSessions {
				entry := entry
				desktop, ok := m.recentDesktop(entry.Desktop)
				if !ok {
					continue
				}
				if i > 0 {
					mobileListDivider(c)
				}
				row := mobileListRow(c, "recent-session-"+entry.Desktop+"-"+entry.Session, "进入最近会话 "+entry.Desktop+" "+entry.Session, 64).Disabled(m.busy || m.scanning || m.reconnecting).Value(m.recentSessionTitle(entry) + " · " + desktop.displayName())
				row.Children(func() {
					mobileSessionIcon(c, entry.Program)
					mobileListText(c, mobileListTextOptions{Title: m.recentSessionTitle(entry), Subtitle: desktop.displayName()})
					mobileListChevron(c)
				})
				if row.Clicked() {
					m.openRecentSession(entry)
				}
			}
		})
	})
}
