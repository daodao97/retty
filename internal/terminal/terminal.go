// Package terminal is a terminal for MyGo's native UI: a view that runs a
// shell, or any program, in a pseudo-terminal, with the terminal emulator
// of Ghostty, libghostty-vt, and draws it with package ui.
//
//	term, err := terminal.New(terminal.Options{})
//	...
//	mygo.NewWindow(mygo.WindowOptions{Title: "Terminal", Content: ui.View(func(c *ui.Context) {
//		terminal.View(c, term).Fill()
//	})})
//
// It does what terminals do: colors, styles and Unicode (wide characters,
// emoji, grapheme clusters), the scrollback with reflow on resize, the
// alternate screen of full-screen programs, the keyboard as legacy
// sequences or the Kitty keyboard protocol, mouse reporting, bracketed
// paste, focus reports, synchronized output, hyperlinks (OSC 8), titles,
// the working directory (OSC 7) and the clipboard (OSC 52). Selecting with
// the pointer copies as Ghostty does: a drag selects cells, a double click
// words and a triple click lines; Option (macOS) or Alt drags a rectangle.
//
// libghostty-vt is a native library loaded at run time, with no cgo: `mygo
// build` and `mygo dev` put the build this package binds into the app, and
// programs run without them, as with `go run` and `go test`, download it
// once into the user's cache. See LibraryPath.
package terminal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"retty/internal/terminal/internal/pty"
	"retty/internal/terminal/internal/vt"
)

