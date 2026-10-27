package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func TestTerminalHistoryIsReadonlyAndPreservesSession(t *testing.T) {
	a, tt := newTestApp(t)
	tab := a.newLocalCommandTab(t.TempDir(), []string{"/bin/sh", "-c", "printf HISTORY_FIXTURE; sleep 20"})
	p := tab.Focus
	tt.Frame()
	waitFor(t, tt, "owned history fixture", func() bool { return bytes.Contains(p.terminal().term.Snapshot(), []byte("HISTORY_FIXTURE")) })
	before, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	a.openTerminalHistory()
	h := a.terminalHistory
	t.Cleanup(h.close)
	waitFor(t, tt, "independent history snapshot", func() bool { return h.term != nil || h.err != "" })
	if h.err != "" || !tt.HasText("历史快照 · 最多 512 行") || !bytes.Contains(h.term.Snapshot(), []byte("HISTORY_FIXTURE")) {
		t.Fatalf("history not rendered: %s", h.err)
	}
	saveSettingsImage(t, tt, "terminal-history-desktop")
	phone := ui.NewTester(func(c *ui.Context) { h.view(c) }, 390, 844)
	phone.Frame()
	saveSettingsImage(t, phone, "terminal-history-mobile")
	h.term.Send([]byte("MUST_NOT_REACH_PTY\n"))
	after, err := a.client.List()
	if err != nil {
		t.Fatal(err)
	}
	var beforeInfo, afterInfo *rex.SessionInfo
	for i := range before {
		if before[i].ID == p.terminal().SID {
			beforeInfo = &before[i]
		}
	}
	for i := range after {
		if after[i].ID == p.terminal().SID {
			afterInfo = &after[i]
		}
	}
	if beforeInfo == nil || afterInfo == nil || beforeInfo.PID != afterInfo.PID || beforeInfo.Cols != afterInfo.Cols || beforeInfo.Rows != afterInfo.Rows || beforeInfo.Attached != afterInfo.Attached || !beforeInfo.LastInput.Equal(afterInfo.LastInput) {
		t.Fatal("history read changed PTY, attachment, size or input")
	}
	if a.paneIsViewed(p) {
		t.Fatal("history snapshot marked live result viewed")
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if h.open || h.term != nil {
		t.Fatal("history close leaked its terminal")
	}
}

type freshnessTransport struct {
	sessionTransport
	enabled   bool
	confirmed time.Time
}

func (t *freshnessTransport) ScreenStatus() (bool, time.Time, uint64) {
	return t.enabled, t.confirmed, 0
}

func TestScreenFreshnessWarnsOnlyForUnconfirmedSyncViews(t *testing.T) {
	source := &freshnessTransport{enabled: true, confirmed: time.Now().Add(-8 * time.Second)}
	stream := newSessionViewStream(source, nil)
	tt := ui.NewTester(func(c *ui.Context) { ui.Text(c, screenFreshness(c, stream)) }, 400, 100)
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), " "), "未确认") {
		t.Fatal("stale frame was presented as current")
	}
	source.confirmed = time.Now()
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), " "), "未确认") {
		t.Fatal("heartbeat did not clear stale warning")
	}
	source.enabled = false
	source.confirmed = time.Now().Add(-time.Hour)
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), " "), "未确认") {
		t.Fatal("legacy quiet view was labelled stale")
	}
}

func TestTerminalHistoryCloseCommandsKeepLivePane(t *testing.T) {
	for _, closeTab := range []bool{false, true} {
		a, _ := newStaticTestApp(t)
		original := a.tab()
		a.terminalHistory = &terminalHistory{open: true}
		if closeTab {
			a.closeActiveTab()
		} else {
			a.closeFocused()
		}
		if a.terminalHistory.open || a.tab() != original || original.Focus.closed {
			t.Fatal("history close ended its live pane")
		}
	}
}
