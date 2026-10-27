package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/remote"
	"retty/internal/rex"
)

// A host owns its transport; tabs never borrow another computer's client.
type desktopHost struct {
	retryTimer       *time.Timer
	retryAttempt     int
	quality          *remote.Quality
	qualityProbeBusy bool
	qualityProbeAt   time.Time
	recoverySince    time.Time
	layout           savedLayout
	key, link        string
	hello            rex.Hello
	client           *rex.Client
	tunnel           func()
	cancel           context.CancelFunc
	generation       int
	busy, creating   bool
	err              string
	sessions         []rex.SessionInfo
	recent           desktopRecent
	pendingSession   string
}
type desktopConnections struct {
	historyLoaded, dirty bool
	hosts                []*desktopHost
	store                connectionRecordStore
	open, showLocal      bool
	input, err           string
	selected             *desktopHost
	qualityHost          *desktopHost
	historySelection     map[string]bool
	recentSessions       []mobileRecentSession
	incoming             []remote.ConnectedDevice

	// written holds the records last written, by key, so that saving
	// skips those unchanged; only the storage goroutine uses it.
	written map[string]string
}

func (a *App) hasDesktopConnections() bool {
	for _, h := range a.desktops.hosts {
		if h.connected() {
			return true
		}
	}
	if a.phone != nil && a.phone.bridge != nil {
		bridge := a.phone.bridge
		if len(bridge.Devices()) > 0 {
			return true
		}
		for _, d := range a.desktops.incoming {
			if bridge.DeviceConnected(d.ID) {
				return true
			}
		}
	}
	return false
}

func (h *desktopHost) name() string {
	if h.hello.Host.Name != "" {
		return h.hello.Host.Name
	}
	if h.recent.Name != "" {
		return h.recent.Name
	}
	return "远端电脑"
}
func (h *desktopHost) connected() bool {
	return remote.ConnectionUsable(h.client) && !h.busy && h.err == ""
}
func (a *App) currentHost() *desktopHost {
	if t := a.tab(); t != nil {
		return t.Host
	}
	return nil
}
func (a *App) paneClient(p *Pane) *rex.Client {
	if p.host != nil {
		return p.host.client
	}
	return a.client
}
func (a *App) hostHello(h *desktopHost) rex.Hello {
	if h != nil {
		return h.hello
	}
	return a.hello
}
func (p *Pane) noticeKey() string {
	if p.terminal() == nil {
		return ""
	}
	if p.host != nil {
		return p.host.key + "/" + p.terminal().SID
	}
	return p.terminal().SID
}
func noticeSessionID(key string) string {
	_, sid, ok := strings.Cut(key, "/")
	if ok {
		return sid
	}
	return key
}
func noticeOnHost(key string, h *desktopHost) bool {
	if h == nil {
		return !strings.Contains(key, "/")
	}
	return strings.HasPrefix(key, h.key+"/")
}
func (a *App) markPaneClosed(p *Pane) {
	if p.host == nil {
		a.markSessionClosed(p.terminal().SID)
	} else {
		a.closeAgentNotice(p.noticeKey())
	}
}

func (a *App) desktopDispatcher() func(func()) {
	win := a.win
	return func(fn func()) {
		a.post(fn)
		if win != nil {
			win.Update(a.runPosted)
		}
	}
}

