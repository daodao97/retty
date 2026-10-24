package main

import (
	"cmp"
	"context"
	"encoding/json"
	"iter"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/terminal"

	"retty/internal/agents"
	"retty/internal/desktopcontrol"
	"retty/internal/diagnostics"
	"retty/internal/files"
	"retty/internal/rex"
)

// Tab is a tab of the window: panes in a tree of splits.
type Tab struct {
	Host *desktopHost // nil is this computer; all splits share the same host
	ID   int
	Name string // the name the user gave it, "" to follow its panes
	Root *Node
	// Focus is the pane with the focus, and Zoom the pane that fills the
	// tab, when one does.
	Focus *Pane
	Zoom  *Pane
	// recent lists the panes by when they had the focus last, the latest
	// last: closing a pane gives the focus back to the one before.
	recent []*Pane
}

// Node is a pane, or a split of two nodes.
type Node struct {
	ID     int
	Pane   *Pane
	Parent *Node
	// Vertical stacks A above B; otherwise A is left of B. Ratio is A's
	// share of the room.
	Vertical bool
	Ratio    float32
	A, B     *Node
}

// Pane is a terminal attached to a session of the server.
type Pane struct {
	browser                   *browserPane
	host                      *desktopHost
	ID                        int
	SID                       string
	Tab                       *Tab
	Node                      *Node
	term                      *terminal.Terminal
	stream                    *rex.Stream
	remoteView                *sessionViewStream
	remoteOwner               string
	remoteSeen, remoteYielded bool
	remoteRelease             <-chan struct{}
	remoteIdleTimer           *time.Timer
	remoteIdleEpoch           uint64
	remoteAttempt             uint64
	remoteAttaching           bool
	remoteLoadingSince        time.Time
	upload                    *paneUpload

	// info is what the server last said of the session.
	info rex.SessionInfo
	// title is the last title the program set.
	title string
	// lastData is when the session last printed, in Unix nanoseconds.
	lastData atomic.Int64
	// attention marks a pane that rang the bell or whose program finished
	// while it was out of sight; seen clears it.
	attention bool
	// bounds is where the pane's card was in the last frame.
	bounds      ui.Rect
	closed      bool
	streamEnded bool
	// restored tells that the session was made anew for a pane of a
	// saved layout whose session was gone.
	restored      bool
	recoverySID   string
	recoveryError string
	recoveryBusy  bool
	startDir      string
	find          paneFind
	// locked tells that a phone holds the session's size, which the pane
	// shows at that size until unlocked; unlocking, that it is asked to.
	locked, unlocking bool
	unlockedAt        time.Time
}

// App is the state of Retty's window.
type App struct {
	cliInstaller     *desktopCLIInstaller
	diagnostics      *desktopDiagnostics
	diagnosticRead   func(context.Context, *rex.Client, diagnostics.Request) (diagnostics.Report, error)
	browserFactory   func(*Pane) (browserView, error) // isolated UI fixtures
	control          *desktopcontrol.Server
	gitStatus        *desktopGitState
	gitRead          func(context.Context, *rex.Client, string) (files.Result, error) // isolated fixtures
	agentHistory     *sidebarAgentHistory
	remoteUpdate     *remoteUpdateDialog
	desktopUpdater   *desktopUpdateService
	terminalHistory  *terminalHistory
	files            *desktopFiles
	filesWidthSaveAt time.Time
	readReceipts     readReceiptState
	pollSnapshots    map[*desktopHost]map[string]rex.SessionInfo
	desktops         desktopConnections
	desktopStorage   chan func()
	phone            *phonePair
	openLinkURL      func(string) error // injectable native URL opener
	win              *mygo.Window
	appearanceItems  map[string]*mygo.MenuItem
	client           *rex.Client
	hello            rex.Hello
	err              string

	tabs              []*Tab
	active            int
	nextID            int
	sidebarVisibleTab *Tab // scroll selection into view only when it changes

	// focusReq asks the view to give the keyboard focus to a pane's
	// terminal.
	focusReq *Pane

	// The command palette, settings and the tab being renamed.
	paletteOpen           bool
	paletteQuery          string
	paletteSSHHosts       []string
	paletteSSHLoading     bool
	paletteSSHError       string
	paletteSSHEpoch       uint64
	settingsOpen          bool
	settingsSection       int
	settingsQuery         string
	settingsModifiedOnly  bool
	settingsInitialFocus  bool
	renaming              *Tab
	renameText            string
	renameFocus           bool
	saveDue               bool
	lastSave              time.Time
	quitting              bool
	shuttingDown          func() bool // nil uses the OS detector
	focusedWin            bool
	activeRemote          *Pane
	remoteReleases        []remoteSizeRelease
	lastSnapshot          string
	title                 string
	agentHooks            map[string]agents.HookInstallation
	agentHookBusy         string
	agentHookError        string
	agentNotified         map[string]uint64
	agentFinishedNotified map[string]uint64
	programNoticeLimiter  rex.ProgramNoticeLimiter
	pushPresence          *desktopPushPresence
	agentNotices          map[string]agentNotice
	agentNotify           func(mygo.NotificationOptions, func()) func()
	openWindow            func()
	closedSessions        map[string]bool

	// posted are changes to make in the next frame, from terminals of a
	// view without a window, as in tests.
	postMu sync.Mutex
	posted []func()
	// services are the window's, for menus and terminal callbacks that run
	// outside a build pass.
	services ui.Services
}