// Options configure a terminal.
type Options struct {
	// ImageLimit bounds Kitty image storage; zero defaults to 8 MiB, negative disables it.
	ImageLimit int64
	// Command is the program the terminal runs and its arguments. Empty
	// runs the user's shell as a login shell, as terminal apps do: $SHELL,
	// else /bin/zsh on macOS and /bin/sh elsewhere; PowerShell on Windows.
	Command []string
	// Dir is the working directory of the program: the user's home
	// directory when empty.
	Dir string
	// Env adds to the environment of the program, as "KEY=value". The
	// terminal sets TERM=xterm-256color and COLORTERM=truecolor, and LANG
	// to a UTF-8 locale when no locale is set, as when the app starts from
	// the Finder.
	Env []string
	// Conn connects the terminal to something other than a program, such
	// as a remote shell: what the terminal reads from it shows, and what
	// is typed is written to it. Command, Dir and Env are then ignored. A
	// Conn with a method Resize(cols, rows int) error is told the size.
	Conn io.ReadWriteCloser

	// FixedCols and FixedRows decode a shared remote screen at its source
	// size, independently of the view. Both must be positive. FitToView
	// scales that grid to fit the view without reflowing ANSI output.
	FixedCols, FixedRows int
	FitToView            bool
	// ReflowView lays out the decoded primary screen at the local font size.
	// The canonical remote emulator keeps its original geometry.
	ReflowView bool

	// Font is the font of the text: the system's monospaced font at 13
	// DIPs when zero.
	Font Font
	// Theme sets the colors, and DarkTheme those in dark windows when set,
	// as Ghostty's theme = light:…,dark:… does (GhosttyTheme returns
	// Ghostty's themes). Without either, the terminal follows the window's
	// light or dark appearance, with LightTheme and DarkTheme.
	Theme, DarkTheme *Theme
	// Cursor is the shape of the cursor, which programs may change, and
	// NoBlink keeps it from blinking unless a program asks it to.
	Cursor  CursorStyle
	NoBlink bool
	// ActiveCursor keeps the cursor active when the software keyboard is
	// hidden, so a mobile terminal still blinks without taking input focus.
	ActiveCursor bool
	// InputContext exposes locally typed, unsubmitted text to native caret
	// navigation. It never buffers input or changes the terminal rendering.
	InputContext bool
	// Scrollback is about how many bytes of output the terminal keeps above
	// the screen: 10 MB when zero, none when negative.
	Scrollback int
	// OptionAsAlt makes Option on macOS the Alt key of programs (Meta),
	// rather than the key that types accented letters and symbols.
	OptionAsAlt bool
	// CopyRawText retains concealed characters when copying a selection.
	// The default copies visible text; neither mode interprets Markdown.
	CopyRawText bool
	// SelectOnDrag lets the terminal select text when a primary-screen program
	// tracks the mouse. Unless SelectOnDragInAltScreen is set, alternate-screen
	// programs retain their mouse protocol and application-owned history.
	// Plain clicks and the wheel still reach the program; Alt bypasses this
	// override, and Shift forces local selection.
	SelectOnDrag bool
	// SelectOnDragInAltScreen extends SelectOnDrag to alternate-screen programs
	// such as editors. Selection copies only the visible grid; the program still
	// receives clicks, the wheel and Alt drags. It has no effect without SelectOnDrag.
	SelectOnDragInAltScreen bool
	// OpenLinksOnTap opens links on an unmodified primary tap, without
	// requiring Command/Control. It is useful for touch terminals. Opening
	// waits for release; moving, cancellation and long-press selection do
	// not open a link. The default retains desktop modifier-click behavior.
	OpenLinksOnTap bool
	// AdaptiveColors keeps neutral RGB/extended-palette panels and text
	// readable when a program caches its startup palette across theme changes.
	// Normal ANSI background colors and colored panels retain their colors.
	AdaptiveColors bool
	// Transparent leaves the terminal's background undrawn, so that what
	// is under the view shows through, as the window's material or an
	// element's translucent background, as Ghostty's background-opacity
	// does: cells with a background color of their own still draw it.
	// Give the theme the background the view shows on, which the text
	// under a block cursor takes.
	Transparent bool

	// OnTitle, OnExit, OnBell and OnNotify run on a goroutine of the
	// terminal when a program sets the title, when the program (or Conn)
	// ends, when it rings the bell, and when it asks for a desktop
	// notification (OSC 9, OSC 777). Calls to the terminal are safe in
	// them.
	OnTitle  func(title string)
	OnExit   func(code int)
	OnBell   func()
	OnNotify func(title, body string)
	// OnRenderEvent reports slow synchronized updates and watchdog releases,
	// without terminal contents. It runs on the reader or UI thread after
	// releasing emulator locks; calls back into the terminal are safe.
	OnRenderEvent func(RenderEvent)
	// OnOpenLink runs on the UI thread, without holding the emulator lock.
	// It receives a Command-clicked URL or printed file path and location.
	OnOpenLink func(Link)
	// OnPreviewLink prepares an optional image hover reader on the UI thread.
	OnPreviewLink func(Link) *LinkPreviewSource
	// OnSplit runs on the UI thread when a context-menu split is chosen.
	// down is false for side-by-side panes and true for stacked panes.
	OnSplit func(down bool)
	// OnPaste runs on the UI thread and can handle clipboard data externally.
	// Returning true suppresses the normal bracketed text paste.
	OnPaste func(ui.Services) bool
	// OnSubmit runs on the UI thread after an unmodified Return key is
	// released. Its terminal key events have already been queued.
	OnSubmit func(ui.Services)
}

// CursorStyle is the shape of the cursor.
type CursorStyle int

const (
	CursorBlock CursorStyle = iota
	CursorBar
	CursorUnderline
)

