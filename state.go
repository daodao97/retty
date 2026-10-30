package main

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

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

// Pane is a container in the split tree. Its content owns rendering and resources.
type Pane struct {
	ID        int
	Key       string // persisted identity, independent of a terminal session
	Tab       *Tab
	Node      *Node
	host      *desktopHost
	dir       string // working context inherited when the component is opened
	content   paneContent
	attention bool
	bounds    ui.Rect
	closed    bool
}

// App is the state of Retty's window.
type App struct {
	usage            *desktopUsage
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
	filesWidthSaveAt time.Time
	readReceipts     readReceiptState
	pollSnapshots    map[*desktopHost]map[string]rex.SessionInfo
	desktops         desktopConnections
	desktopStorage   chan func()
	phone            *phonePair
	openLinkURL      func(string) error // injectable native URL opener
	win              *mygo.Window
	appearanceItems  map[string]*mygo.MenuItem
	commandItems     map[*command]*mygo.MenuItem
	client           *rex.Client
	hello            rex.Hello
	err              string

	tabs                  []*Tab
	active                int
	nextID                int
	sidebarVisibleTab     *Tab // scroll selection into view only when it changes
	sidebarProjectOpen    map[string]bool
	sidebarVisibleProject string

	// focusReq asks the view to give the keyboard focus to a pane's
	// terminal.
	focusReq *Pane

	// The command palette, settings and the tab being renamed.
	paletteOpen          bool
	paletteQuery         string
	paletteSSHHosts      []string
	paletteSSHLoading    bool
	paletteSSHError      string
	paletteSSHEpoch      uint64
	settingsOpen         bool
	settingsSection      int
	settingsQuery        string
	settingsModifiedOnly bool
	settingsInitialFocus bool
	renaming             *Tab
	renameText           string
	renameFocus          bool
	goalSetup            *goalSetup
	automations          automationsView
	// openedAutomation are automation sessions opened in a tab on request;
	// hiddenSessions are automation sessions the task board shows instead.
	openedAutomation map[string]bool
	hiddenSessions   map[string]bool
	// setupSessions are agents setting up tasks; the task board shows
	// them. taskBoard shows over boardOver's content while boardShown.
	setupSessions  []setupSession
	taskBoard      *boardPane
	boardShown     bool
	boardOver      *Tab
	saveDue        bool
	lastSave       time.Time
	quitting       bool
	shuttingDown   func() bool // nil uses the OS detector
	focusedWin     bool
	activeRemote   *Pane
	remoteReleases []remoteSizeRelease
	lastSnapshot   string
	title          string
	agentHooks     map[string]agents.HookInstallation
	agentHookBusy  string
	agentHookError string
	// screenViewers is how many devices watch the screen; screenAccess is
	// the helper's permissions, nil until read.
	screenViewers         int
	screenAccess          *screenAccess
	screenBusy            bool
	closedLid             closedLid
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
	a.changed()
	return t
}

