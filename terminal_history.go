package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"retty/internal/terminal"
)

// History is a separate, read-only snapshot. Live viewport replacements never
// erase the history the user is reading or cause a PTY resize.
type terminalHistory struct {
	open, busy bool
	err        string
	rows       int
	client     *rex.Client
	sid        string
	term       *terminal.Terminal
	cancel     context.CancelFunc
	epoch      uint64
	dispatch   func(func())
}

type historyConn struct {
	done chan struct{}
	once sync.Once
}

func (c *historyConn) Read([]byte) (int, error)    { <-c.done; return 0, io.EOF }
func (c *historyConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *historyConn) Close() error                { c.once.Do(func() { close(c.done) }); return nil }

func (h *terminalHistory) close() {
	h.open = false
	h.epoch++
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	if h.term != nil {
		h.term.Close()
		h.term = nil
	}
}

func (h *terminalHistory) load(rows int) {
	if h.client == nil || h.busy {
		return
	}
	h.epoch++
	epoch := h.epoch
	h.busy, h.err = true, ""
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	h.cancel = cancel
	client, sid := h.client, h.sid
	go func() {
		data, err := client.History(ctx, sid, rows)
		cancel()
		h.dispatch(func() {
			if !h.open || h.epoch != epoch {
				return
			}
			h.busy, h.cancel = false, nil
			if err != nil {
				switch {
				case strings.Contains(err.Error(), "unknown op"):
					h.err = "请更新电脑端会话服务后查看独立历史"
				case errors.Is(err, context.DeadlineExceeded):
					h.err = "读取历史超时，请检查连接后重试"
				default:
					h.err = "无法读取历史，请检查连接后重试"
				}
				return
			}
			conn := &historyConn{done: make(chan struct{})}
			next, err := terminal.New(terminal.Options{Conn: conn, FixedCols: data.Cols, FixedRows: data.Rows, ReflowView: true, Font: termFont, Theme: lightTerm, DarkTheme: darkTerm, AdaptiveColors: true, CopyRawText: prefs.CopyRawText})
			if err != nil {
				conn.Close()
				h.err = "无法显示历史快照"
				return
			}
			next.SetInputEnabled(false)
			next.Resize(data.Cols, data.Rows)
			next.Feed(data.Data)
			if h.term != nil {
				h.term.Close()
			}
			h.term, h.rows = next, rows
		})
	}()
}

func (h *terminalHistory) view(c *ui.Context) {
	if h == nil || !h.open {
		return
	}
	k := colorsOf(c)
	ui.DialogBase(c.Key("terminal-history"), &h.open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		w, height := c.Size()
		panel.Column().Width(min(1000, w-24)).Height(height - 48).Radius(12).Background(k.panel).Clip().Children(func() {
			ui.Row(c).Padding(10, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("历史快照 · 最多 %d 行", max(h.rows, 512))).FontSize(13).Grow(1).SingleLine().Ellipsis("…")
				if ui.Button(c, "加载更多").Disabled(h.busy || h.rows >= 8192).Clicked() {
					h.load(min(max(h.rows, 512)*4, 8192))
				}
				if ui.Button(c, "关闭").Clicked() {
					h.open = false
				}
			})
			if h.busy {
				ui.Text(c, "正在读取历史…").FontSize(12).Padding(0, 12, 8, 12)
			}
			if h.err != "" {
				ui.Text(c, h.err).FontSize(12).TextColor(c.Theme().Danger).Padding(0, 12, 8, 12)
			}
			if h.term != nil {
				terminal.View(c, h.term).Key("history-terminal").Fill().TextCaretFunc(nil).Label("历史终端")
			} else {
				ui.Spacer(c).Grow(1)
			}
		})
	})
	if !h.open {
		h.close()
	}
}

func (a *App) openTerminalHistory() {
	t := a.tab()
	if t == nil || t.Focus == nil || t.Focus.terminal() == nil || t.Focus.terminal().SID == "" {
		return
	}
	if a.terminalHistory != nil {
		a.terminalHistory.close()
	}
	h := &terminalHistory{open: true, client: a.paneClient(t.Focus), sid: t.Focus.terminal().SID, dispatch: a.desktopDispatcher()}
	a.terminalHistory = h
	h.load(512)
}

func (m *mobileApp) openTerminalHistory() {
	if m.client == nil || m.selected.ID == "" {
		return
	}
	if m.terminalHistory != nil {
		m.terminalHistory.close()
	}
	h := &terminalHistory{open: true, client: m.client, sid: m.selected.ID, dispatch: func(fn func()) { mygo.RunOnMain(func() { fn(); m.invalidate() }) }}
	m.terminalHistory = h
	h.load(512)
}