func (a *App) post(fn func()) {
	a.postMu.Lock()
	a.posted = append(a.posted, fn)
	a.postMu.Unlock()
}

// later changes the tree of tabs and panes once the frame building now
// is built, as closing what it is building would pull it from under it.
func (a *App) later(c *ui.Context, fn func()) { a.laterFrom(c.Services(), fn) }

func (a *App) laterFrom(s ui.Services, fn func()) {
	a.post(fn)
	s.Invalidate()
}

// runPosted makes the changes posted since the last frame.
func (a *App) runPosted() {
	a.postMu.Lock()
	fns := a.posted
	a.posted = nil
	a.postMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (a *App) id() int {
	a.nextID++
	return a.nextID
}

func (a *App) tab() *Tab {
	if a.active >= 0 && a.active < len(a.tabs) {
		return a.tabs[a.active]
	}
	return nil
}

// panes returns the panes of a node in order, left to right and top to
// bottom.
func (n *Node) panes() []*Pane {
	if n == nil {
		return nil
	}
	if n.Pane != nil {
		return []*Pane{n.Pane}
	}
	return append(n.A.panes(), n.B.panes()...)
}

func (t *Tab) panes() []*Pane { return t.Root.panes() }

// setFocus gives a pane the focus of its tab.
func (t *Tab) setFocus(p *Pane) {
	if p == nil {
		return
	}
	t.Focus = p
	p.attention = false
	t.recent = slices.DeleteFunc(t.recent, func(q *Pane) bool { return q == p })
	t.recent = append(t.recent, p)
}

// newPane makes a pane with a new session in dir.
func (a *App) newPane(dir string, cols, rows int) *Pane {
	return a.newCommandPane(dir, nil, cols, rows)
}

func (a *App) newCommandPane(dir string, command []string, cols, rows int) *Pane {
	return a.newOptionsPane(rex.CreateOptions{Dir: dir, Command: command, Cols: cols, Rows: rows})
}

func (a *App) newOptionsPane(options rex.CreateOptions) *Pane {
	dir, cols, rows := options.Dir, options.Cols, options.Rows
	if cols <= 0 {
		cols, rows = 80, 24
	}
	options.Cols, options.Rows = cols, rows
	info, err := a.client.Create(options)
	if err != nil {
		log.Printf("creating a session: %v", err)
		a.err = err.Error()
		info = rex.SessionInfo{ID: "", Exited: true}
	}
	p := &Pane{ID: a.id(), SID: info.ID, info: info, startDir: dir}
	if p.info.Dir == "" {
		p.info.Dir = dir
	}
	a.attach(p, cols, rows)
	return p
}

// attach makes the pane's terminal, attached to its session.
func (a *App) attach(p *Pane, cols, rows int) {
	win := a.win
	onSplit := func(down bool) {
		a.laterFrom(a.services, func() { a.splitPane(p, down) })
	}
	options := terminal.Options{
		Conn:           nopConn{},
		Font:           termFont,
		Theme:          lightTerm,
		DarkTheme:      darkTerm,
		AdaptiveColors: true,
		Transparent:    true,
		CopyRawText:    prefs.CopyRawText,
		OnSplit:        onSplit,
		OnPaste:        func(s ui.Services) bool { return a.pastePaneClipboard(p, s) },
		OnOpenLink:     func(link terminal.Link) { a.openTerminalLink(p, link) },
		OnPreviewLink:  func(link terminal.Link) *terminal.LinkPreviewSource { return a.terminalImagePreview(p, link) },
	}
	if p.SID == "" {
		term, err := terminal.New(options)
		if err == nil {
			message := "Could not start a session: " + a.err
			if p.recoveryError != "" {
				message = "无法恢复 Agent 会话：" + p.recoveryError
			}
			if cols <= 0 {
				cols = 80
			}
			if rows <= 0 {
				rows = 24
			}
			term.Resize(cols, rows)
			term.Feed([]byte("\x1b[31m" + message + "\x1b[0m\r\n"))
			p.term = term
		}
		return
	}
	if p.host != nil {
		var view *sessionViewStream
		view = newSessionViewStream(nil, func() {
			a.post(func() {
				if !p.closed && !a.quitting && p.remoteView == view && !view.hasTransport() {
					a.pauseRemotePane(p)
					p.streamEnded = true
				}
			})
			if win != nil {
				win.Invalidate()
			}
		})
		p.remoteView = view
		view.setInput(nil)
	} else {
		p.stream = a.paneClient(p).Stream(p.SID, cols, rows)
	}
	stream := p.stream
	p.streamEnded = false
	if p.stream != nil {
		p.stream.OnData = func(int) { p.lastData.Store(time.Now().UnixNano()) }
	}
	// The terminal's callbacks change the state on the main thread, in
	// the window that made the pane, as long as it is open.
	update := func(fn func()) {
		if win != nil {
			win.Update(fn)
		} else {
			a.post(fn)
		}
	}
	options.Conn = p.stream
	if p.host != nil {
		// Only the active pane attaches and supplies its own viewport size.
		// Reuse the phone's retained-reader and atomic snapshot replacement.
		options.Conn = p.remoteView
	}
	options.OnRenderEvent = func(event terminal.RenderEvent) { logRenderEvent(p.SID, event) }
	options.OnTitle = func(title string) {
		update(func() { p.title = title })
	}
	options.OnBell = func() {
		update(func() { a.ring(p) })
	}
	options.OnNotify = func(title, body string) {
		update(func() { a.showTerminalNotice(p, title, body) })
	}
	options.OnExit = func(int) {
		update(func() {
			if p.closed || a.quitting || p.stream != stream || a.systemShuttingDown() {
				return
			}
			if p.host == nil {
				if p.info.Resumed {
					a.checkAgentRestoreExit(p, stream)
				} else {
					a.closePane(p)
				}
			} else {
				p.streamEnded = true
			}
		})
	}
	term, err := terminal.New(options)
	if err != nil {
		log.Printf("terminal: %v", err)
		a.err = err.Error()
		return
	}
	// Until a view shows it, the terminal has the session's size, so that
	// attaching changes nothing in the session.
	term.Resize(cols, rows)
	p.term = term
}

// nopConn is the Conn of a terminal that only shows what the app feeds it.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)    { select {} }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }

func (a *App) ring(p *Pane) {
	if t := a.tab(); t == nil || t.Focus != p || p.Tab != t || !a.focusedWin {
		p.attention = true
	}
}

// newTab opens a tab with a shell in dir, after the active tab.
func (a *App) newTab(dir string) *Tab {
	if h := a.currentHost(); h != nil {
		a.createDesktopSession(h, dir, nil, false)
		return nil
	}
	return a.newLocalTab(dir)
}

func (a *App) newLocalTab(dir string) *Tab {
	return a.newLocalCommandTab(dir, nil)
}

func (a *App) newLocalCommandTab(dir string, command []string) *Tab {
	return a.newLocalOptionsTab(rex.CreateOptions{Dir: dir, Command: command})
}

func (a *App) newLocalOptionsTab(options rex.CreateOptions) *Tab {
	p := a.newOptionsPane(options)
	t := a.singlePaneTab(p)
	at := min(a.active+1, len(a.tabs))
	a.tabs = slices.Insert(a.tabs, at, t)
	a.active = at
	a.focusReq = p
	if prefs.FilesOpen {
		a.toggleFiles()
	}
	a.changed()
	return t
}

// currentDir returns the directory new panes open in: the focused pane's.
func (a *App) currentDir() string {
	if t := a.tab(); t != nil && t.Focus != nil {
		if d := t.Focus.info.Dir; d != "" {
			return d
		}
	}
	if h := a.currentHost(); h != nil {
		return h.hello.Host.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// splitPane targets the context menu's pane, even if another pane has focus.
// A menu retained after its pane or tab closes must not split another pane.
func (a *App) splitPane(p *Pane, down bool) {
	if p == nil || p.closed || p.Node == nil || p.Node.Pane != p {
		return
	}
	for i, t := range a.tabs {
		if t == p.Tab {
			a.selectTab(i)
			t.setFocus(p)
			a.split(down)
			return
		}
	}
}

// split splits the focused pane, the new pane right of it or below it.
func (a *App) split(vertical bool) {
	t := a.tab()
	if t == nil || t.Focus == nil {
		return
	}
	old := t.Focus
	if old.browser != nil {
		if _, err := a.openBrowser(old, "about:blank", old.host, vertical); err != nil {
			a.err = err.Error()
		}
		return
	}
	if t.Host != nil {
		a.createDesktopSession(t.Host, a.currentDir(), old, vertical)
		return
	}
	t.Zoom = nil
	n := old.Node
	cols, rows := 80, 24
	if old.term != nil {
		cols, rows = old.term.Size()
		if vertical {
			rows = max(rows/2, 2)
		} else {
			cols = max(cols/2, 10)
		}
	}
	p := a.newPane(a.currentDir(), cols, rows)
	p.Tab = t
	left := &Node{ID: a.id(), Pane: old, Parent: n}
	right := &Node{ID: a.id(), Pane: p, Parent: n}
	old.Node, p.Node = left, right
	n.Pane, n.A, n.B, n.Vertical, n.Ratio = nil, left, right, vertical, 0.5
	t.setFocus(p)
	a.focusReq = p
	a.changed()
}

// singlePaneTab makes a tab of one pane, which has the focus.
func (a *App) singlePaneTab(p *Pane) *Tab {
	t := &Tab{ID: a.id(), Host: p.host}
	t.Root = &Node{ID: a.id(), Pane: p}
	p.Tab, p.Node = t, t.Root
	t.setFocus(p)
	return t
}

// allPanes yields every pane of every tab, with its tab.
func (a *App) allPanes() iter.Seq2[*Tab, *Pane] {
	return func(yield func(*Tab, *Pane) bool) {
		for _, t := range a.tabs {
			for _, p := range t.panes() {
				if !yield(t, p) {
					return
				}
			}
		}
	}
}

// hostPanes yields the panes of the tabs of a computer, nil for this one.
func (a *App) hostPanes(h *desktopHost) iter.Seq[*Pane] {
	return func(yield func(*Pane) bool) {
		for t, p := range a.allPanes() {
			if t.Host == h && p.browser == nil && !yield(p) {
				return
			}
		}
	}
}

// endPane closes a pane's terminal. With end, the pane's session ends too:
// its notice closes and this computer's session is killed, while a remote
// one only detaches, as closing a remote tab does. Without, as when the
// window closes, the session goes on.
func (a *App) endPane(p *Pane, end bool) {
	p.closed = true
	if p.browser != nil {
		p.browser.stop()
		return
	}
	if end {
		a.markPaneClosed(p)
	}
	if p.term != nil {
		a.closePaneTerminal(p)
	}
	if sid := cmp.Or(p.SID, p.recoverySID); end && sid != "" && p.host == nil {
		go a.client.Kill(sid)
	}
}

// replacePane puts p in old's place in its tab, zoomed and focused as old
// was, so that closing p later never gives the focus back to old.
func (a *App) replacePane(old, p *Pane) {
	t := old.Tab
	p.Tab, p.Node = t, old.Node
	old.Node.Pane = p
	if t.Zoom == old {
		t.Zoom = p
	}
	t.recent = slices.DeleteFunc(t.recent, func(q *Pane) bool { return q == old })
	t.setFocus(p)
	a.focusReq = p
	a.changed()
}

// closePane closes a pane and ends its session.
func (a *App) closePane(p *Pane) {
	if p.closed {
		return
	}
	a.endPane(p, true)
	t := p.Tab
	if t.Zoom == p {
		t.Zoom = nil
	}
	t.recent = slices.DeleteFunc(t.recent, func(q *Pane) bool { return q == p })
	n := p.Node
	if n.Parent == nil {
		a.removeTab(t)
		return
	}
	parent := n.Parent
	other := parent.A
	if other == n {
		other = parent.B
	}
	// The other side takes the parent's place.
	parent.Pane, parent.A, parent.B, parent.Vertical, parent.Ratio = other.Pane, other.A, other.B, other.Vertical, other.Ratio
	if parent.Pane != nil {
		parent.Pane.Node = parent
	} else {
		parent.A.Parent, parent.B.Parent = parent, parent
	}
	if t.Focus == p {
		next := (*Pane)(nil)
		if k := len(t.recent); k > 0 {
			next = t.recent[k-1]
		} else if ps := t.panes(); len(ps) > 0 {
			next = ps[0]
		}
		t.Focus = nil
		t.setFocus(next)
	}
	if a.tab() == t {
		a.focusReq = t.Focus
	}
	a.changed()
}

func (a *App) removeTab(t *Tab) {
	i := slices.Index(a.tabs, t)
	if i < 0 {
		return
	}
	a.tabs = slices.Delete(a.tabs, i, i+1)
	if a.active > i || a.active >= len(a.tabs) {
		a.active = max(a.active-1, 0)
	}
	if a.renaming == t {
		a.renaming = nil
	}
	if len(a.tabs) == 0 {
		a.changed()
		a.save()
		if a.win != nil {
			a.win.Close()
		}
		return
	}
	if nt := a.tab(); nt != nil {
		a.focusReq = nt.Focus
	}
	a.changed()
}

// closeTab closes a tab and ends the sessions of its panes.
func (a *App) closeTab(t *Tab) {
	for _, p := range t.panes() {
		a.endPane(p, true)
	}
	a.removeTab(t)
}

func (a *App) selectTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	a.active = i
	t := a.tabs[i]
	if t.Focus != nil {
		t.Focus.attention = false
	}
	a.focusReq = t.Focus
	a.closeViewedPaneNotice()
	a.changed()
}

// moveFocus gives the focus to the pane next to the focused one, toward
// dx, dy.
func (a *App) moveFocus(dx, dy int) {
	t := a.tab()
	if t == nil || t.Focus == nil || t.Zoom != nil {
		return
	}
	from := t.Focus.bounds
	cx, cy := from.X+from.W/2, from.Y+from.H/2
	var best *Pane
	bestScore := float32(1e9)
	for _, p := range t.panes() {
		if p == t.Focus {
			continue
		}
		r := p.bounds
		var dist, overlap float32
		switch {
		case dx > 0:
			dist = r.X - (from.X + from.W)
			overlap = min(r.Y+r.H, from.Y+from.H) - max(r.Y, from.Y)
		case dx < 0:
			dist = from.X - (r.X + r.W)
			overlap = min(r.Y+r.H, from.Y+from.H) - max(r.Y, from.Y)
		case dy > 0:
			dist = r.Y - (from.Y + from.H)
			overlap = min(r.X+r.W, from.X+from.W) - max(r.X, from.X)
		default:
			dist = from.Y - (r.Y + r.H)
			overlap = min(r.X+r.W, from.X+from.W) - max(r.X, from.X)
		}
		if dist < -4 || overlap <= 0 {
			continue
		}
		// The nearest, then the one most in line with the focused pane's
		// center.
		px, py := r.X+r.W/2, r.Y+r.H/2
		off := px - cx
		if dx != 0 {
			off = py - cy
		}
		score := dist*4 + abs(off)
		if score < bestScore {
			best, bestScore = p, score
		}
	}
	if best != nil {
		t.setFocus(best)
		a.focusReq = best
	}
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// resizeFocused moves the nearest divider of the focused pane on the
// axis of dx, dy.
func (a *App) resizeFocused(dx, dy int) {
	t := a.tab()
	if t == nil || t.Focus == nil {
		return
	}
	vertical := dy != 0
	for n := t.Focus.Node; n.Parent != nil; n = n.Parent {
		p := n.Parent
		if p.Vertical != vertical {
			continue
		}
		step := float32(0.05)
		if dx < 0 || dy < 0 {
			step = -step
		}
		p.Ratio = min(max(p.Ratio+step, 0.1), 0.9)
		a.changed()
		return
	}
}

// equalize gives every split of the tab equal shares, by the panes on
// either side.
func (a *App) equalize() {
	t := a.tab()
	if t == nil {
		return
	}
	var walk func(n *Node) (cols, rows int)
	walk = func(n *Node) (int, int) {
		if n.Pane != nil {
			return 1, 1
		}
		ac, ar := walk(n.A)
		bc, br := walk(n.B)
		if n.Vertical {
			n.Ratio = float32(ar) / float32(ar+br)
			return max(ac, bc), ar + br
		}
		n.Ratio = float32(ac) / float32(ac+bc)
		return ac + bc, max(ar, br)
	}
	walk(t.Root)
	a.changed()
}

func (a *App) toggleZoom() {
	t := a.tab()
	if t == nil || t.Focus == nil {
		return
	}
	if t.Zoom != nil {
		t.Zoom = nil
	} else if len(t.panes()) > 1 {
		t.Zoom = t.Focus
	}
	a.focusReq = t.Focus
	a.changed()
}

// label returns how a pane names itself: its program, or the title the
// program set, and the directory it works in.
func (p *Pane) label() (name, detail string) {
	if p.browser != nil {
		return browserTitle(p.browser)
	}
	in := p.info
	prog := paneProgram(p)
	dir := shortDir(in.Dir)
	if in.Program == "" {
		return in.Shell, shortDir(p.startDir)
	}
	if in.Idle || prog.Shell {
		name := in.Program
		if name == "" {
			name = in.Shell
		}
		return name, dir
	}
	if t := strings.TrimSpace(p.title); t != "" && !titleOfShell(t, in) {
		t = programTitle(t, prog)
		if t != "" {
			return t, ""
		}
	}
	name = prog.Name
	if name == "ssh" || in.Program == "ssh" {
		if host := sshHost(in.Args); host != "" {
			return "SSH", host
		}
	}
	return name, dir
}

// titleOfShell tells a title a shell set, naming the program or the
// directory, from one a program set of its own.
func titleOfShell(title string, in rex.SessionInfo) bool {
	l := strings.ToLower(title)
	for _, w := range []string{strings.ToLower(in.Program), strings.ToLower(in.Shell)} {
		if w != "" && strings.HasPrefix(l, w) {
			return true
		}
	}
	if home, _ := os.UserHomeDir(); strings.Contains(title, "~") || (home != "" && strings.Contains(title, home)) {
		return true
	}
	return strings.Contains(title, "@") && strings.Contains(title, ":")
}

func sshHost(args []string) string {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if len(a) == 2 && strings.Contains("bcDEeFIiJLlmOopQRSWw", a[1:]) {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// shortDir shows a directory as shells do, from ~, and only its last
// parts when it is deep.
func shortDir(d string) string {
	if d == "" {
		return ""
	}
	if home, _ := os.UserHomeDir(); home != "" {
		if d == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(d, home+"/"); ok {
			d = "~/" + rest
		}
	}
	parts := strings.Split(d, "/")
	if len(parts) > 5 || len(d) > 48 && len(parts) > 3 {
		return "…/" + strings.Join(parts[len(parts)-2:], "/")
	}
	return d
}

// tabLabel names a tab: its name, or its focused pane's.
func (t *Tab) label() (name, detail string) {
	if t.Name != "" {
		return t.Name, ""
	}
	if t.Focus == nil {
		return "Shell", ""
	}
	return t.Focus.label()
}

// The saved layout: the tabs, their splits and the sessions of their
// panes, which the server keeps, and the app restores when it starts.
type savedLayout struct {
	Active int        `json:"active"`
	Tabs   []savedTab `json:"tabs"`
}

type savedTab struct {
	Name  string     `json:"name,omitempty"`
	Root  *savedNode `json:"root"`
	Focus int        `json:"focus"`
	Zoom  bool       `json:"zoom,omitempty"`
}

type savedNode struct {
	Browser  *savedBrowser `json:"browser,omitempty"`
	SID      string        `json:"sid,omitempty"`
	Dir      string        `json:"dir,omitempty"`
	Cols     int           `json:"cols,omitempty"`
	Rows     int           `json:"rows,omitempty"`
	Vertical bool          `json:"vertical,omitempty"`
	Ratio    float32       `json:"ratio,omitempty"`
	A        *savedNode    `json:"a,omitempty"`
	B        *savedNode    `json:"b,omitempty"`
}

func (a *App) snapshot() savedLayout { return a.snapshotHost(nil) }

func (a *App) snapshotHost(host *desktopHost) savedLayout {
	l := savedLayout{}
	var save func(n *Node) *savedNode
	save = func(n *Node) *savedNode {
		if p := n.Pane; p != nil {
			if p.browser != nil {
				return &savedNode{Dir: p.info.Dir, Browser: &savedBrowser{ID: p.browser.id, URL: p.browser.url}}
			}
			s := &savedNode{SID: p.SID, Dir: p.info.Dir}
			if s.SID == "" {
				s.SID = p.recoverySID
			}
			if s.Dir == "" {
				s.Dir = p.startDir
			}
			if p.term != nil {
				s.Cols, s.Rows = p.term.Size()
			}
			return s
		}
		return &savedNode{Vertical: n.Vertical, Ratio: n.Ratio, A: save(n.A), B: save(n.B)}
	}
	for _, t := range a.tabs {
		if t.Host != host {
			continue
		}
		if t == a.tab() {
			l.Active = len(l.Tabs)
		}
		st := savedTab{Name: t.Name, Root: save(t.Root), Zoom: t.Zoom != nil}
		st.Focus = max(slices.Index(t.panes(), t.Focus), 0)
		l.Tabs = append(l.Tabs, st)
	}
	return l
}

// changed notes that the layout changed, to save it soon.
func (a *App) changed() { a.saveDue = true; a.desktops.dirty = true }

// save sends the layout to the server, which keeps it on disk.
func (a *App) save() {
	a.saveDue = false
	if a.desktops.dirty {
		a.desktops.dirty = false
		a.saveDesktopHistory()
	}
	a.lastSave = time.Now()
	if a.client == nil {
		return
	}
	b, err := json.Marshal(a.snapshot())
	if err != nil || string(b) == a.lastSnapshot {
		return
	}
	a.lastSnapshot = string(b)
	c := a.client
	go c.SetLayout(b)
}

// restore opens the tabs of the saved layout, attaching to the sessions
// the server still has and starting new ones in place of those it lost.
func (a *App) restore() bool {
	raw, err := a.client.Layout()
	if err != nil {
		return false
	}
	var l savedLayout
	if len(raw) > 0 && json.Unmarshal(raw, &l) != nil {
		return false
	}
	live := map[string]rex.SessionInfo{}
	if infos, err := a.client.List(); err == nil {
		for _, in := range infos {
			if !in.Exited {
				live[in.ID] = in
			}
		}
	}
	used := map[string]bool{}
	var load func(s *savedNode, t *Tab, parent *Node) *Node
	load = func(s *savedNode, t *Tab, parent *Node) *Node {
		n := &Node{ID: a.id(), Parent: parent}
		if s.A == nil || s.B == nil {
			if s.Browser != nil {
				p, err := a.newBrowserPane(s.Browser.ID, s.Browser.URL, nil)
				if err != nil {
					p, _ = a.newBrowserPane(s.Browser.ID, "about:blank", nil)
				}
				p.Tab, p.Node = t, n
				p.info.Dir, p.startDir = s.Dir, s.Dir
				n.Pane = p
				return n
			}
			var p *Pane
			if in, ok := live[s.SID]; ok && !used[s.SID] {
				used[s.SID] = true
				p = &Pane{ID: a.id(), SID: s.SID, info: in, startDir: s.Dir}
				a.attach(p, in.Cols, in.Rows)
			} else {
				if a.hello.AgentRecovery && s.SID != "" && !used[s.SID] {
					used[s.SID] = true
					p = a.restoreAgentPane(s)
				}
				if p == nil {
					dir := s.Dir
					if st, err := os.Stat(dir); err != nil || !st.IsDir() {
						dir, _ = os.UserHomeDir()
					}
					p = a.newPane(dir, s.Cols, s.Rows)
					p.restored = true
				}
			}
			p.Tab, p.Node = t, n
			n.Pane = p
			return n
		}
		n.Vertical, n.Ratio = s.Vertical, min(max(s.Ratio, 0.05), 0.95)
		n.A, n.B = load(s.A, t, n), load(s.B, t, n)
		return n
	}
	for _, st := range l.Tabs {
		if st.Root == nil {
			continue
		}
		t := &Tab{ID: a.id(), Name: st.Name}
		t.Root = load(st.Root, t, nil)
		ps := t.panes()
		for _, p := range ps {
			t.setFocus(p)
		}
		t.setFocus(ps[min(max(st.Focus, 0), len(ps)-1)])
		if st.Zoom && len(ps) > 1 {
			t.Zoom = t.Focus
		}
		a.tabs = append(a.tabs, t)
	}
	// Sessions no tab shows, as those of another window that crashed,
	// get a tab of their own rather than running unseen.
	for _, in := range sortedInfos(live) {
		if used[in.ID] {
			continue
		}
		a.addSessionTab(in)
	}
	if len(a.tabs) == 0 {
		return false
	}
	a.active = min(max(l.Active, 0), len(a.tabs)-1)
	a.focusReq = a.tab().Focus
	return true
}

func (a *App) markSessionClosed(sid string) {
	if sid != "" {
		if a.closedSessions == nil {
			a.closedSessions = map[string]bool{}
		}
		a.closedSessions[sid] = true
		a.closeAgentNotice(sid)
	}
}

func (a *App) addSessionTab(in rex.SessionInfo) {
	p := &Pane{ID: a.id(), SID: in.ID, info: in, startDir: in.Dir}
	a.attach(p, in.Cols, in.Rows)
	a.tabs = append(a.tabs, a.singlePaneTab(p))
}

// Remote-created sessions share the same server, but not the GUI's layout.
// Append their tabs without changing focus or resizing any existing session.
func (a *App) syncSessionTabs(byID map[string]rex.SessionInfo) {
	if a.client == nil || a.quitting {
		return
	}
	shown := map[string]bool{}
	for p := range a.hostPanes(nil) {
		shown[p.SID] = true
	}
	added := false
	for _, in := range sortedInfos(byID) {
		if in.ID == "" || in.Exited || shown[in.ID] || a.closedSessions[in.ID] {
			continue
		}
		a.addSessionTab(in)
		added = true
	}
	if added {
		if a.tab() != nil && a.focusReq == nil && len(shown) == 0 {
			a.focusReq = a.tab().Focus
		}
		a.changed()
	}
}

func sortedInfos(m map[string]rex.SessionInfo) []rex.SessionInfo {
	out := make([]rex.SessionInfo, 0, len(m))
	for _, in := range m {
		out = append(out, in)
	}
	slices.SortFunc(out, func(a, b rex.SessionInfo) int { return a.Created.Compare(b.Created) })
	return out
}

// poll asks the server what runs in each session, every half second,
// until the window closes.
func (a *App) poll(win *mygo.Window, client *rex.Client) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-client.Closed():
			win.Update(func() {
				if !a.quitting {
					a.err = "Lost the connection to the session server."
				}
			})
			return
		case <-tick.C:
		}
		infos, err := client.List()
		if err != nil {
			continue
		}
		byID := make(map[string]rex.SessionInfo, len(infos))
		for _, in := range infos {
			byID[in.ID] = in
		}
		mygo.RunOnMain(func() {
			if a.win == win {
				if a.applyPolled(nil, byID) {
					win.Invalidate()
				}
			}
		})
	}
}

// apply takes what the server said of the sessions.
func (a *App) apply(byID map[string]rex.SessionInfo) { a.applyHost(nil, byID) }

func (a *App) applyHost(host *desktopHost, byID map[string]rex.SessionInfo) {
	if a.win != nil {
		a.focusedWin = a.win.IsFocused()
	}
	a.closeViewedPaneNotice()
	// Remote panes whose sessions ended close after the loop, as closing
	// one changes the tree it walks.
	var ended []*Pane
	for p := range a.hostPanes(host) {
		t := p.Tab
		in, ok := byID[p.SID]
		if !ok {
			if host != nil && time.Since(p.info.Created) > 3*time.Second {
				ended = append(ended, p)
			}
			continue
		}
		was := p.info
		if a.hostHello(host).Version == 4 {
			previous := was
			// Reopening a window on the same App resets pane snapshots,
			// but retained notices must not swallow the next legacy turn.
			previous.Agent.CompletionRevision = max(previous.Agent.CompletionRevision, a.agentFinishedNotified[p.noticeKey()])
			in.Agent = rex.LegacyCompletionState(previous, in)
		}
		p.info = in
		if host != nil {
			if p.remoteView != nil && p.stream == nil {
				p.remoteView.geometry(in.Cols, in.Rows)
			}
			a.followRemoteSizeOwner(p)
		}
		// A viewer uses its own TCP stream. The control connection can
		// stay healthy after that stream drops, so restore it on the
		// next successful poll without redialing or replacing the shell.
		if host != nil && host.connected() && p.streamEnded && !in.Exited {
			a.reattachDesktopPane(p, in)
		}
		a.followSizeLock(p, was)
		a.updateAgentNotice(p, was.Agent)
		// A program that ran a while and finished out of sight
		// asks for attention.
		if !was.Idle && was.Program != "" && in.Idle && was.Program != in.Program {
			seen := t == a.tab() && t.Focus == p && a.focusedWin
			if !seen {
				p.attention = true
			}
			var completion uint64
			if agents.Finished(in.Agent.State) {
				completion = in.Agent.CompletionRevision
			}
			if agents.Finished(was.Agent.State) {
				if agent, ok := agents.Detect(was.Program, was.Args); ok && agent.ID == was.Agent.ID {
					completion = max(completion, was.Agent.CompletionRevision)
				}
			}
			integratedFinished := completion > 0 && a.agentFinishedNotified[p.noticeKey()] >= completion
			if !seen && !integratedFinished && was.Agent.Source != rex.ProgramStatusSource && in.Agent.Source != rex.ProgramStatusSource && time.Since(was.LastInput) > 8*time.Second {
				prog := programOf(was.Program)
				a.showPaneNotice(p, "program", mygo.NotificationOptions{
					Title: prog.Name + " finished",
					Body:  "in " + shortDir(in.Dir),
				})
			}
		}
		if was.Dir != in.Dir {
			a.changed()
		}
		if host != nil && in.Exited {
			ended = append(ended, p)
		}
	}
	for _, p := range ended {
		a.closePane(p)
	}
	for sid := range a.agentNotices {
		if _, ok := byID[noticeSessionID(sid)]; !ok && noticeOnHost(sid, host) {
			a.closeAgentNotice(sid)
		}
	}
	if host == nil {
		a.syncSessionTabs(byID)
	}
	if a.saveDue && time.Since(a.lastSave) > time.Second {
		a.save()
	}
}
