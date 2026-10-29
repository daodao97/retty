package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/rex"
)

type desktopPresenceState uint8

const (
	desktopChecking desktopPresenceState = iota
	desktopOnline
	desktopUnavailable
)

func (s desktopPresenceState) label() string {
	switch s {
	case desktopOnline:
		return "在线"
	case desktopUnavailable:
		return "不可达"
	default:
		return "检查中"
	}
}

type desktopPresence struct {
	state    desktopPresenceState
	checked  time.Time
	failures int // consecutive, to back off probing a desktop that is off
}

const recentPresenceInterval = 30 * time.Second
const maxPresenceInterval = 5 * time.Minute

// presenceProbeTimeout bounds one probe, tunnel included.
const presenceProbeTimeout = 12 * time.Second

// Each probe of an unreachable desktop builds a whole tunnel only to time
// out; probe it less often the longer it stays away, up to five minutes.
func (p desktopPresence) interval() time.Duration {
	return min(recentPresenceInterval<<min(max(p.failures-1, 0), 4), maxPresenceInterval)
}

func (m *mobileApp) stopRecentPresence() {
	if m.presenceCancel != nil {
		m.presenceCancel()
		m.presenceCancel = nil
	}
	m.presenceEpoch++ // Late results cannot overwrite a new connection or history.
}

// Probes run only while the foreground connection page is visible. Keep cached
// results during refresh, and probe at most two desktops at a time. A desktop
// that answers keeps its tunnel parked, so the next probe or a tap reuses it.
func (m *mobileApp) refreshRecentPresence(c *ui.Context) {
	if m.win == nil || m.background || m.client != nil || m.busy || m.scanning || len(m.history) == 0 || m.presenceCancel != nil {
		return
	}
	if m.presence == nil {
		m.presence = make(map[string]desktopPresence)
	}
	wait := recentPresenceInterval
	var links []string
	for _, entry := range m.history {
		result := m.presence[entry.Link]
		remaining := result.interval() - c.Now().Sub(result.checked)
		if result.checked.IsZero() || remaining <= 0 {
			links = append(links, entry.Link)
		} else {
			wait = min(wait, remaining)
		}
	}
	if len(links) == 0 {
		c.After(wait)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.presenceCancel = cancel
	m.presenceEpoch++
	epoch := m.presenceEpoch
	go func() {
		defer cancel()
		var workers sync.WaitGroup
		limit := make(chan struct{}, 2)
		for _, link := range links {
			workers.Go(func() {
				select {
				case limit <- struct{}{}:
					defer func() { <-limit }()
				case <-ctx.Done():
					return
				}
				probeCtx, stop := context.WithTimeout(ctx, presenceProbeTimeout)
				trace := remote.NewPreviewDiagnostics(presenceProbeTimeout)
				hello, sessions, err := remote.Preview(probeCtx, link, trace)
				stop()
				if ctx.Err() != nil {
					return
				}
				// Only the log keeps a probe's report: one every 30 seconds would
				// push the connections out of the diagnostics the user can copy.
				log.Printf("%s", trace.Finish(err).Text)
				result := desktopPresence{state: desktopOnline, checked: time.Now()}
				mygo.RunOnMain(func() {
					if m.presenceEpoch == epoch {
						if err != nil {
							result.state = desktopUnavailable
							result.failures = m.presence[link].failures + 1
						}
						m.presence[link] = result
						if err == nil {
							m.applyRecentDesktopMetadata(link, hello, sessions)
						}
						m.invalidate()
					}
				})
			})
		}
		workers.Wait()
		mygo.RunOnMain(func() {
			if m.presenceEpoch == epoch {
				m.presenceCancel = nil
				m.invalidate()
			}
		})
	}()
}

// Refresh cached icons even when the user stays on the home page. Previewing
// another desktop must not replace the active connection or its session list.
func (m *mobileApp) applyRecentDesktopMetadata(link string, hello rex.Hello, sessions []rex.SessionInfo) {
	var desktop desktopRecent
	for i, entry := range m.history {
		if entry.Link != link {
			continue
		}
		platform := desktopPlatform(hello.Host)
		if platform != "" && entry.OS != platform {
			m.history[i].OS = platform
			m.persistDesktopHistory()
		}
		desktop = m.history[i]
		break
	}
	if desktop.Link == "" {
		return
	}
	changed := false
	for i, entry := range m.recentSessions {
		if entry.Desktop != desktop.Link && (desktop.ID == "" || entry.Desktop != desktop.ID) {
			continue
		}
		for _, session := range sessions {
			if session.ID != entry.Session || session.Exited {
				continue
			}
			entry.Program, entry.Title = sessionProgramName(session), mobileSessionTitle(session)
			if entry != m.recentSessions[i] {
				m.recentSessions[i] = entry
				changed = true
			}
			break
		}
	}
	if changed {
		m.persistRecentSessions()
	}
}

func (m *mobileApp) persistDesktopHistory() {
	saved, _ := json.Marshal(m.history)
	epoch := m.historyEpoch
	m.save("history", saved, func() {
		if m.historyEpoch == epoch {
			m.error = "无法保存连接记录，请重试"
			m.invalidate()
		}
	})
}