// Terminal is a terminal emulator and the program it runs. Its methods are
// safe from any goroutine.
type Terminal struct {
	previewState *linkPreview // guarded by mu
	opts         Options
	in           inputQueue
	context      inputContext

	// mu guards the emulator, which the reader changes and frames read.
	mu                             sync.Mutex
	term                           *vt.Terminal
	presentation                   *vt.Terminal
	revision, presentationRevision uint64
	presentationSize               gridSize
	presentationHistory            int
	search                         *vt.Search
	searchMoves                    int
	size                           gridSize
	events                         events
	closed                         bool
	clipOut                        []string // what programs copied, for the view to write
	// src is the program's pseudo-terminal, or Conn: nil until the
	// program starts, from launch, its options.
	src    io.ReadWriteCloser
	proc   *pty.PTY
	launch *pty.Options
	timer  *time.Timer // starts the program when no view does

	srcOnce sync.Once
	srcErr  error
	// connMu guards the size Conn is told, from a goroutine at a time.
	connMu   sync.Mutex
	connSize gridSize
	connBusy bool

	// rmu guards what frames draw from: the render state, which a
	// program's synchronized update (mode 2026) holds.
	rmu           sync.Mutex
	rs            *vt.RenderState
	held          bool
	heldSince     time.Time // most recent input during a synchronized update
	holdStarted   time.Time
	resumeReading *readingPosition

	draw     atomic.Pointer[func()] // asks for a frame of the view
	done     chan struct{}
	doneOnce sync.Once
	code     atomic.Int64

	// curTheme is the view's theme, for the program's questions; t.mu
	// guards it. v is the view's state, on the main thread.
	curTheme *Theme
	v        *view
}

// gridSize is the size of the screen in cells and of a cell in pixels.
type gridSize struct{ cols, rows, cellW, cellH int }

// RenderEvent describes a rendering delay without recording program output.
type RenderEvent struct {
	Kind     string // "slow_sync" or "sync_timeout"
	Duration time.Duration
}

const syncOutputTimeout = time.Second
const slowSyncThreshold = 250 * time.Millisecond

// events are what a program did while the reader wrote its output, for
// the callbacks, which run once the lock is released.
type events struct {
	title  bool
	bells  int
	notify [][2]string
	render []RenderEvent
}

// New makes a terminal running opts.Command, or connected to opts.Conn.
// The program starts once a view shows the terminal, at the size of the
// view, or half a second later at 80×24 when none does.
func New(opts Options) (*Terminal, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	opts.Font.Features = append([]string(nil), opts.Font.Features...)
	t := &Terminal{opts: opts, done: make(chan struct{})}
	t.size = gridSize{80, 24, 8, 16}
	if opts.FixedCols > 0 && opts.FixedRows > 0 {
		t.size.cols, t.size.rows = opts.FixedCols, opts.FixedRows
	}
	var err error
	t.term, err = vt.NewTerminal(t.size.cols, t.size.rows, vt.Effects{
		WritePTY:     func(p []byte) { t.in.push(append([]byte(nil), p...)) },
		Bell:         func() { t.events.bells++ },
		TitleChanged: func() { t.events.title = true },
		RenderHold:   t.renderHold,
		Clipboard: func(text string, primary bool) bool {
			if !primary {
				t.clipOut = append(t.clipOut, text)
				t.redraw()
			}
			return !primary
		},
		Size: func() (int, int, int, int) { return t.size.cols, t.size.rows, t.size.cellW, t.size.cellH },
		Dark: func() bool { return t.curTheme == nil || t.curTheme.dark() },
		Notify: func(title, body string) {
			t.events.notify = append(t.events.notify, [2]string{title, body})
		},
	})
	if err != nil {
		return nil, err
	}
	if opts.ImageLimit >= 0 {
		limit := uint64(opts.ImageLimit)
		if limit == 0 {
			limit = 8 << 20
		}
		t.term.SetImageLimit(limit)
	}
	t.term.SetTerminfoName("xterm-256color")
	// Grapheme clusters take the cells of their width, as in Ghostty:
	// emoji with modifiers and joined emoji take two.
	t.term.SetModeDefault(vt.ModeGraphemes, true)
	switch {
	case opts.Scrollback < 0:
		t.term.SetScrollback(0)
	case opts.Scrollback > 0:
		t.term.SetScrollback(opts.Scrollback)
	default:
		t.term.SetScrollback(10 << 20)
	}
	t.term.SetDefaultCursor(vtCursor(opts.Cursor), !opts.NoBlink)
	if t.rs, err = vt.NewRenderState(); err != nil {
		t.term.Free()
		return nil, err
	}
	if opts.Conn != nil {
		t.src = opts.Conn
		t.run()
		return t, nil
	}
	path, args, err := command(opts.Command)
	if err != nil {
		t.rs.Free()
		t.term.Free()
		return nil, err
	}
	dir := opts.Dir
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	t.launch = &pty.Options{Path: path, Args: args, Dir: dir, Env: environment(opts.Env)}
	if runtime.GOOS == "windows" {
		// ConPTY keeps a screen of its own, which a resize pulling rows
		// back from the scrollback would disagree with.
		t.term.SetResizePullBack(false)
	}
	// The program starts once a view knows the size of the screen, so
	// that it draws its first screen at that size; a terminal no view
	// shows starts after half a second, at 80×24.
	t.mu.Lock()
	t.timer = time.AfterFunc(500*time.Millisecond, func() {
		t.mu.Lock()
		o := t.launch
		t.launch = nil
		t.mu.Unlock()
		t.start(o)
	})
	t.mu.Unlock()
	return t, nil
}