// stop ends the host's connection attempt, retry and watcher. Replies of
// the old generation are then ignored.
func (h *desktopHost) stop() {
	h.generation++
	if h.retryTimer != nil {
		h.retryTimer.Stop()
		h.retryTimer = nil
	}
	if h.cancel != nil {
		h.cancel()
	}
}
func desktopLayoutKey(h *desktopHost) string {
	if h.recent.ID != "" {
		return h.recent.ID
	}
	return h.key
}
func (a *App) desktopInputEnabled(h *desktopHost, enabled bool) {
	for p := range a.hostPanes(h) {
		if p.terminalView() != nil {
			p.terminal().term.SetInputEnabled(enabled && (p.terminal().remoteView == nil || a.activeRemote == p && !p.terminal().remoteYielded && p.terminal().remoteView.inputReady()))
		}
	}
}
func (a *App) flushDesktopHistory() {
	if a.desktopStorage == nil {
		return
	}
	done := make(chan struct{})
	a.desktopStorage <- func() { close(done) }
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}
func (a *App) loadDesktopHistory() {
	a.desktops = desktopConnections{store: newDesktopConnectionStore()}
	if a.desktopStorage == nil {
		a.desktopStorage = connectionStorage()
	}
	store, win, update := a.desktops.store, a.win, a.desktopDispatcher()
	a.desktopStorage <- func() {
		history := readDesktopHistory(store)
		recentSessions := loadRecord[[]mobileRecentSession](store, "recent-sessions")
		incoming := loadRecord[[]remote.ConnectedDevice](store, "incoming-devices")
		layouts := loadRecord[map[string]savedLayout](store, "desktop-layouts")
		update(func() {
			if a.win != win || a.quitting {
				return
			}
			a.desktops.historyLoaded = true
			a.desktops.recentSessions = mergeRecentSessions(a.desktops.recentSessions, recentSessions)
			a.desktops.incoming = mergeIncomingDevices(a.desktops.incoming, incoming)
			for _, entry := range history {
				h, err := a.rememberDesktop(entry.Link)
				if err != nil {
					continue
				}
				h.recent = entry
				h.layout = layouts[desktopLayoutKey(h)]
				if len(h.layout.Tabs) > 0 {
					a.connectDesktop(h)
				}
			}
		})
	}
}
func (a *App) saveDesktopHistory() {
	if a.desktops.store == nil || a.desktopStorage == nil || !a.desktops.historyLoaded {
		return
	}
	var history []desktopRecent
	layouts := map[string]savedLayout{}
	for _, h := range a.desktops.hosts {
		history = append(history, h.recent)
		layout := a.snapshotHost(h)
		if len(layout.Tabs) == 0 && len(h.layout.Tabs) > 0 {
			layout = h.layout
		}
		layouts[desktopLayoutKey(h)] = layout
	}
	// Every change of the layout saves, and each write syncs the store to
	// disk: write only the records that changed since the last write.
	records := map[string]any{
		"history":          mergeDesktopHistory(history, nil),
		"desktop-layouts":  layouts,
		"recent-sessions":  a.desktops.recentSessions,
		"incoming-devices": a.desktops.incoming,
	}
	data := map[string][]byte{}
	for key, record := range records {
		data[key], _ = json.Marshal(record)
	}
	if a.desktops.written == nil {
		a.desktops.written = map[string]string{}
	}
	store, written, update := a.desktops.store, a.desktops.written, a.desktopDispatcher()
	a.desktopStorage <- func() {
		for _, key := range []string{"history", "desktop-layouts", "recent-sessions", "incoming-devices"} {
			if written[key] == string(data[key]) {
				continue
			}
			if err := store.Set(key, data[key]); err != nil {
				update(func() { a.desktops.err = "无法保存最近连接，请重试。" })
				return
			}
			written[key] = string(data[key])
		}
	}
}

