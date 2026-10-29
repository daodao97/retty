package main

import (
	"github.com/egoist/mygo"
	"retty/internal/remote"
	"time"
)

func (m *mobileApp) pauseConnection() {
	m.cancelAutomations()
	m.clearKeyboardModifiers()
	m.cancelImagePaste()
	m.stopPolling()
	m.generation++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.retryTimer != nil {
		m.retryTimer.Stop()
		m.retryTimer = nil
	}
	m.busy = false
	m.endingOpen, m.closingSession = false, ""
	m.reconnecting = true
	if m.releaseSizeLock() {
		m.lockSuspended = true
	}
	if m.stream != nil {
		m.stream.replace(nil)
	}
	if m.term != nil {
		m.term.SetInputEnabled(false)
	}
	m.closeConnectionAfterSizeRelease()
	m.invalidate()
}

func (m *mobileApp) connectionLost() {
	if m.home && !m.background {
		// No session is being viewed here; keep history available for an
		// explicit selection instead of starting a home-page retry loop.
		m.disconnect(false)
		return
	}
	if m.reconnecting || m.background || m.link == "" {
		return
	}
	if m.client != nil {
		m.clearKeyboardModifiers()
		m.cancelImagePaste()
		m.stopPolling()
		m.checkRetainedConnection()
		return
	}
	m.pauseConnection()
	m.retryAttempt = 0
	m.scheduleRetry()
}

func (m *mobileApp) scheduleRetry() {
	if m.background || !m.reconnecting || m.manualIssue() {
		return
	}
	if m.retryTimer != nil {
		m.retryTimer.Stop()
	}
	generation := m.generation
	delay := remote.RetryDelay(m.retryAttempt)
	if m.retryAttempt == 0 {
		delay = 0
	}
	m.retryAttempt++
	m.retryTimer = time.AfterFunc(delay, func() {
		mygo.RunOnMain(func() {
			if m.generation == generation && m.reconnecting && !m.background {
				m.retryTimer = nil
				m.startConnection(true)
			}
		})
	})
	m.invalidate()
}