// currentDir returns the directory new panes open in: the focused pane's.
func (a *App) currentDir() string {
	if t := a.tab(); t != nil && t.Focus != nil {
		if d := t.Focus.directory(); d != "" {
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

// hostPanes yields only terminal panes on a computer, nil for this one.
func (a *App) hostPanes(h *desktopHost) iter.Seq[*Pane] {
	return func(yield func(*Pane) bool) {
		for t, p := range a.allPanes() {
			if t.Host == h && p.terminal() != nil && !yield(p) {
				return
			}
		}
	}
}

// endPane disposes the content. For terminals, end also ends the session:
// its notice closes and this computer's session is killed, while a remote
// one only detaches, as closing a remote tab does. Without, as when the
// window closes, the session goes on.
func (a *App) endPane(p *Pane, end bool) {
	if p.closed {
		return
	}
	p.closed = true
	if p.content != nil {
		p.content.dispose(a, p, end)
	}
}

// replacePane puts p in old's place in its tab, zoomed and focused as old
// was, so that closing p later never gives the focus back to old.
func (a *App) replacePane(old, p *Pane) {
	p.Key = old.identity()
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

// closePane removes a container and disposes its content.
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
	// Choosing a tab shows it, also in place of the task board.
	a.hideBoard()
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
	if p.content == nil {
		return "窗格", ""
	}
	return p.content.label(p)
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
	Kind    paneKind      `json:"kind,omitempty"`
	PaneID  string        `json:"paneId,omitempty"`
	Files   *savedFiles   `json:"files,omitempty"`
	Browser *savedBrowser `json:"browser,omitempty"`
	SID     string        `json:"sid,omitempty"`
	// AutomationOpened keeps an automation's session the person opened
	// in a tab; any other automation session gets none.
	AutomationOpened bool       `json:"automationOpened,omitempty"`
	Dir              string     `json:"dir,omitempty"`
	Cols             int        `json:"cols,omitempty"`
	Rows             int        `json:"rows,omitempty"`
	Vertical         bool       `json:"vertical,omitempty"`
	Ratio            float32    `json:"ratio,omitempty"`
	A                *savedNode `json:"a,omitempty"`
	B                *savedNode `json:"b,omitempty"`
}

func (a *App) snapshot() savedLayout { return a.snapshotHost(nil) }

func (a *App) snapshotHost(host *desktopHost) savedLayout {
	l := savedLayout{}
	var save func(n *Node) *savedNode
	save = func(n *Node) *savedNode {
		if p := n.Pane; p != nil {
			s := p.content.snapshot(p)
			s.Kind, s.PaneID, s.Dir = p.content.kind(), p.identity(), p.directory()
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
			if p := a.restorePaneContent(s, nil); p != nil {
				p.Tab, p.Node = t, n
				n.Pane = p
				return n
			}
			var p *Pane
			if in, ok := live[s.SID]; ok && !used[s.SID] {
				used[s.SID] = true
				p = &Pane{ID: a.id(), content: &terminalPane{SID: s.SID, info: in, startDir: s.Dir, automationOpened: s.AutomationOpened}}
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
					p.terminal().restored = true
				}
			}
			p.Tab, p.Node, p.Key = t, n, s.PaneID
			n.Pane = p
			return n
		}
		n.Vertical, n.Ratio = s.Vertical, min(max(s.Ratio, 0.05), 0.95)
		n.A, n.B = load(s.A, t, n), load(s.B, t, n)
		return n
	}
	// Automation sessions show on the task board, not in tabs, also when
	// an earlier version saved one in the layout. Leaving one out only
	// leaves it untabbed: it keeps running.
	a.automations.reload()
	var prune func(s *savedNode) *savedNode
	prune = func(s *savedNode) *savedNode {
		if s == nil {
			return nil
		}
		if s.A == nil || s.B == nil {
			in, ok := live[s.SID]
			switch {
			case !ok || s.Kind != "" && s.Kind != paneTerminal:
			case s.AutomationOpened:
				if a.openedAutomation == nil {
					a.openedAutomation = map[string]bool{}
				}
				a.openedAutomation[s.SID] = true
			case a.hiddenAutomationSession(in):
				used[s.SID] = true
				return nil
			}
			return s
		}
		x, y := prune(s.A), prune(s.B)
		switch {
		case x == nil:
			return y
		case y == nil:
			return x
		}
		copied := *s
		copied.A, copied.B = x, y
		return &copied
	}
	for _, st := range l.Tabs {
		if st.Root = prune(st.Root); st.Root == nil {
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
		if used[in.ID] || a.hiddenAutomationSession(in) {
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
	p := &Pane{ID: a.id(), content: &terminalPane{SID: in.ID, info: in, startDir: in.Dir}}
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
		shown[p.terminal().SID] = true
	}
	added := false
	reloaded := false
	for _, in := range sortedInfos(byID) {
		if in.ID == "" || in.Exited || shown[in.ID] || a.closedSessions[in.ID] {
			continue
		}
		// Read the schedule once per pass, after the list: a runner records
		// its agent a moment before polling may see the agent.
		if !reloaded && !a.hiddenSessions[in.ID] {
			a.automations.reload()
			reloaded = true
		}
		if a.hiddenAutomationSession(in) {
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
		in, ok := byID[p.terminal().SID]
		if !ok {
			if host != nil && time.Since(p.terminal().info.Created) > 3*time.Second {
				ended = append(ended, p)
			}
			continue
		}
		was := p.terminal().info
		if a.hostHello(host).Version == 4 {
			previous := was
			// Reopening a window on the same App resets pane snapshots,
			// but retained notices must not swallow the next legacy turn.
			previous.Agent.CompletionRevision = max(previous.Agent.CompletionRevision, a.agentFinishedNotified[p.noticeKey()])
			in.Agent = rex.LegacyCompletionState(previous, in)
		}
		p.terminal().info = in
		if host != nil {
			if p.terminal().remoteView != nil && p.terminal().stream == nil {
				p.terminal().remoteView.geometry(in.Cols, in.Rows)
			}
			a.followRemoteSizeOwner(p)
		}
		// A viewer uses its own TCP stream. The control connection can
		// stay healthy after that stream drops, so restore it on the
		// next successful poll without redialing or replacing the shell.
		if host != nil && host.connected() && p.terminal().streamEnded && !in.Exited {
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