func (a *App) rememberDesktop(raw string) (*desktopHost, error) {
	addr, err := remote.ParseLink(raw)
	if err != nil {
		return nil, err
	}
	link := remote.Link(addr)
	for _, h := range a.desktops.hosts {
		if h.link == link {
			return h, nil
		}
	}
	sum := sha256.Sum256([]byte(addr))
	h := &desktopHost{key: hex.EncodeToString(sum[:12]), link: link, recent: desktopRecent{Link: link, Name: "远端电脑"}}
	a.desktops.hosts = append(a.desktops.hosts, h)
	return h, nil
}
func (a *App) submitDesktopConnection() {
	h, err := a.rememberDesktop(a.desktops.input)
	if err != nil {
		a.desktops.err = err.Error()
		return
	}
	a.desktops.input, a.desktops.err = "", ""
	a.desktops.selected = h
	a.connectDesktop(h)
}
func (a *App) connectDesktop(h *desktopHost) {
	if h.busy || h.connected() {
		return
	}
	h.stop()
	remote.CloseConnection(h.client, h.tunnel)
	h.client, h.tunnel = nil, nil
	h.qualityProbeBusy = false
	h.qualityProbeAt = time.Time{}
	generation := h.generation
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.busy, h.err = cancel, true, ""
	a.desktopInputEnabled(h, false)
	update := a.desktopDispatcher()
	link := h.link
	if h.quality == nil {
		h.quality = &remote.Quality{}
	}
	quality := h.quality
	device := mygo.App.Device()
	deviceName := device.Name
	if deviceName == "" {
		deviceName = a.hello.Host.Name
	}
	go func() {
		deadline, stop := context.WithTimeout(ctx, 45*time.Second)
		defer stop()
		client, tunnel, hello, infos, err := remote.OpenConnection(deadline, link, rex.DeviceInfo{Name: deviceName, OS: device.System}, nil, quality)
		update(func() {
			if h.generation != generation || a.quitting {
				remote.CloseConnection(client, tunnel)
				return
			}
			h.busy = false
			if err != nil {
				remote.CloseConnection(client, tunnel)
				a.failDesktop(h, err, h.retryAttempt > 0, update)
				return
			}
			if hello.Host.ID != "" && hello.Host.ID == a.hello.Host.ID {
				remote.CloseConnection(client, tunnel)
				h.err = "这是此电脑，请使用本地标签页。"
				return
			}
			// A newly copied capability can belong to a computer already remembered.
			for _, other := range a.desktops.hosts {
				if other != h && hello.Host.ID != "" && sameDesktop(other.recent, desktopRecent{ID: hello.Host.ID, Name: hello.Host.Name}) {
					a.desktops.selected = other
					other.link, other.recent.Link = h.link, h.link
					remote.CloseConnection(client, tunnel)
					a.desktops.hosts = slices.DeleteFunc(a.desktops.hosts, func(v *desktopHost) bool { return v == h })
					if !other.connected() {
						a.connectDesktop(other)
					}
					a.saveDesktopHistory()
					return
				}
			}
			h.client, h.tunnel, h.hello, h.sessions = client, tunnel, hello, liveDesktopSessions(infos)
			h.retryAttempt = 0
			a.finishDesktopRecovery(h)
			a.desktops.hosts = slices.Insert(slices.DeleteFunc(a.desktops.hosts, func(other *desktopHost) bool { return other == h }), 0, h)
			h.recent = desktopRecent{Link: h.link, Name: h.name(), ID: hello.Host.ID, OS: desktopPlatform(hello.Host)}
			a.reattachDesktopTabs(h)

			a.restoreDesktopLayout(h)
			a.saveDesktopHistory()
			a.finishDesktopPendingSession(h)
			go a.pollDesktop(ctx, h, client, generation, update)
		})
	}()
}
func liveDesktopSessions(infos []rex.SessionInfo) []rex.SessionInfo {
	out := slices.DeleteFunc(slices.Clone(infos), func(in rex.SessionInfo) bool { return in.Exited })
	slices.SortFunc(out, func(x, y rex.SessionInfo) int { return x.Created.Compare(y.Created) })
	return out
}
func desktopSession(h *desktopHost, sid string) (rex.SessionInfo, bool) {
	for _, in := range h.sessions {
		if in.ID == sid {
			return in, true
		}
	}
	return rex.SessionInfo{}, false
}
func (a *App) pollDesktop(ctx context.Context, h *desktopHost, client *rex.Client, generation int, update func(func())) {
	remote.WatchConnection(ctx, client, nil, func(infos []rex.SessionInfo, err error) {
		update(func() {
			if ctx.Err() != nil || h.generation != generation || a.quitting {
				return
			}
			if err != nil {
				a.recoverDesktop(ctx, h, client, generation, update)
				return
			}
			h.sessions = liveDesktopSessions(infos)
			byID := map[string]rex.SessionInfo{}
			for _, in := range infos {
				byID[in.ID] = in
			}
			a.applyHost(h, byID)
		})
	}, h.quality)
}
func (a *App) recoverDesktop(ctx context.Context, h *desktopHost, client *rex.Client, generation int, update func(func())) {
	if h.recoverySince.IsZero() {
		h.recoverySince = time.Now()
		if h.quality != nil {
			h.quality.RecoveryStarted(h.recoverySince)
		}
	}
	h.busy, h.creating = true, false
	h.err = "正在恢复连接，远端会话继续运行。"
	a.desktopInputEnabled(h, false)
	go func() {
		check, cancel := context.WithTimeout(ctx, remote.ResumeRecoveryTimeout)
		next, hello, infos, err := remote.ResumeConnection(check, client, nil)
		cancel()
		update(func() {
			if h.generation != generation || a.quitting {
				if next != nil && next != client {
					next.Close()
				}
				return
			}
			h.busy = false
			if err != nil {
				a.failDesktop(h, err, true, update)
				return
			}
			h.err = ""
			a.finishDesktopRecovery(h)
			if next != client {
				client.Close()
				h.client = next
				h.hello = hello
			}
			h.sessions = liveDesktopSessions(infos)
			a.reattachDesktopTabs(h)
			a.finishDesktopPendingSession(h)
			go a.pollDesktop(ctx, h, h.client, generation, update)
		})
	}()
}

