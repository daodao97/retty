package main

import (
	"io"
	"retty/internal/remote"
	"retty/internal/rex"
	"time"
)

// Focus changes acquire ownership once. Polls and redraws never reclaim a
// session another device took, preventing two foreground apps from fighting.
func (a *App) syncRemotePaneActivity() {
	var desired *Pane
	if t := a.tab(); a.focusedWin && t != nil && t.Host != nil && t.Focus != nil && t.Focus.terminal() != nil {
		desired = t.Focus
	}
	if desired != a.activeRemote {
		if a.activeRemote != nil {
			a.pauseRemotePane(a.activeRemote)
		}
		a.activeRemote = desired
		if desired != nil {
			a.resumeRemotePane(desired)
		}
	}
	if desired != nil && desired.terminalView() != nil && desired.terminal().remoteView != nil {
		ready := desired.terminal().remoteView.inputReady() && !desired.terminal().remoteYielded && desired.host.connected()
		if !ready {
			desired.cancelFileUpload()
		}
		desired.terminal().term.SetInputEnabled(ready)
	}
}

// Only the explicit takeover button may reclaim another device's session.
func (a *App) activateRemotePane(p *Pane) {
	if p == nil || p.terminal() == nil || p.closed || p.host == nil || !a.focusedWin {
		return
	}
	a.pauseRemotePane(p)
	p.terminal().remoteYielded = false
	a.startRemotePane(p, true)
}

func (a *App) resumeRemotePane(p *Pane) { a.startRemotePane(p, false) }

const remotePaneKeepAlive = 60 * time.Second
const remoteLoadingDelay = 400 * time.Millisecond

func (a *App) startRemotePane(p *Pane, takeover bool) {
	if p.terminal().remoteView == nil || p.closed || p.terminal().info.Exited || !p.host.connected() || !a.focusedWin {
		return
	}
	if p.terminal().remoteIdleTimer != nil {
		p.terminal().remoteIdleTimer.Stop()
		p.terminal().remoteIdleTimer = nil
	}
	p.terminal().remoteIdleEpoch++
	if !p.terminal().remoteView.hasTransport() {
		if p.terminal().streamEnded && p.host.quality != nil {
			p.host.quality.ScreenRecovered(time.Now())
		}
		reader := a.paneClient(p).ViewStream(p.terminal().SID).PreferScreenSync(false)
		reader.OnData = func(int) { p.terminal().lastData.Store(time.Now().UnixNano()) }
		p.terminal().remoteView.geometry(p.terminal().info.Cols, p.terminal().info.Rows)
		p.terminal().remoteView.replace(reader)
		p.terminal().remoteLoadingSince = time.Now()
		// Attach off the UI thread, without the stream's initial resize delay.
		go reader.Resize(0, 0)
	}
	p.terminal().streamEnded = false
	if p.terminal().stream != nil || p.terminal().remoteAttaching || p.terminal().remoteYielded && !takeover {
		return
	}
	p.terminal().remoteAttempt++
	attempt, client, generation := p.terminal().remoteAttempt, a.paneClient(p), p.host.generation
	p.terminal().remoteAttaching = true
	update := a.desktopDispatcher()
	go func() {
		// A fresh LIST avoids reclaiming a phone's ownership using an old poll.
		infos, err := client.List()
		update(func() {
			if p.closed || a.quitting || p.terminal().remoteAttempt != attempt || p.host.generation != generation || a.paneClient(p) != client {
				return
			}
			p.terminal().remoteAttaching = false
			if a.activeRemote != p || !a.focusedWin || !p.host.connected() {
				return
			}
			if err != nil {
				p.terminal().streamEnded = true
				return
			}
			var found bool
			for _, info := range infos {
				if info.ID != p.terminal().SID {
					continue
				}
				found = !info.Exited
				if !takeover && info.SizeLock != "" && info.SizeLock != p.terminal().remoteOwner {
					p.terminal().remoteYielded = true
					return
				}
			}
			if !found {
				return
			}
			p.terminal().remoteOwner, p.terminal().remoteSeen, p.terminal().remoteYielded = newSizeLockOwner(), false, false
			cols, rows := p.terminal().term.Size()
			var stream *rex.Stream
			if a.hostHello(p.host).Version >= 6 {
				stream = client.LockStreamAfter(p.terminal().SID, p.terminal().remoteOwner, a.hello.Host.Name, cols, rows, p.terminal().remoteRelease)
			} else {
				stream = client.Stream(p.terminal().SID, cols, rows)
			}
			p.terminal().stream = stream
			stream.PreferScreenSync(true)
			owner, locked := p.terminal().remoteOwner, a.hostHello(p.host).Version >= 6
			go func() {
				err := stream.Resize(cols, rows)
				if err == nil {
					_, _, _, err = stream.ReadSnapshot()
				}
				owned := !locked
				if err == nil && locked {
					var current []rex.SessionInfo
					current, err = client.List()
					for _, info := range current {
						if info.ID == p.terminal().SID && !info.Exited && info.SizeLock == owner {
							owned = true
						}
					}
				}
				update(func() {
					if p.closed || a.quitting || p.terminal().stream != stream {
						return
					}
					if err != nil {
						a.pauseRemotePane(p)
						p.terminal().streamEnded = true
						return
					}
					if !owned {
						p.terminal().remoteYielded = true
						a.pauseRemotePane(p)
						return
					}
					if a.activeRemote == p && a.focusedWin && !p.terminal().remoteYielded {
						p.terminal().remoteView.setInput(stream)
						// Layout may have changed while the ownership handshake ran.
						c, r := p.terminal().term.Size()
						p.terminal().remoteView.geometry(c, r)
						stream.Resize(c, r)
					}
				})
				if err == nil {
					io.Copy(io.Discard, stream)
				}
				update(func() {
					if !p.closed && !a.quitting && p.terminal().stream == stream {
						a.pauseRemotePane(p)
						p.terminal().streamEnded = true
					}
				})
			}()
		})
	}()
}

