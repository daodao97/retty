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
	if t := a.tab(); a.focusedWin && t != nil && t.Host != nil && t.Focus != nil && t.Focus.browser == nil {
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
	if desired != nil && desired.term != nil && desired.remoteView != nil {
		ready := desired.remoteView.inputReady() && !desired.remoteYielded && desired.host.connected()
		if !ready {
			desired.cancelFileUpload()
		}
		desired.term.SetInputEnabled(ready)
	}
}

// Only the explicit takeover button may reclaim another device's session.
func (a *App) activateRemotePane(p *Pane) {
	if p == nil || p.closed || p.host == nil || !a.focusedWin {
		return
	}
	a.pauseRemotePane(p)
	p.remoteYielded = false
	a.startRemotePane(p, true)
}

func (a *App) resumeRemotePane(p *Pane) { a.startRemotePane(p, false) }

const remotePaneKeepAlive = 60 * time.Second
const remoteLoadingDelay = 400 * time.Millisecond

func (a *App) startRemotePane(p *Pane, takeover bool) {
	if p.remoteView == nil || p.closed || p.info.Exited || !p.host.connected() || !a.focusedWin {
		return
	}
	if p.remoteIdleTimer != nil {
		p.remoteIdleTimer.Stop()
		p.remoteIdleTimer = nil
	}
	p.remoteIdleEpoch++
	if !p.remoteView.hasTransport() {
		if p.streamEnded && p.host.quality != nil {
			p.host.quality.ScreenRecovered(time.Now())
		}
		reader := a.paneClient(p).ViewStream(p.SID).PreferScreenSync(false)
		reader.OnData = func(int) { p.lastData.Store(time.Now().UnixNano()) }
		p.remoteView.geometry(p.info.Cols, p.info.Rows)
		p.remoteView.replace(reader)
		p.remoteLoadingSince = time.Now()
		// Attach off the UI thread, without the stream's initial resize delay.
		go reader.Resize(0, 0)
	}
	p.streamEnded = false
	if p.stream != nil || p.remoteAttaching || p.remoteYielded && !takeover {
		return
	}
	p.remoteAttempt++
	attempt, client, generation := p.remoteAttempt, a.paneClient(p), p.host.generation
	p.remoteAttaching = true
	update := a.desktopDispatcher()
	go func() {
		// A fresh LIST avoids reclaiming a phone's ownership using an old poll.
		infos, err := client.List()
		update(func() {
			if p.closed || a.quitting || p.remoteAttempt != attempt || p.host.generation != generation || a.paneClient(p) != client {
				return
			}
			p.remoteAttaching = false
			if a.activeRemote != p || !a.focusedWin || !p.host.connected() {
				return
			}
			if err != nil {
				p.streamEnded = true
				return
			}
			var found bool
			for _, info := range infos {
				if info.ID != p.SID {
					continue
				}
				found = !info.Exited
				if !takeover && info.SizeLock != "" && info.SizeLock != p.remoteOwner {
					p.remoteYielded = true
					return
				}
			}
			if !found {
				return
			}
			p.remoteOwner, p.remoteSeen, p.remoteYielded = newSizeLockOwner(), false, false
			cols, rows := p.term.Size()
			var stream *rex.Stream
			if a.hostHello(p.host).Version >= 6 {
				stream = client.LockStreamAfter(p.SID, p.remoteOwner, a.hello.Host.Name, cols, rows, p.remoteRelease)
			} else {
				stream = client.Stream(p.SID, cols, rows)
			}
			p.stream = stream
			stream.PreferScreenSync(true)
			owner, locked := p.remoteOwner, a.hostHello(p.host).Version >= 6
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
						if info.ID == p.SID && !info.Exited && info.SizeLock == owner {
							owned = true
						}
					}
				}
				update(func() {
					if p.closed || a.quitting || p.stream != stream {
						return
					}
					if err != nil {
						a.pauseRemotePane(p)
						p.streamEnded = true
						return
					}
					if !owned {
						p.remoteYielded = true
						a.pauseRemotePane(p)
						return
					}
					if a.activeRemote == p && a.focusedWin && !p.remoteYielded {
						p.remoteView.setInput(stream)
						// Layout may have changed while the ownership handshake ran.
						c, r := p.term.Size()
						p.remoteView.geometry(c, r)
						stream.Resize(c, r)
					}
				})
				if err == nil {
					io.Copy(io.Discard, stream)
				}
				update(func() {
					if !p.closed && !a.quitting && p.stream == stream {
						a.pauseRemotePane(p)
						p.streamEnded = true
					}
				})
			}()
		})
	}()
}

// Disable input and release size immediately; keep the read-only stream warm.
func (a *App) pauseRemotePane(p *Pane) {
	p.cancelFileUpload()
	if p.remoteView == nil {
		return
	}
	p.remoteAttempt++
	p.remoteAttaching = false
	p.remoteView.setInput(nil)
	if p.stream != nil {
		p.remoteRelease = p.stream.CloseAndReleaseSize()
		a.retainRemoteSizeRelease(p.host, p.remoteRelease)
		p.stream = nil
	}
	if p.term != nil {
		p.term.SetInputEnabled(false)
	}
	if p.remoteIdleTimer != nil {
		p.remoteIdleTimer.Stop()
		p.remoteIdleTimer = nil
	}
	p.remoteIdleEpoch++
	if p.remoteView.hasTransport() {
		reader := p.remoteView.transport()
		epoch := p.remoteIdleEpoch
		update := a.desktopDispatcher()
		p.remoteIdleTimer = time.AfterFunc(remotePaneKeepAlive, func() {
			update(func() {
				if p.remoteIdleEpoch == epoch {
					a.expireRemotePane(p, reader)
				}
			})
		})
	}
}

func (a *App) expireRemotePane(p *Pane, reader sessionTransport) {
	p.remoteIdleTimer = nil
	if p.closed || a.activeRemote == p || p.remoteView.transport() != reader {
		return
	}
	p.remoteView.replace(nil)
	p.streamEnded = false
}

func (a *App) closePaneTerminal(p *Pane) {
	a.pauseRemotePane(p)
	if p.remoteIdleTimer != nil {
		p.remoteIdleTimer.Stop()
		p.remoteIdleTimer = nil
	}
	if p.remoteView != nil {
		p.remoteView.Close()
	}
	if p.term != nil {
		p.term.Close()
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
	if p.remoteView == nil || !p.remoteView.hasTransport() || p.stream == nil || p.remoteOwner == "" || a.hostHello(p.host).Version < 6 {
		return
	}
	if p.info.SizeLock == p.remoteOwner {
		p.remoteSeen = true
		return
	}
	holds, since := p.stream.HoldsLock()
	// LIST may have been issued before this attachment acquired its lock.
	if !p.remoteSeen && (!holds || time.Since(since) < 3*time.Second) {
		return
	}
	p.remoteYielded = true
	a.pauseRemotePane(p)
}