// failDesktop tells why a host could not connect, and retries when the
// failure may pass and retry allows.
func (a *App) failDesktop(h *desktopHost, err error, retry bool, update func(func())) {
	issue := connectionIssueFor(err)
	h.err = issue.body
	if retry && issue.automatic {
		a.scheduleDesktopRetry(h, update)
	}
}
func (a *App) scheduleDesktopRetry(h *desktopHost, update func(func())) {
	generation := h.generation
	delay := remote.RetryDelay(h.retryAttempt)
	h.retryAttempt++
	if h.retryTimer != nil {
		h.retryTimer.Stop()
	}
	h.retryTimer = time.AfterFunc(delay, func() {
		update(func() {
			if h.generation == generation && !a.quitting {
				h.retryTimer = nil
				a.connectDesktop(h)
			}
		})
	})
}
func (a *App) reattachDesktopTabs(h *desktopHost) {
	for p := range a.hostPanes(h) {
		if in, ok := desktopSession(h, p.terminal().SID); ok {
			a.reattachDesktopPane(p, in)
		} else {
			p.terminal().info.Exited = true
		}
	}
}

func (a *App) reattachDesktopPane(p *Pane, in rex.SessionInfo) {
	if p.terminal().remoteView != nil {
		p.terminal().info = in
		a.pauseRemotePane(p)
		p.terminal().remoteView.replace(nil)
		if a.activeRemote == p {
			a.resumeRemotePane(p)
		}
		return
	}
	p.closed = true
	if p.terminalView() != nil {
		a.closePaneTerminal(p)
	}
	p.closed, p.terminal().info, p.terminal().term = false, in, nil
	a.attach(p, in.Cols, in.Rows)
}

func (a *App) disconnectDesktop(h *desktopHost) {
	h.qualityProbeBusy = false
	h.pendingSession = ""
	h.stop()
	h.retryAttempt = 0
	h.recoverySince = time.Time{}
	h.busy, h.creating = false, false
	h.err = ""
	for _, t := range slices.Clone(a.tabs) {
		if t.Host == h {
			a.closeTab(t)
		}
	}
	h.layout = savedLayout{}
	a.closeReleasedDesktopTransport(h)
	h.client, h.tunnel = nil, nil
	a.saveDesktopHistory()
}
func (a *App) closeDesktopConnections() {
	for _, h := range a.desktops.hosts {
		h.stop()
		remote.CloseConnection(h.client, h.tunnel)
		h.client, h.tunnel = nil, nil
	}
}
func (a *App) openDesktopSession(h *desktopHost, in rex.SessionInfo, selectIt bool) {
	if !h.connected() || in.Exited {
		return
	}
	if selectIt {
		p := &Pane{content: &terminalPane{info: in, startDir: in.Dir}}
		name, _ := p.label()
		a.desktops.recentSessions = mergeRecentSessions([]mobileRecentSession{{Desktop: h.key, Session: in.ID, Title: name, Program: sessionProgramName(in)}}, a.desktops.recentSessions)
		a.saveDesktopHistory()
	}
	for p := range a.hostPanes(h) {
		if p.terminal().SID == in.ID {
			if selectIt {
				p.Tab.setFocus(p)
				a.selectTab(slices.Index(a.tabs, p.Tab))
				a.finishDesktopDialog()
			}
			return
		}
	}
	p := &Pane{ID: a.id(), host: h, content: &terminalPane{SID: in.ID, info: in, startDir: in.Dir}}
	a.attach(p, in.Cols, in.Rows)
	t := a.singlePaneTab(p)
	at := len(a.tabs)
	if selectIt {
		at = min(a.active+1, len(a.tabs))
	}
	a.tabs = slices.Insert(a.tabs, at, t)
	if selectIt {
		a.selectTab(at)
		a.finishDesktopDialog()
	}
	a.changed()
}

