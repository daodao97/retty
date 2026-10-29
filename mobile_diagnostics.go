package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

func (m *mobileApp) recordConnectionDiagnostic(report remote.DiagnosticReport) {
	m.connectionDiagnostics = append(m.connectionDiagnostics, report)
	if len(m.connectionDiagnostics) > 6 {
		m.connectionDiagnostics = m.connectionDiagnostics[len(m.connectionDiagnostics)-6:]
	}
	m.connectionDiagnosticsCopied = false
	log.Printf("%s", report.Text)
	m.persistConnectionDiagnostics()
}

// Opening a link may complete before the Keychain load reaches the main
// thread. Merge those attempts so an early success cannot erase older failures.
func (m *mobileApp) restoreConnectionDiagnostics(saved []remote.DiagnosticReport) {
	hadNew := len(m.connectionDiagnostics) > 0
	combined := append(saved[:len(saved):len(saved)], m.connectionDiagnostics...)
	slices.SortStableFunc(combined, func(a, b remote.DiagnosticReport) int { return a.Started.Compare(b.Started) })
	combined = slices.CompactFunc(combined, func(a, b remote.DiagnosticReport) bool { return a.Started.Equal(b.Started) && a.Text == b.Text })
	m.connectionDiagnostics = combined[max(0, len(combined)-6):]
	if hadNew {
		m.persistConnectionDiagnostics()
	}
}

func (m *mobileApp) persistConnectionDiagnostics() {
	data, _ := json.Marshal(m.connectionDiagnostics)
	m.save("connection-diagnostics-v1", data, func() { log.Print("Retty: 无法保存连接诊断") })
}

func (m *mobileApp) connectionDiagnosticsAction(c *ui.Context) {
	if m.connectionIssue == nil || len(m.connectionDiagnostics) == 0 {
		return
	}
	if mobileTextAction(c, "查看连接诊断", "连接诊断").Clicked() {
		m.connectionDetailsOpen, m.sessionSettingsOpen = false, false
		m.connectionDiagnosticsOpen, m.connectionDiagnosticsCopied = true, false
	}
}

func (m *mobileApp) connectionDiagnosticsText() string {
	var reports []string
	for _, report := range m.connectionDiagnostics {
		reports = append(reports, report.Text)
	}
	return strings.Join(reports, "\n\n────────\n\n")
}

func (m *mobileApp) connectionDiagnosticsDialog(c *ui.Context) {
	if m.connectionIssue == nil {
		m.connectionDiagnosticsOpen = false
		return
	}
	ui.DialogBase(c, &m.connectionDiagnosticsOpen, func(back, panel ui.Element) {
		back.Background(colorsOf(c).scrim)
		panel.Label("连接诊断面板").Width(440).MaxWidthPercent(95).HeightPercent(85).MaxHeight(640).Padding(spaceXL).Radius(radiusXL).Background(c.Theme().Surface).Column().Gap(spaceM)
		ui.Text(c, "连接诊断").FontSize(mobileTitle).Bold()
		ui.Text(c, "保留最近 6 次连接记录，便于排查连接失败原因。").FontSize(textSmall).TextColor(c.Theme().TextMuted).FillWidth()
		ui.Scroll(c.Key("connection-diagnostics-scroll")).Grow(1).MinHeight(0).FillWidth().Gap(spaceXL).Children(func() {
			for i := len(m.connectionDiagnostics) - 1; i >= 0; i-- {
				report := m.connectionDiagnostics[i]
				ui.Column(c.Key(fmt.Sprintf("diagnostic-%d", i))).FillWidth().Gap(spaceS).Children(func() {
					ui.Text(c, report.Result).FontSize(mobileSubhead).Bold().FillWidth()
					ui.Text(c, report.Text).Label(fmt.Sprintf("连接诊断记录 %d", i+1)).FontSize(textSmall).LineHeight(1.4).FillWidth()
				})
			}
		})
		copyText := "复制诊断"
		if m.connectionDiagnosticsCopied {
			copyText = "已复制"
		}
		ui.Row(c).FillWidth().Gap(spaceM).Children(func() {
			if ui.Button(c, copyText).Label("复制连接诊断").Grow(1).MinWidth(0).Height(44).Disabled(len(m.connectionDiagnostics) == 0).Clicked() {
				c.WriteClipboard(m.connectionDiagnosticsText())
				m.connectionDiagnosticsCopied = true
			}
			if ui.Button(c, "关闭").Label("关闭连接诊断").Grow(1).MinWidth(0).Height(44).Clicked() {
				m.connectionDiagnosticsOpen = false
			}
		})
	})
}