// run reads the output and writes the input of the program, or Conn.
func (t *Terminal) run() {
	go t.read()
	go t.in.run(t.src)
}

// start starts the program of launch, which the caller took, in a
// pseudo-terminal of the screen's size.
func (t *Terminal) start(o *pty.Options) {
	if o == nil {
		return
	}
	t.mu.Lock()
	s := t.size
	t.mu.Unlock()
	o.Cols, o.Rows, o.Width, o.Height = s.cols, s.rows, s.cols*s.cellW, s.rows*s.cellH
	proc, err := pty.Start(*o)
	if err != nil {
		t.write([]byte("\x1b[31m" + err.Error() + "\x1b[0m"))
		t.code.Store(-1)
		t.finish()
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		proc.Close()
		t.finish()
		return
	}
	t.proc, t.src = proc, proc
	if t.size != s {
		// Resized while it started.
		proc.Resize(t.size.cols, t.size.rows, t.size.cols*t.size.cellW, t.size.rows*t.size.cellH)
	}
	t.mu.Unlock()
	go func() {
		code, _ := proc.Wait()
		t.code.Store(int64(code))
		// What the program wrote before exiting is still to be read; then
		// hang up whatever else has the terminal, as terminal apps do.
		select {
		case <-t.done:
		case <-time.After(100 * time.Millisecond):
			proc.Close()
		}
	}()
	t.run()
}

// command returns the program to run and its arguments, with args[0] its
// name: the user's shell as a login shell ("-zsh") when cmd is empty.
func command(cmd []string) (path string, args []string, err error) {
	if len(cmd) > 0 {
		path, err = exec.LookPath(cmd[0])
		if err != nil {
			return "", nil, err
		}
		return path, cmd, nil
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe", os.Getenv("COMSPEC"), "cmd.exe"} {
			if p, err := exec.LookPath(name); name != "" && err == nil {
				return p, []string{name}, nil
			}
		}
		return "", nil, errors.New("terminal: no shell found")
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
	}
	return shell, []string{"-" + filepath.Base(shell)}, nil
}

// environment returns the environment of the program: this process's with
// the terminal's variables and extra.
func environment(extra []string) []string {
	env := os.Environ()
	set := func(kv string) {
		k, _, _ := strings.Cut(kv, "=")
		for i, e := range env {
			if strings.HasPrefix(e, k+"=") {
				env[i] = kv
				return
			}
		}
		env = append(env, kv)
	}
	set("TERM=xterm-256color")
	set("COLORTERM=truecolor")
	if runtime.GOOS != "windows" && os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" && os.Getenv("LC_CTYPE") == "" {
		set("LANG=en_US.UTF-8")
	}
	for _, kv := range extra {
		set(kv)
	}
	return env
}

