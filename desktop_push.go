package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/egoist/mygo"
	"os"
	"retty/internal/push"
	"retty/internal/remote"
	"retty/internal/rex"
	"runtime"
	"time"
)

const desktopAwayAfter = 5 * time.Minute

type desktopPushPresence struct {
	id               string
	sequence         uint64
	locked, sleeping bool // Main-thread state, including system power events.
	changes          chan push.DesktopActivity
	remoteBusy       map[*rex.Client]bool
}

func desktopUserPresent(locked, sleeping bool, idle time.Duration) bool {
	return !locked && !sleeping && idle < desktopAwayAfter
}

func (a *App) desktopPushActivity() push.DesktopActivity {
	p := a.pushPresence
	p.sequence++
	present := desktopUserPresent(p.locked, p.sleeping, mygo.Power.IdleTime())
	activity := push.DesktopActivity{ID: p.id, Sequence: p.sequence, RoutingVersion: 1, Present: present, Active: present, HideWaiting: prefs.HideDesktopNotifications || prefs.HideAgentNotifications, HideCompletion: prefs.HideDesktopNotifications || prefs.HideAgentCompletionNotifications}
	// Active also carries presence to older workers, whose foreground lease
	// is their only way to suppress APNs. New workers use the exact pane.
	if a.win.IsFocused() && !a.win.IsMinimized() && present && (a.terminalHistory == nil || !a.terminalHistory.open) {
		if t := a.tab(); t != nil && t.Focus != nil && t.Focus.terminal() != nil && !t.Focus.closed {
			activity.ViewedDesktop, activity.ViewedSession = a.hostHello(t.Host).Host.ID, t.Focus.terminal().SID
		}
	}
	return activity
}

func (a *App) reportDesktopPushPresence() {
	if a.pushPresence == nil || a.quitting {
		return
	}
	activity := a.desktopPushActivity()
	select {
	case <-a.pushPresence.changes:
	default:
	}
	a.pushPresence.changes <- activity
	a.reportRemotePushPresence(activity)
}

func (a *App) reportRemotePushPresence(activity push.DesktopActivity) {
	presence := a.pushPresence
	for _, h := range a.desktops.hosts {
		if !h.connected() || presence.remoteBusy[h.client] {
			continue
		}
		client := h.client
		presence.remoteBusy[client] = true
		info := rex.DeviceInfo{ID: a.hello.Host.ID, Name: a.hello.Host.Name, OS: a.hello.Host.OS, Activity: &activity}
		win := a.win
		go func() {
			_, _ = client.HelloFrom(info)
			win.Update(func() { delete(presence.remoteBusy, client) })
		}()
	}
}

// Power events and system-wide input idle time distinguish a person at the
// computer from an app merely left open. IPC runs outside the UI thread.
func startDesktopPushPresence(a *App) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	win := a.win
	a.pushPresence = &desktopPushPresence{id: fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()), changes: make(chan push.DesktopActivity, 1), remoteBusy: map[*rex.Client]bool{}}
	done := make(chan struct{})
	win.OnFocus(a.reportDesktopPushPresence)
	win.OnBlur(a.reportDesktopPushPresence)
	offs := []func(){
		mygo.Power.OnLockScreen(func() { a.pushPresence.locked = true; a.reportDesktopPushPresence() }),
		mygo.Power.OnUnlockScreen(func() { a.pushPresence.locked = false; a.reportDesktopPushPresence() }),
		mygo.Power.OnSuspend(func() { a.pushPresence.sleeping = true; a.reportDesktopPushPresence() }),
		mygo.Power.OnResume(func() { a.pushPresence.sleeping = false; a.reportDesktopPushPresence() }),
	}
	win.OnClosed(func() {
		for _, off := range offs {
			off()
		}
		activity := a.desktopPushActivity()
		activity.Active, activity.Present = false, false
		activity.ViewedDesktop, activity.ViewedSession = "", ""
		go push.ReportDesktop(rex.Dir(), activity)
		a.reportRemotePushPresence(activity)
		close(done)
	})
	a.reportDesktopPushPresence()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case activity := <-a.pushPresence.changes:
				_ = push.ReportDesktop(rex.Dir(), activity)
			case <-ticker.C:
				win.Update(func() { a.reportDesktopPushPresence() })
			}
		}
	}()
}

func (a *App) paneNoticeID(p *Pane) string {
	info := p.terminal().info
	h := a.hostHello(p.host)
	if h.Version < 5 {
		info.Agent.CompletionRevision = 0
	}
	return rex.AgentNoticeID(h.Host.ID, info)
}

func (a *App) routePaneNotice(p *Pane, kind string, opts mygo.NotificationOptions) {
	activity := a.desktopPushActivity()
	id := a.paneNoticeID(p)
	if kind == "terminal" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", id, opts.Body, time.Now().UnixNano())))
		id = fmt.Sprintf("retty-agent-%x", sum[:16])
	}
	n := push.Notice{ID: id, Desktop: a.hostHello(p.host).Host.ID, Session: p.terminal().SID, Kind: kind, Title: opts.Title, Body: opts.Body}
	n.Viewed = activity.Present && activity.ViewedDesktop == n.Desktop && activity.ViewedSession == n.Session
	win := a.win
	var client *rex.Client
	if p.host != nil {
		client = p.host.client
	}
	go func() {
		var route push.Route
		var err error
		if client != nil {
			route, err = remote.ClaimNotification(context.Background(), client, activity, n)
			if errors.Is(err, push.ErrLegacyRouting) {
				// Older source bridges cannot arbitrate. Keep their APNs owner
				// unchanged and show desktop reminders only while present.
				err = nil
				route = push.RouteQuiet
				if activity.Present && !n.Viewed {
					route = push.RouteDesktop
				}
			}
		} else {
			route, err = claimDesktopNotice(rex.Dir(), activity, n)
		}
		win.Update(func() {
			if a.quitting || p.closed || a.win != win || kind != "terminal" && a.paneNoticeID(p) != n.ID {
				return
			}
			if err != nil {
				a.agentHookError = "无法确定通知接收端，请检查任务通知服务。"
				return
			}
			if route == push.RouteDesktop {
				a.showDesktopPaneNotice(p, kind, opts)
			}
		})
	}()
}

func claimDesktopNotice(dir string, activity push.DesktopActivity, n push.Notice) (push.Route, error) {
	route, err := push.Claim(dir, activity, n)
	if !errors.Is(err, push.ErrLegacyRouting) {
		return route, err
	}
	// Do not restart a live old worker. Its coarse lease can still carry
	// presence until a safe worker update enables persistent channel claims.
	if err = push.ReportDesktop(dir, activity); err != nil {
		return push.RouteQuiet, err
	}
	status, err := push.Query(dir)
	if err != nil {
		return push.RouteQuiet, err
	}
	if n.Viewed {
		return push.RouteQuiet, nil
	}
	if !activity.Present && status.Configured && status.Devices > 0 {
		return push.RoutePhone, nil
	}
	return push.RouteDesktop, nil
}
