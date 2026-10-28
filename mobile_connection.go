package main

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/remote"
	"retty/internal/rex"
)

// A suspended tunnel can outlive its usable network path. Give socket reuse
// and the parallel redial a short chance, then let startConnection open a fresh
// tunnel as an explicit home-page selection would.
const mobileResumeTimeout = 5 * time.Second

func (m *mobileApp) connectionUsable() bool {
	return !m.reconnecting && remote.ConnectionUsable(m.client)
}

func (m *mobileApp) isConnectedDesktop(desktop desktopRecent) bool {
	return m.connectionUsable() && (desktop.Link == m.link || desktop.ID != "" && desktop.ID == m.hello.Host.ID)
}

func (m *mobileApp) openDesktop(desktop desktopRecent) {
	if m.isConnectedDesktop(desktop) {
		m.connect(m.link)
		return
	}
	m.connect(desktop.Link)
}

func (m *mobileApp) goHome() {
	// Leaving a recovering page cancels its retries. A healthy connection may
	// still be reused during this foreground visit until the app backgrounds.
	if m.busy || m.reconnecting {
		m.disconnect(false)
	}
	m.detach()
	m.home, m.error = true, ""
	m.resumeSID = ""
	m.invalidate()
}

func (m *mobileApp) stopPolling() {
	if m.pollCancel != nil {
		m.pollCancel()
		m.pollCancel = nil
	}
}

func (m *mobileApp) startPolling(client *rex.Client, generation int) {
	m.stopPolling()
	if m.background {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.pollCancel = cancel
	go m.poll(ctx, client, generation)
}

func (m *mobileApp) enterBackground() {
	m.closePreview()
	m.cancelUsage()
	m.cancelSpeech(true)
	m.clearKeyboardModifiers()
	if m.background {
		return
	}
	m.background = true
	remote.CloseWarmTunnels()
	if m.releaseSizeLock() {
		m.lockSuspended = true
		// Keep the terminal and control connection, but stop this inactive
		// view from resizing the PTY while the desktop takes it back.
		m.stream.replace(nil)
	}
	if m.term != nil {
		m.term.SetInputEnabled(false)
	}
	m.invalidate()
	m.resumeCheckID++
	m.stopRecentPresence()
	m.stopPolling()
	m.cancelImagePaste()
	if m.client == nil && !m.busy && !m.reconnecting {
		return
	}
	m.resumeLink = m.link
	if m.resumeSID == "" && m.selected.ID != "" {
		m.resumeSID = m.selected.ID
	}
	if m.busy {
		m.pauseConnection()
		return
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	// Let iOS suspend this idle transport. No background heartbeat or UI work
	// is needed, and an elapsed timer must not destroy a reusable tunnel.
}

func (m *mobileApp) enterForeground() {
	if !m.background {
		return
	}
	m.background = false
	if m.home {
		// The home page only previews recent desktops. Do not validate or
		// reconnect a suspended connection until the user chooses a desktop
		// or session. Explicit notification/deep-link opens leave home first.
		m.disconnect(false)
		m.error = ""
		return
	}
	if m.resumeLink == "" {
		m.invalidate()
		return
	}
	m.link, m.resumeLink = m.resumeLink, ""
	if m.manualIssue() {
		m.invalidate()
		return
	}
	if m.client != nil {
		m.checkRetainedConnection()
	} else {
		m.pauseConnection()
		m.startConnection(true)
	}
	m.invalidate()
}

// Reuse the existing authenticated control connection. Bound the check so a
// stale TCP connection after a network change cannot delay recovery for 15s.
func (m *mobileApp) checkRetainedConnection() {
	client, generation := m.client, m.generation
	m.resumeCheckID++
	checkID := m.resumeCheckID
	ctx, cancel := context.WithTimeout(context.Background(), mobileResumeTimeout)
	m.cancel = cancel
	m.reconnecting = true // Disable terminal input until the check completes.
	if m.term != nil {
		m.term.SetInputEnabled(false)
	}
	m.invalidate()
	go func() {
		next, hello, sessions, err := remote.ResumeConnection(ctx, client, m.pushSnapshot.Load())
		cancel()
		mygo.RunOnMain(func() {
			if m.generation != generation || m.client != client || m.background || m.resumeCheckID != checkID {
				if next != nil && next != client {
					next.Close()
				}
				return
			}
			if err == nil && next != client {
				client.Close()
				m.client, m.hello = next, hello
				if m.stream != nil {
					m.stream.replace(nil)
				}
			}
			m.finishRetainedConnection(m.client, generation, sessions, err)
		})
	}()
}

func (m *mobileApp) finishRetainedConnection(client *rex.Client, generation int, sessions []rex.SessionInfo, err error) {
	if m.generation != generation || m.client != client || m.background {
		return
	}
	m.cancel = nil
	if err != nil {
		m.pauseConnection()
		if remote.Failure(err) == remote.ProtocolMismatch {
			m.connectionFailed(err, true)
		} else {
			m.startConnection(true)
		}
		return
	}
	m.reconnecting, m.retryAttempt, m.error = false, 0, ""
	m.connectionIssue, m.connectionDetailsOpen = nil, false
	m.updateSessions(sessions, false)
	m.resumeSelected()
	m.startPolling(client, generation)
	m.invalidate()
}

// resumeSelected restores the open terminal after a reconnect, or opens the
// session chosen while the connection was away. A recovering connection has
// already dropped the view's transport; a retained one reattaches only when
// its transport closed or the session's geometry changed meanwhile.
func (m *mobileApp) resumeSelected() {
	sid := m.resumeSID
	m.resumeSID = ""
	if sid != "" && sid != m.selected.ID {
		m.detach()
		m.openSessionID(sid)
		return
	}
	if m.term == nil || m.stream == nil {
		return
	}
	for _, session := range m.sessions {
		if session.ID == m.selected.ID {
			geometryChanged := !m.stream.HasScreenSize() && (session.Cols != m.selected.Cols || session.Rows != m.selected.Rows)
			m.selected = session
			m.stream.geometry(session.Cols, session.Rows)
			if !m.stream.hasTransport() || geometryChanged {
				m.reattach(session)
			}
			return
		}
	}
	m.detach()
	m.error = "原会话已结束，请选择其他会话"
}