// read processes the program's output until it ends.
func (t *Terminal) read() {
	buf := make([]byte, 64<<10)
	for {
		var n, cols, rows int
		var err error
		var snapshot []byte
		if src, ok := t.src.(interface {
			ReadScreenUpdate([]byte) (int, int, int, []byte, error)
		}); ok {
			n, cols, rows, snapshot, err = src.ReadScreenUpdate(buf)
		} else if src, ok := t.src.(interface {
			ReadScreen([]byte) (int, int, int, error)
		}); ok {
			n, cols, rows, err = src.ReadScreen(buf)
		} else {
			n, err = t.src.Read(buf)
		}
		if len(snapshot) > 0 {
			t.restoreSnapshot(snapshot, cols, rows)
			if err != nil {
				break
			}
			continue
		}
		if cols > 0 && rows > 0 {
			t.mu.Lock()
			if t.opts.FixedCols > 0 && t.opts.FixedRows > 0 {
				t.opts.FixedCols, t.opts.FixedRows = cols, rows
				size := t.size
				size.cols, size.rows = cols, rows
				t.resize(size)
			}
			t.mu.Unlock()
		}
		if n > 0 {
			t.write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	t.ended()
}

// write processes output, then runs the callbacks of what it did.
func (t *Terminal) write(p []byte) {
	t.mu.Lock()
	var ev events
	var title string
	if t.term != nil {
		t.term.Write(p)
		t.revision++
		if t.held && len(p) != 0 {
			// A slow but active redraw is not a stalled program. Only release
			// an unfinished update after incoming data has stopped.
			t.heldSince = time.Now()
		}
		ev, t.events = t.events, events{}
		if ev.title {
			title = t.term.Title()
		}
	}
	t.mu.Unlock()
	t.fire(ev, title)
	t.redraw()
}

func (t *Terminal) fire(ev events, title string) {
	o := &t.opts
	for _, event := range ev.render {
		if o.OnRenderEvent != nil {
			o.OnRenderEvent(event)
		}
	}
	if ev.title && o.OnTitle != nil {
		o.OnTitle(title)
	}
	for range ev.bells {
		if o.OnBell != nil {
			o.OnBell()
		}
	}
	for _, n := range ev.notify {
		if o.OnNotify != nil {
			o.OnNotify(n[0], n[1])
		}
	}
}

// ended marks the program ended, once its output did.
func (t *Terminal) ended() {
	t.closeSrc()
	if t.proc != nil {
		code, _ := t.proc.Wait()
		t.code.Store(int64(code))
	}
	t.mu.Lock()
	if t.term != nil && !t.closed {
		msg := "[Process exited]"
		if code := t.code.Load(); code != 0 {
			msg = fmt.Sprintf("[Process exited with code %d]", code)
		}
		if t.opts.Conn != nil {
			msg = "[Disconnected]"
		}
		if x, _ := t.term.Cursor(); x > 0 {
			msg = "\r\n" + msg
		}
		t.term.Write([]byte("\x1b[0;2m" + msg + "\x1b[0m"))
	}
	t.mu.Unlock()
	t.finish()
}

// finish closes Done and calls OnExit, once.
func (t *Terminal) finish() {
	t.doneOnce.Do(func() {
		close(t.done)
		t.in.close()
		if t.opts.OnExit != nil {
			t.opts.OnExit(int(t.code.Load()))
		}
		t.redraw()
	})
}

// Done is closed once the program exited (or Conn ended).
func (t *Terminal) Done() <-chan struct{} { return t.done }

// ExitCode returns the exit code of the program once it exited: 128 plus
// the signal's number for a program a signal killed, -1 for one that
// could not start.
func (t *Terminal) ExitCode() int { return int(t.code.Load()) }

// Title returns the title the program set (OSC 0, OSC 2).
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return ""
	}
	return t.term.Title()
}

// Dir returns the working directory the shell reported (OSC 7, which the
// shells of macOS report), or "".
func (t *Terminal) Dir() string {
	t.mu.Lock()
	pwd := ""
	if t.term != nil {
		pwd = t.term.Pwd()
	}
	t.mu.Unlock()
	return fileURLPath(pwd)
}

// SetFont changes the font, as Options.Font.
// set them, as for zooming.
func (t *Terminal) SetFont(f Font) {
	f.Features = append([]string(nil), f.Features...)
	t.mu.Lock()
	t.opts.Font = f
	t.mu.Unlock()
	t.redraw()
}

