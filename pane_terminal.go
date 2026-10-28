package main

import (
	"cmp"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"retty/internal/terminal"
)

type terminalPane struct {
	SID                       string
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
	lastData    atomic.Int64
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

func (v *terminalPane) dispose(a *App, p *Pane, end bool) {
	if end {
		a.markPaneClosed(p)
	}
	if p.terminalView() != nil {
		a.closePaneTerminal(p)
	}
	if sid := cmp.Or(v.SID, v.recoverySID); end && sid != "" && p.host == nil {
		go a.client.Kill(sid)
	}
}
func (v *terminalPane) label(p *Pane) (name, detail string) {
	in := v.info
	prog := paneProgram(p)
	dir := shortDir(in.Dir)
	if in.Program == "" {
		return in.Shell, shortDir(v.startDir)
	}
	if in.Idle || prog.Shell {
		name := in.Program
		if name == "" {
			name = in.Shell
		}
		return name, dir
	}
	if t := strings.TrimSpace(v.title); t != "" && !titleOfShell(t, in) {
		t = programTitle(t, prog)
		if t != "" {
			return positionTitleFolder(t, prefs.TitleFolderPosition), ""
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

func (v *terminalPane) render(a *App, c *ui.Context, k *colors, p *Pane) {
	t := p.Tab
	a.paneGitBar(c, k, t, p)
	if v.find.open {
		a.findBar(c, k, t, p)
	}
	if v.locked {
		a.sizeLockBar(c, k, p)
	}
	if v.upload != nil {
		a.fileUploadBar(c, k, p)
	}
	if v.recoveryError != "" {
		a.agentRecoveryBar(c, k, p)
	}
	body := ui.Box(c.Key("terminal-body")).Grow(1).MinHeight(0)
	body.Children(func() {
		if p.terminalView() == nil {
			return
		}
		program := paneProgram(p)
		v.term.SetSelectOnDrag(program.Agent || program.LocalSelection)
		v.term.SetSelectOnDragInAltScreen(program.LocalSelection)
		tv := terminal.View(c, v.term).Fill()
		if p.acceptsFileDrop() {
			if files := tv.DroppedFiles(); len(files) > 0 {
				a.pasteDroppedFiles(t, p, files)
			}
		}
		if v.find.open && tv.Shortcut(0, ui.KeyEscape) {
			a.closeFind(p)
		}
		if a.focusReq == p && a.tab() == t {
			tv.Focus()
			a.focusReq = nil
		}
		if tv.Focused() && t.Focus != p {
			t.setFocus(p)
			a.changed()
		}
		if tv.Pressed() && p.host != nil && !v.remoteYielded {
			a.resumeRemotePane(p)
		}
		if tv.Focused() {
			p.attention = false
		}
	})
	if p.host != nil && (!p.host.connected() || v.streamEnded) {
		ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(32).Padding(0, paneActionInset(p), 0, 0).Background(k.panel).
			Children(func() { a.desktopOfflineBar(c, k, p.host) })
	} else if p.host != nil && v.remoteView != nil && a.remotePaneStatus(c, p) != "" {
		ui.Row(c).Absolute().Top(0).Left(0).Right(0).Height(28).Padding(0, 8+paneActionInset(p), 0, 12).AlignItems(ui.Center).Gap(8).Background(k.panel).Children(func() {
			message := a.remotePaneStatus(c, p)
			ui.Text(c, message).FontSize(12).TextColor(k.textMuted).Grow(1)
			if v.remoteYielded && iconButton(c, k, "rotate-ccw", "接入当前窗格", 24, 13).Clicked() {
				t.setFocus(p)
				a.focusReq = p
				a.activateRemotePane(p)
			}
		})
	}
}
func (v *terminalPane) kind() paneKind                   { return paneTerminal }
func (v *terminalPane) split(a *App, p *Pane, down bool) { a.splitTerminal(down) }
func (v *terminalPane) snapshot(p *Pane) *savedNode {
	s := &savedNode{SID: cmp.Or(v.SID, v.recoverySID)}
	if v.term != nil {
		s.Cols, s.Rows = v.term.Size()
	}
	return s
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
	p := &Pane{ID: a.id(), content: &terminalPane{SID: info.ID, info: info, startDir: dir}}
	if p.terminal().info.Dir == "" {
		p.terminal().info.Dir = dir
	}
	a.attach(p, cols, rows)
	return p
}

// attach makes the pane's terminal, attached to its session.
func (a *App) attach(p *Pane, cols, rows int) {
	v := p.terminal()
	if v == nil {
		return
	}
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
	if v.SID == "" {
		term, err := terminal.New(options)
		if err == nil {
			message := "Could not start a session: " + a.err
			if v.recoveryError != "" {
				message = "无法恢复 Agent 会话：" + v.recoveryError
			}
			if cols <= 0 {
				cols = 80
			}
			if rows <= 0 {
				rows = 24
			}
			term.Resize(cols, rows)
			term.Feed([]byte("\x1b[31m" + message + "\x1b[0m\r\n"))
			v.term = term
		}
		return
	}
	if p.host != nil {
		var view *sessionViewStream
		view = newSessionViewStream(nil, func() {
			a.post(func() {
				if !p.closed && !a.quitting && v.remoteView == view && !view.hasTransport() {
					a.pauseRemotePane(p)
					v.streamEnded = true
				}
			})
			if win != nil {
				win.Invalidate()
			}
		})
		v.remoteView = view
		view.setInput(nil)
	} else {
		v.stream = a.paneClient(p).Stream(v.SID, cols, rows)
	}
	stream := v.stream
	v.streamEnded = false
	if v.stream != nil {
		v.stream.OnData = func(int) { v.lastData.Store(time.Now().UnixNano()) }
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
	options.Conn = v.stream
	if p.host != nil {
		// Only the active pane attaches and supplies its own viewport size.
		// Reuse the phone's retained-reader and atomic snapshot replacement.
		options.Conn = v.remoteView
	}
	options.OnRenderEvent = func(event terminal.RenderEvent) { logRenderEvent(v.SID, event) }
	options.OnTitle = func(title string) {
		update(func() { v.title = title })
	}
	options.OnBell = func() {
		update(func() { a.ring(p) })
	}
	options.OnNotify = func(title, body string) {
		update(func() { a.showTerminalNotice(p, title, body) })
	}
	options.OnExit = func(int) {
		update(func() {
			if p.closed || a.quitting || v.stream != stream || a.systemShuttingDown() {
				return
			}
			if p.host == nil {
				if v.info.Resumed {
					a.checkAgentRestoreExit(p, stream)
				} else {
					a.closePane(p)
				}
			} else {
				v.streamEnded = true
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
	v.term = term
}

// nopConn is the Conn of a terminal that only shows what the app feeds it.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)    { select {} }
func (nopConn) Write(p []byte) (int, error) { return len(p), nil }
func (nopConn) Close() error                { return nil }

// splitTerminal opens a shell beside any focused component.
func (a *App) splitTerminal(vertical bool) {
	t := a.tab()
	if t == nil || t.Focus == nil {
		return
	}
	old := t.Focus
	if t.Host != nil {
		a.createDesktopSession(t.Host, a.currentDir(), old, vertical)
		return
	}
	cols, rows := 80, 24
	if old.terminalView() != nil {
		cols, rows = old.terminal().term.Size()
		if vertical {
			rows = max(rows/2, 2)
		} else {
			cols = max(cols/2, 10)
		}
	}
	p := a.newPane(a.currentDir(), cols, rows)
	a.placePane(old, p, vertical)
}