// Network creates run outside the UI thread. A late response cannot replace
// another tab or resurrect a tab closed while the request was in flight.
func (a *App) createDesktopSession(h *desktopHost, dir string, split *Pane, vertical bool) {
	if !h.connected() {
		a.showDesktopConnections(h)
		return
	}
	if h.creating {
		return
	}
	if dir == "" || dir == "~" {
		dir = h.hello.Host.Home
	} else if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		dir = path.Join(h.hello.Host.Home, rest)
	}
	h.creating = true
	client, generation := h.client, h.generation
	update := a.desktopDispatcher()
	cols, rows := 80, 24
	if split != nil && split.terminalView() != nil {
		cols, rows = split.terminal().term.Size()
		if vertical {
			rows = max(rows/2, 2)
		} else {
			cols = max(cols/2, 10)
		}
	}
	go func() {
		in, err := client.Create(rex.CreateOptions{Dir: dir, Cols: cols, Rows: rows})
		update(func() {
			if generation != h.generation || a.quitting {
				return
			}
			h.creating = false
			if err != nil {
				a.desktops.err = "无法新建会话，请检查远端连接。"
				a.showDesktopConnections(h)
				return
			}
			h.sessions = append(h.sessions, in)
			if split == nil {
				a.openDesktopSession(h, in, true)
				return
			}
			if split.closed || split.Tab.Host != h || !slices.Contains(a.tabs, split.Tab) {
				return
			}
			t, n := split.Tab, split.Node
			p := &Pane{ID: a.id(), host: h, Tab: t, content: &terminalPane{SID: in.ID, info: in, startDir: in.Dir}}
			left, right := &Node{ID: a.id(), Pane: split, Parent: n}, &Node{ID: a.id(), Pane: p, Parent: n}
			split.Node, p.Node = left, right
			n.Pane, n.A, n.B, n.Vertical, n.Ratio = nil, left, right, vertical, .5
			t.Zoom = nil
			a.attach(p, in.Cols, in.Rows)
			t.setFocus(p)
			a.selectTab(slices.Index(a.tabs, t))
			a.changed()
		})
	}()
}
func (a *App) restartDesktopPane(p *Pane) {
	// Ending a remote process is explicit; closing a remote tab only detaches.
	h, client, generation := p.host, p.host.client, p.host.generation
	if !h.connected() || h.creating {
		return
	}
	h.creating = true
	update := a.desktopDispatcher()
	cols, rows := p.terminal().info.Cols, p.terminal().info.Rows
	dir := p.terminal().info.Dir
	go func() {
		in, err := client.Create(rex.CreateOptions{Dir: dir, Cols: cols, Rows: rows})
		update(func() {
			if generation != h.generation || a.quitting {
				return
			}
			h.creating = false
			if err != nil {
				a.desktops.err = "无法重新启动会话。"
				a.showDesktopConnections(h)
				return
			}
			if p.closed {
				return
			}
			replacement := &Pane{ID: a.id(), host: h, content: &terminalPane{SID: in.ID, info: in, startDir: dir}}
			a.endPane(p, true)
			go client.Kill(p.terminal().SID)
			a.attach(replacement, in.Cols, in.Rows)
			a.replacePane(p, replacement)
		})
	}()
}
func (a *App) showDesktopConnections(h *desktopHost) {
	a.desktops.qualityHost = nil
	a.settingsOpen, a.paletteOpen, a.renaming = false, false, nil
	a.desktops.open, a.desktops.showLocal = true, false
	a.desktops.historySelection = nil
	a.desktops.selected = h
	a.focusReq = nil
}
func (a *App) finishDesktopDialog() {
	a.desktops.qualityHost = nil
	a.desktops.open = false
	if t := a.tab(); t != nil {
		a.focusReq = t.Focus
	}
}