// SetTheme changes the colors, as Options.Theme and DarkTheme.
func (t *Terminal) SetTheme(theme, dark *Theme) {
	t.mu.Lock()
	t.opts.Theme, t.opts.DarkTheme = theme, dark
	t.mu.Unlock()
	t.redraw()
}

// Size returns the size of the screen in cells.
func (t *Terminal) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size.cols, t.size.rows
}

// Text returns the text of the screen and its scrollback, with the lines a
// program's long output wrapped joined.
func (t *Terminal) Text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return ""
	}
	return t.term.Text()
}

// SelectedText returns the selection in the grid currently shown to the user.
func (t *Terminal) SelectedText() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	screen := t.term
	if t.opts.ReflowView && !t.opts.FitToView && t.presentation != nil {
		screen = t.presentation
	}
	if screen == nil {
		return "", false
	}
	if t.opts.CopyRawText {
		return screen.SelectionText()
	}
	return screen.VisibleSelectionText()
}

// SelectionAnchor locates the last selection gesture in view DIPs. It is
// unavailable while the finger is dragging, so contextual actions do not
// obstruct selection. The caller places it relative to the view's bounds.
func (t *Terminal) SelectionAnchor() (ui.Rect, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.v == nil || t.v.selecting || t.v.screen() == nil {
		return ui.Rect{}, false
	}
	if _, selected := t.v.screen().SelectionText(); !selected {
		return ui.Rect{}, false
	}
	return ui.Rect{X: t.v.pointer[0], Y: t.v.pointer[1], W: 1, H: float32(t.v.cellH) / t.v.scale}, true
}

// ClearSelection removes the local selection without sending input to the
// program or changing the clipboard, viewport or terminal contents.
func (t *Terminal) ClearSelection() {
	t.mu.Lock()
	if t.term != nil {
		t.term.SetSelection(nil)
	}
	if t.presentation != nil {
		t.presentation.SetSelection(nil)
	}
	t.mu.Unlock()
	t.redraw()
}

// SelectAll selects the displayed screen and its available scrollback.
func (t *Terminal) SelectAll() {
	t.mu.Lock()
	if t.v != nil && t.v.screen() != nil {
		if selection, ok := t.v.screen().SelectAll(); ok {
			t.v.screen().SetSelection(&selection)
		}
	}
	t.mu.Unlock()
	t.redraw()
}

// SetCopyRawText changes selection copying without restarting the session.
func (t *Terminal) SetCopyRawText(raw bool) {
	t.mu.Lock()
	t.opts.CopyRawText = raw
	t.mu.Unlock()
}

func (t *Terminal) SetSelectOnDrag(selectOnDrag bool) {
	t.mu.Lock()
	t.opts.SelectOnDrag = selectOnDrag
	t.mu.Unlock()
}

// SetSelectOnDragInAltScreen changes whether SelectOnDrag includes full-screen
// programs, without restarting the session.
func (t *Terminal) SetSelectOnDragInAltScreen(selectOnDrag bool) {
	t.mu.Lock()
	t.opts.SelectOnDragInAltScreen = selectOnDrag
	t.mu.Unlock()
}

// SetAdaptiveColors changes rendering without altering the terminal's cells
// or restarting the program that owns them.
func (t *Terminal) SetAdaptiveColors(adaptive bool) {
	t.mu.Lock()
	changed := t.opts.AdaptiveColors != adaptive
	t.opts.AdaptiveColors = adaptive
	t.mu.Unlock()
	if changed {
		t.redraw()
	}
}

// Resize sets the size of the screen in cells, for a terminal no view
// shows, as one a server keeps for a session while no window does: a view
// sets the size of the terminal it shows.
func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resize(gridSize{max(cols, 1), max(rows, 1), t.size.cellW, t.size.cellH})
}

