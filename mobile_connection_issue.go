package main

import (
	"log"

	"github.com/egoist/mygo/ui"
)

func (m *mobileApp) connectionFailed(err error, recovering bool) {
	m.busy, m.cancel, m.error = false, nil, ""
	m.connectionIssue = connectionIssueFor(err)
	log.Printf("mobile connect: %s", m.connectionIssue.title)
	if recovering {
		m.scheduleRetry()
	}
	m.invalidate()
}

func (m *mobileApp) retryConnection() {
	if m.background || m.busy || m.link == "" {
		return
	}
	recovering := m.reconnecting || m.term != nil || m.client != nil
	m.pauseConnection()
	m.reconnecting = recovering
	m.connectionIssue, m.connectionDetailsOpen = nil, false
	m.retryAttempt, m.error = 0, ""
	m.startConnection(recovering)
}

func (m *mobileApp) rescanConnection() {
	m.disconnect(false)
	m.scan()
}

func (m *mobileApp) needsRecovery() bool {
	return m.reconnecting || m.stream != nil && !m.stream.inputReady()
}

// degraded tells that the connection is recovering or has a reported issue,
// so sessions cannot be opened yet.
func (m *mobileApp) degraded() bool {
	return m.needsRecovery() || m.connectionIssue != nil
}

// manualIssue tells that a connection issue waits for the user instead of
// automatic retries.
func (m *mobileApp) manualIssue() bool {
	return m.connectionIssue != nil && !m.connectionIssue.automatic
}

func (m *mobileApp) recoveryAnimating() bool {
	return !m.background && m.needsRecovery() && !m.manualIssue()
}

func (m *mobileApp) recoveryStatus() string {
	if m.manualIssue() {
		return m.connectionIssue.title
	}
	if !m.reconnecting {
		return "正在载入会话…"
	}
	return "正在重连…"
}

// Inline feedback is used on the home/session pages. The terminal keeps its
// viewport unchanged and exposes the same actions through its fixed header.
func (m *mobileApp) connectionFeedback(c *ui.Context) {
	if !m.degraded() {
		return
	}
	title, body := "正在重连", "恢复连接后可打开会话"
	if m.term != nil {
		body = "已有内容仍可阅读"
	}
	if m.connectionIssue != nil {
		title, body = m.connectionIssue.title, m.connectionIssue.body
	}
	if connectionStatusSummary(c, title, body, m.recoveryAnimating()).Clicked() {
		m.connectionDetailsOpen = true
	}
}

func (m *mobileApp) connectionActions(c *ui.Context) {
	ui.Row(c).FillWidth().Gap(spaceXS).Children(func() {
		if (m.connectionIssue == nil || m.connectionIssue.retry) && m.link != "" {
			if mobileIconAction(c, "重试连接", "rotate-ccw").Disabled(m.busy).Clicked() {
				m.retryConnection()
			}
		}
		if mobileIconAction(c, "重新扫码连接", "scan-line").Disabled(m.scanning).Clicked() {
			m.rescanConnection()
		}
		if m.needsRecovery() && mobileIconAction(c, "取消重连", "x").Clicked() {
			m.disconnect(false)
		}
	})
}

func (m *mobileApp) connectionDialog(c *ui.Context) {
	ui.DialogBase(c, &m.connectionDetailsOpen, func(back, panel ui.Element) {
		back.Background(colorsOf(c).scrim)
		panel.Width(340).MaxWidthPercent(95).Padding(spaceXL).Radius(radiusXL).Background(c.Theme().Surface).Column().Gap(spaceL)
		title, body := "正在恢复连接", "恢复后将继续当前会话，可先阅读已有内容。"
		closeText := "继续阅读"
		if m.term == nil {
			title, body = "正在重新连接", "会话列表已保留，连接恢复后即可打开会话。"
			closeText = "返回列表"
		}
		if m.connectionIssue != nil {
			title, body = m.connectionIssue.title, m.connectionIssue.body
		}
		ui.Text(c, title).FontSize(mobileTitle).Bold()
		ui.Text(c, body).FontSize(mobileSubhead).LineHeight(1.4).TextColor(c.Theme().TextMuted)
		m.connectionActions(c)
		m.connectionDiagnosticsAction(c)
		if ui.Button(c, closeText).Label("关闭连接提示").FillWidth().Height(44).Clicked() {
			m.connectionDetailsOpen = false
		}
	})
}