func (a *App) finishDesktopPendingSession(h *desktopHost) {
	if sid := h.pendingSession; sid != "" {
		h.pendingSession = ""
		if a.desktops.open && a.desktops.selected == h {
			a.openDesktopRecentSession(h, sid)
		}
	}
}
func (a *App) openDesktopRecentSession(h *desktopHost, sid string) {
	if in, ok := desktopSession(h, sid); ok {
		a.openDesktopSession(h, in, true)
	} else {
		a.desktops.err = "原会话已结束，请选择其他会话。"
	}
}

func (a *App) removeDesktopHistory(selection map[string]bool) {
	removed := map[string]bool{}
	for _, h := range slices.Clone(a.desktops.hosts) {
		if !selection[h.key] {
			continue
		}
		removed[h.key] = true
		a.disconnectDesktop(h)
		if a.desktops.selected == h {
			a.desktops.selected = nil
		}
	}
	a.desktops.hosts = slices.DeleteFunc(a.desktops.hosts, func(h *desktopHost) bool { return removed[h.key] })
	a.desktops.recentSessions = slices.DeleteFunc(a.desktops.recentSessions, func(s mobileRecentSession) bool { return removed[s.Desktop] })
	a.desktops.historySelection = nil
	a.saveDesktopHistory()
}

// Restore remote splits locally, using only sessions the remote server still
// has. A missing session is never replaced with a new shell during restore.
func (a *App) restoreDesktopLayout(h *desktopHost) {
	saved := h.layout
	h.layout = savedLayout{}
	shown := map[string]bool{}
	for p := range a.hostPanes(h) {
		shown[p.terminal().SID] = true
	}
	var load func(*savedNode, *Tab, *Node) *Node
	load = func(s *savedNode, t *Tab, parent *Node) *Node {
		if s == nil {
			return nil
		}
		n := &Node{ID: a.id(), Parent: parent}
		if s.A == nil || s.B == nil {
			if p := a.restorePaneContent(s, h); p != nil {
				p.Tab, p.Node = t, n
				n.Pane = p
				return n
			}
			in, ok := desktopSession(h, s.SID)
			if !ok || shown[s.SID] {
				return nil
			}
			shown[s.SID] = true
			p := &Pane{ID: a.id(), host: h, Tab: t, Node: n, Key: s.PaneID, content: &terminalPane{SID: in.ID, info: in, startDir: in.Dir}}
			n.Pane = p
			a.attach(p, in.Cols, in.Rows)
			return n
		}
		n.A, n.B = load(s.A, t, n), load(s.B, t, n)
		if n.A == nil {
			if n.B != nil {
				n.B.Parent = parent
			}
			return n.B
		}
		if n.B == nil {
			n.A.Parent = parent
			return n.A
		}
		n.Vertical, n.Ratio = s.Vertical, min(max(s.Ratio, .05), .95)
		return n
	}
	for _, savedTab := range saved.Tabs {
		t := &Tab{ID: a.id(), Host: h, Name: savedTab.Name}
		t.Root = load(savedTab.Root, t, nil)
		if t.Root == nil {
			continue
		}
		panes := t.panes()
		t.setFocus(panes[min(max(savedTab.Focus, 0), len(panes)-1)])
		if savedTab.Zoom && len(panes) > 1 {
			t.Zoom = t.Focus
		}
		a.tabs = append(a.tabs, t)
	}
	a.changed()
}