// Snapshot returns what the terminal shows as escape sequences: its
// scrollback and screen with their styles, the cursor and the modes
// programs set. Fed to a new terminal of the same size, it shows the same,
// as a terminal attaching again to a session a server keeps.
func (t *Terminal) Snapshot() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return nil
	}
	return t.term.VT()
}

// SnapshotRecent includes at most historyRows retained rows above the grid.
// A negative value requests only the visible grid.
func (t *Terminal) SnapshotRecent(historyRows int) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return nil
	}
	if historyRows < 0 {
		return t.term.VTScreen()
	}
	return t.term.VTRecent(historyRows)
}

// Send sends data to the program as if typed.
func (t *Terminal) Send(data []byte) {
	if len(data) > 0 {
		t.in.push(append([]byte(nil), data...))
	}
}

// SetInputEnabled keeps reading and selection available during recovery while
// discarding queued keystrokes. Commands typed offline are never replayed.
func (t *Terminal) SetInputEnabled(enabled bool) {
	t.in.mu.Lock()
	if !enabled {
		t.clearInputContext()
	}
	if t.in.paused != !enabled {
		t.in.paused = !enabled
		if !enabled {
			t.in.queue = nil
		}
	}
	t.in.mu.Unlock()
}

// Paste pastes text, bracketed when the program asks (mode 2004), so that
// it does not run as commands as it would typed.
func (t *Terminal) Paste(text string) {
	t.mu.Lock()
	if t.term == nil {
		t.mu.Unlock()
		return
	}
	data := vt.EncodePaste(text, t.term.Mode(vt.ModeBracketedPaste))
	t.term.ScrollToBottom()
	t.mu.Unlock()
	t.contextText(text)
	t.Send(data)
	t.redraw()
}

// SetFixedSize decodes the source at cols×rows, independently of the view,
// following the geometry of later screen frames, as FixedCols and
// FixedRows do; zero sizes the screen to the view again. A window showing
// a session whose size another device holds uses it.
func (t *Terminal) SetFixedSize(cols, rows int) {
	t.mu.Lock()
	if cols > 0 && rows > 0 {
		t.opts.FixedCols, t.opts.FixedRows = cols, rows
	} else {
		t.opts.FixedCols, t.opts.FixedRows = 0, 0
	}
	t.mu.Unlock()
	t.redraw()
}

// SetFitToView switches a fixed remote grid between fitting the view and
// its configured font size. Neither mode changes the remote program's size.
func (t *Terminal) SetFitToView(fit bool) {
	t.mu.Lock()
	t.opts.FitToView = fit
	t.mu.Unlock()
	t.redraw()
}

// Feed shows data as if the program wrote it: text and escape sequences.
func (t *Terminal) Feed(data []byte) { t.write(data) }

// Close closes the terminal: the program is hung up (SIGHUP), or Conn
// closed, and the terminal frees its memory. A view of it then shows
// nothing.
func (t *Terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.cancelLinkPreviewLocked()
	src, proc, waiting := t.src, t.proc, t.launch != nil
	t.launch = nil
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
	var err error
	switch {
	case src != nil:
		err = t.closeSrc()
	case waiting:
		// The program never started; one starting finishes itself.
		t.code.Store(-1)
		t.finish()
	}
	if proc != nil {
		// A program that ignores the hangup is killed after a moment.
		go func() {
			select {
			case <-proc.Exited():
			case <-time.After(2 * time.Second):
				proc.Kill()
			}
		}()
	}
	t.in.close()
	// The reader may be writing to the emulator, and a frame drawing it:
	// free it under the locks.
	t.mu.Lock()
	t.rmu.Lock()
	if t.search != nil {
		t.search.Free()
		t.search = nil
	}
	if t.presentation != nil {
		t.presentation.Free()
		t.presentation = nil
	}
	t.term.Free()
	t.term = nil
	t.rs.Free()
	t.rs = nil
	t.rmu.Unlock()
	t.mu.Unlock()
	t.redraw()
	return err
}

// closeSrc closes the program's pseudo-terminal, or Conn, once.
func (t *Terminal) closeSrc() error {
	t.srcOnce.Do(func() { t.srcErr = t.src.Close() })
	return t.srcErr
}

