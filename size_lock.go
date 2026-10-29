package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"retty/internal/rex"
)

// followSizeLock shows a session whose size a phone holds at the phone's
// size: one PTY has one size, and the program draws for the phone. The
// pane follows that size until the phone leaves or a window unlocks it.
func (a *App) followSizeLock(p *Pane, was rex.SessionInfo) {
	in := p.terminal().info
	if p.host != nil {
		return
	} // Remote viewers follow framed source dimensions.
	// A list made before an unlock completed still names the lock.
	if p.terminalView() == nil || p.terminal().unlocking || time.Since(p.terminal().unlockedAt) < 2*time.Second {
		return
	}
	switch {
	case in.SizeLock != "" && (was.SizeLock != in.SizeLock || was.Cols != in.Cols || was.Rows != in.Rows || !p.terminal().locked):
		first := !p.terminal().locked
		p.terminal().locked = true
		p.terminal().term.SetFixedSize(in.Cols, in.Rows)
		if first {
			// What the phone's program drew reached this pane before the
			// list did. Replace it with the session's screen at its size.
			sid, client := p.terminal().SID, a.paneClient(p)
			go client.Resync(sid)
		}
	case in.SizeLock == "" && p.terminal().locked:
		// The phone became inactive, or another window released it.
		p.terminal().locked = false
		if p.terminal().stream != nil {
			p.terminal().stream.ResendSize()
		}
		p.terminal().term.SetFixedSize(0, 0)
	}
}

// unlockSize gives the session's size back to the desktop: the pane sizes
// it to its view again, and the phone reflows the desktop's screen.
func (a *App) unlockSize(p *Pane) {
	if !p.terminal().locked || p.terminal().unlocking {
		return
	}
	p.terminal().unlocking = true
	// The window is the main thread's to read, as it closes there.
	sid, client, update := p.terminal().SID, a.paneClient(p), a.desktopDispatcher()
	go func() {
		err := client.UnlockSize(sid, 0, 0)
		update(func() {
			p.terminal().unlocking = false
			if err != nil {
				a.err = err.Error()
				return
			}
			p.terminal().locked, p.terminal().unlockedAt = false, time.Now()
			p.terminal().info.SizeLock, p.terminal().info.SizeLockDevice = "", ""
			// The view's next frame resizes the session to the pane.
			if p.terminal().stream != nil {
				p.terminal().stream.ResendSize()
			}
			p.terminal().term.SetFixedSize(0, 0)
		})
	}()
}

// sizeLockBar tells that a phone holds the pane's size, with the unlock.
func (a *App) sizeLockBar(c *ui.Context, k *colors, p *Pane) {
	device := p.terminal().info.SizeLockDevice
	if device == "" {
		device = "iPhone"
	}
	ui.Row(c.Key("size-lock")).Height(32).Padding(0, 8+paneActionInset(p), 0, 12).Gap(spaceM).
		AlignItems(ui.Center).Background(k.panel).BorderWidth(0, 0, 1, 0).BorderColor(k.panelBorder).
		Children(func() {
			ui.Text(c, fmt.Sprintf("%s 正在使用此会话（%d×%d），窗格尺寸已锁定", device, p.terminal().info.Cols, p.terminal().info.Rows)).
				FontSize(textSmall).TextColor(k.textMuted).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if ui.Button(c, "解锁").Disabled(p.terminal().unlocking).Label("Unlock Size").Clicked() {
				a.unlockSize(p)
			}
		})
}