// Disable input and release size immediately; keep the read-only stream warm.
func (a *App) pauseRemotePane(p *Pane) {
	p.cancelFileUpload()
	if p.terminal().remoteView == nil {
		return
	}
	p.terminal().remoteAttempt++
	p.terminal().remoteAttaching = false
	p.terminal().remoteView.setInput(nil)
	if p.terminal().stream != nil {
		p.terminal().remoteRelease = p.terminal().stream.CloseAndReleaseSize()
		a.retainRemoteSizeRelease(p.host, p.terminal().remoteRelease)
		p.terminal().stream = nil
	}
	if p.terminalView() != nil {
		p.terminal().term.SetInputEnabled(false)
	}
	if p.terminal().remoteIdleTimer != nil {
		p.terminal().remoteIdleTimer.Stop()
		p.terminal().remoteIdleTimer = nil
	}
	p.terminal().remoteIdleEpoch++
	if p.terminal().remoteView.hasTransport() {
		reader := p.terminal().remoteView.transport()
		epoch := p.terminal().remoteIdleEpoch
		update := a.desktopDispatcher()
		p.terminal().remoteIdleTimer = time.AfterFunc(remotePaneKeepAlive, func() {
			update(func() {
				if p.terminal().remoteIdleEpoch == epoch {
					a.expireRemotePane(p, reader)
				}
			})
		})
	}
}

func (a *App) expireRemotePane(p *Pane, reader sessionTransport) {
	p.terminal().remoteIdleTimer = nil
	if p.closed || a.activeRemote == p || p.terminal().remoteView.transport() != reader {
		return
	}
	p.terminal().remoteView.replace(nil)
	p.terminal().streamEnded = false
}

func (a *App) closePaneTerminal(p *Pane) {
	if p.terminal() == nil {
		return
	}
	a.pauseRemotePane(p)
	if p.terminal().remoteIdleTimer != nil {
		p.terminal().remoteIdleTimer.Stop()
		p.terminal().remoteIdleTimer = nil
	}
	if p.terminal().remoteView != nil {
		p.terminal().remoteView.Close()
	}
	if p.terminalView() != nil {
		p.terminal().term.Close()
	}
}

func (a *App) waitRemoteSizeReleases() {
	deadline := time.NewTimer(remoteSizeReleaseTimeout)
	defer deadline.Stop()
	for _, release := range a.remoteReleases {
		select {
		case <-release.done:
		case <-deadline.C:
			return
		}
	}
}

const remoteSizeReleaseTimeout = 30 * time.Second

type remoteSizeRelease struct {
	host *desktopHost
	done <-chan struct{}
}

func (a *App) retainRemoteSizeRelease(h *desktopHost, done <-chan struct{}) {
	pending := a.remoteReleases[:0]
	for _, release := range a.remoteReleases {
		select {
		case <-release.done:
		default:
			pending = append(pending, release)
		}
	}
	a.remoteReleases = append(pending, remoteSizeRelease{h, done})
}

// Do not close the shared control channel before the detached panes have
// released their ownership, including panes already removed from the layout.
func (a *App) closeReleasedDesktopTransport(h *desktopHost) {
	var releases []<-chan struct{}
	for _, release := range a.remoteReleases {
		if release.host == h {
			releases = append(releases, release.done)
		}
	}
	client, tunnel := h.client, h.tunnel
	go func() {
		deadline := time.NewTimer(remoteSizeReleaseTimeout)
		defer deadline.Stop()
		for _, done := range releases {
			select {
			case <-done:
			case <-deadline.C:
				remote.CloseConnection(client, tunnel)
				return
			}
		}
		remote.CloseConnection(client, tunnel)
	}()
}

func (a *App) followRemoteSizeOwner(p *Pane) {
	if p.terminal().remoteView == nil || !p.terminal().remoteView.hasTransport() || p.terminal().stream == nil || p.terminal().remoteOwner == "" || a.hostHello(p.host).Version < 6 {
		return
	}
	if p.terminal().info.SizeLock == p.terminal().remoteOwner {
		p.terminal().remoteSeen = true
		return
	}
	holds, since := p.terminal().stream.HoldsLock()
	// LIST may have been issued before this attachment acquired its lock.
	if !p.terminal().remoteSeen && (!holds || time.Since(since) < 3*time.Second) {
		return
	}
	p.terminal().remoteYielded = true
	a.pauseRemotePane(p)
}