// resizeConn tells Conn the size of the screen, from a goroutine, the
// latest size only once it told the one before.
func (t *Terminal) resizeConn(c interface{ Resize(cols, rows int) error }, s gridSize) {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	t.connSize = s
	if t.connBusy {
		return
	}
	t.connBusy = true
	go func() {
		sent := gridSize{}
		for {
			t.connMu.Lock()
			s := t.connSize
			if s == sent {
				t.connBusy = false
				t.connMu.Unlock()
				return
			}
			t.connMu.Unlock()
			c.Resize(s.cols, s.rows)
			sent = s
		}
	}()
}

// resize sets the size of the screen, from the view; t.mu is held.
func (t *Terminal) resize(s gridSize) {
	if t.term == nil {
		return
	}
	if o := t.launch; o != nil {
		t.launch = nil
		t.timer.Stop()
		defer func() { go t.start(o) }() // at this size
	}
	if s == t.size {
		return
	}
	t.size = s
	t.revision++
	t.term.Resize(s.cols, s.rows, s.cellW, s.cellH)
	switch src := t.src.(type) {
	case *pty.PTY:
		src.Resize(s.cols, s.rows, s.cols*s.cellW, s.rows*s.cellH)
	case interface{ Resize(cols, rows int) error }:
		t.resizeConn(src, s)
	}
}

// renderHold starts or ends a program's synchronized update, during which
// frames show the screen as it was when it started; t.mu is held.
func (t *Terminal) renderHold(held bool) {
	t.rmu.Lock()
	if held {
		if !t.held {
			if t.rs != nil {
				if t.opts.ReflowView && !t.opts.FitToView {
					if t.presentation != nil {
						t.rs.Update(t.presentation)
					}
				} else {
					t.rs.Update(t.term)
				}
			}
			t.holdStarted = time.Now()
		}
		t.heldSince = time.Now()
	} else if t.held {
		if elapsed := time.Since(t.holdStarted); elapsed >= slowSyncThreshold {
			t.events.render = append(t.events.render, RenderEvent{Kind: "slow_sync", Duration: elapsed})
		}
	}
	t.held = held
	t.rmu.Unlock()
}

// redraw asks the view for a frame.
func (t *Terminal) redraw() {
	if f := t.draw.Load(); f != nil {
		(*f)()
	}
}

// fileURLPath returns the path of a file URL, as shells report their
// directory (file://host/path), or s when it is not one.
func fileURLPath(s string) string {
	rest, ok := strings.CutPrefix(s, "file://")
	if !ok {
		return s
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[i:]
	}
	return unescapePath(rest)
}

func unescapePath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := unhex(s[i+1]); ok {
				if w, ok := unhex(s[i+2]); ok {
					b.WriteByte(v<<4 | w)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// inputQueue writes what is typed to the program in order, from a
// goroutine of its own: a program that does not read its input must not
// block the reader, which answers its queries.
type inputQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  [][]byte
	closed bool
	paused bool
}

func (q *inputQueue) init() {
	if q.cond == nil {
		q.cond = sync.NewCond(&q.mu)
	}
}

func (q *inputQueue) push(p []byte) {
	q.mu.Lock()
	q.init()
	if !q.closed && !q.paused {
		q.queue = append(q.queue, p)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *inputQueue) close() {
	q.mu.Lock()
	q.init()
	q.closed = true
	q.queue = nil
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *inputQueue) run(w io.Writer) {
	for {
		q.mu.Lock()
		q.init()
		for len(q.queue) == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		p := q.queue[0]
		q.queue = q.queue[1:]
		q.mu.Unlock()
		if _, err := w.Write(p); err != nil {
			q.close()
			return
		}
	}
}

func vtCursor(s CursorStyle) vt.CursorStyle {
	switch s {
	case CursorBar:
		return vt.CursorBar
	case CursorUnderline:
		return vt.CursorUnderline
	}
	return vt.CursorBlock
}
