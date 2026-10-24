package terminal

import (
	"math"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal/internal/vt"
)

// View shows the terminal in a window of native UI, and takes its keyboard
// input once it has the focus, which a click gives it. Size it like any
// element, as with Fill or Grow; its size sets the terminal's, in cells of
// the font. A terminal shows in one view at a time.
//
// Copy and paste are Command+C and Command+V on macOS, which the Edit
// menu's roles also send, and Control+Shift+C and Control+Shift+V
// elsewhere; Shift+Page Up and Shift+Page Down scroll a page of the
// scrollback, as does the wheel, unless the program takes the mouse. Hold
// Shift to select while a program takes the mouse, and Command (Control
// elsewhere) to open a hyperlink (OSC 8) on click.
func View(c *ui.Context, t *Terminal) ui.Element {
	v := t.viewOf(c)
	// HandleTextInput connects the system's input methods, and is what makes
	// tapping the terminal raise the soft keyboard on Android: Focusable alone
	// only marks it as a focus target. Its client types what a method commits
	// — see textinput.go.
	//
	// It also sets the text cursor itself, so the pointer cursor a link
	// hovers below still wins: the element is built first, then the link
	// decides. Order matters — calling it after the hover check overwrote
	// CursorPointer with CursorText and a Command-hover over a URL stopped
	// showing the hand (links_test.go caught it).
	e := ui.Box(c).Focusable().FocusRing(false).Cursor(ui.CursorText).Clip().Role(ui.RoleTextField).Label("Terminal")
	e.HandleTextInput(v.client())
	e.Cursor(ui.CursorText)
	v.build(c, e)
	x, y, over := e.PointerPosition()
	v.hover = linkMatch{}
	var previewLink Link
	if over && v.cellW != 0 && (v.linkMods&ui.Cmd != 0 || t.opts.OpenLinksOnTap || t.opts.OnPreviewLink != nil) {
		t.mu.Lock()
		match := v.linkAt(x, y)
		t.mu.Unlock()
		previewLink = match.Link
		if v.linkMods&ui.Cmd != 0 || t.opts.OpenLinksOnTap {
			v.hover = match
		}
		if v.hover.valid() {
			e.Cursor(ui.CursorPointer)
			label := v.hover.URL
			if label == "" {
				label = v.hover.Path
			}
			prefix := "⌘ 点击打开 "
			if t.opts.OpenLinksOnTap {
				prefix = "点击打开 "
			}
			e.Tooltip(prefix + label)
		}
	}
	v.previewLink(c, e, previewLink, x, y)
	e.HandleInput(v.input).TouchSelection()
	e.TextCaretFunc(v.caret)
	if t.opts.InputContext {
		e.TextContext(t.textContext)
	}
	e.Draw(v.paint)
	e.ContextMenu(v.menu)
	return e
}

// viewOf returns the terminal's view state, made on the first frame
// showing it.
func (t *Terminal) viewOf(c *ui.Context) *view {
	if t.v == nil {
		t.v = &view{t: t, blinkStart: time.Now()}
		t.v.shaped.init(4096)
	}
	v := t.v
	if v.services != c.Services() {
		v.services = c.Services()
		draw := v.services.Invalidate
		t.draw.Store(&draw)
	}
	return v
}

// view is what a frame of the terminal needs from frame to frame. Main
// thread only.
type view struct {
	images        []shownImage
	bitmaps       map[uint32]*cachedImage
	imageTerminal *vt.Terminal
	imageReset    uint64
	preview       *linkPreview
	t             *Terminal
	services      ui.Services

	theme              *Theme
	applied            *Theme // the theme the emulator has
	adaptiveColors     bool
	oppositeBackground ui.Color

	// The grid, in device pixels: the cells, the baseline in a cell, and
	// the origin of the grid relative to the element; scale is device
	// pixels per DIP.
	font                             fontKey
	fonts                            [4]ui.Font
	fitKey                           fitFontKey
	fittedFont                       fontKey
	scale                            float32
	cellW, cellH, baseline           int
	ox, oy                           int
	painted                          ui.Rect // the element's box when last painted
	cols, rows                       int
	fixedGrid, followCursor, reflow  bool
	panX, panY, viewportW, viewportH int

	colors        vt.Colors
	cursor        vt.Cursor
	scrollbar     vt.Scrollbar
	searchMatches []vt.Selection
	searchRects   []vt.MatchRect
	lines         []rowCache
	rowScratch    rowBuildScratch
	shaped        lru
	contrast      map[contrastPair]ui.Color

	keys    *vt.KeyEncoder
	mouse   *vt.MouseEncoder
	gesture *vt.Gesture

	// pending is a key that types text, until the text comes; preedit
	// is an input method's composition and its caret.
	pending      *pendingKey
	preedit      string
	preeditCaret int
	// ti is the text-input target the system's input methods edit, whose
	// text is typed into the terminal rather than stored. See
	// textinput.go for why having one is what raises the keyboard.
	ti *textInput

	focused    bool
	blinkStart time.Time
	caretCell  [2]int // the cursor's cell, for input methods
	// selecting tells that the primary button selects; reporting that a
	// press went to the program, which gets the release too.
	selecting, reporting bool
	touchSelecting       bool
	// A tracked primary click is deferred until release so that a drag
	// can stay local instead of starting the program's own text selection.
	mousePress  *ui.InputEvent
	pointer     [2]float32
	scrolled    float32 // scrolling not yet a whole row
	ticking     bool    // an autoscroll tick is due
	linkMods    ui.Modifiers
	linkPressed bool
	linkTap     *linkTap
	hover       linkMatch
}

type linkTap struct {
	link      linkMatch
	x, y      float32
	cancelled bool
}

func (tap *linkTap) moved(x, y float32) bool {
	return math.Abs(float64(x-tap.x)) > 8 || math.Abs(float64(y-tap.y)) > 8
}

type pendingKey struct {
	key       vt.Key
	unshifted rune
	mods      ui.Modifiers
	repeat    bool
}

// build updates the view as a frame builds.
func (v *view) build(c *ui.Context, e ui.Element) {
	t := v.t
	dark := c.Theme().Dark
	focused := e.Focused()
	t.mu.Lock()
	theme := t.opts.Theme
	if dark && t.opts.DarkTheme != nil {
		theme = t.opts.DarkTheme
	} else if theme == nil {
		theme = defaultTheme(dark)
	}
	v.theme = theme
	if adaptive := t.opts.AdaptiveColors; adaptive != v.adaptiveColors {
		v.adaptiveColors = adaptive
		clear(v.contrast)
		v.lines = nil
	}
	opposite := t.opts.DarkTheme
	if theme.dark() {
		opposite = t.opts.Theme
	}
	if opposite == nil || opposite.dark() == theme.dark() {
		opposite = defaultTheme(!theme.dark())
	}
	v.oppositeBackground = opposite.Background
	if t.term == nil {
		v.release()
	} else {
		if v.applied != theme {
			t.revision++
			theme.apply(t.term)
			v.applied = theme
			clear(v.contrast)
			v.lines = nil
		}
		t.curTheme = theme
		if focused != v.focused && t.term.Mode(vt.ModeFocusEvent) {
			t.in.push(vt.EncodeFocus(focused))
		}
		// Input methods compose where the cursor is now, which the last
		// frame may not show yet.
		v.caretCell = [2]int{v.cursor.X, v.cursor.Y}
		if v.screen().AtBottom() {
			x, y := v.screen().Cursor()
			v.caretCell = [2]int{x, y}
		}
	}
	copied := t.clipOut
	t.clipOut = nil
	var holdDue time.Time
	if t.term != nil && t.held {
		holdDue = t.heldSince.Add(syncOutputTimeout)
	}
	t.mu.Unlock()
	if !holdDue.IsZero() {
		// The watchdog must also wake a window with a hidden cursor and no
		// further output; otherwise an incomplete update could stay frozen.
		c.After(max(holdDue.Sub(c.Now()), time.Millisecond))
	}
	for _, s := range copied {
		c.WriteClipboard(s)
	}
	if focused != v.focused {
		v.focused = focused
		v.blinkStart = c.Now()
		if !focused {
			// Input methods drop their composition with the focus.
			v.preedit, v.pending = "", nil
			v.linkMods, v.linkPressed = 0, false
			v.linkTap = nil
			if v.mousePress != nil {
				v.mousePress = nil
				v.selecting, v.ticking = false, false
			}
		}
	}
	// The cursor blinks, every 600 ms, while the terminal has the focus.
	if (v.focused || t.opts.ActiveCursor) && v.cursor.Blinking && v.cursor.Visible {
		since := c.Now().Sub(v.blinkStart)
		c.After(blinkPeriod - since%blinkPeriod)
	}
	if v.ticking {
		c.After(40 * time.Millisecond)
	}
}

const blinkPeriod = 600 * time.Millisecond

// release frees what the view made of the library, once the terminal is
// closed.
func (v *view) release() {
	if v.keys != nil {
		v.keys.Free()
		v.keys = nil
	}
	if v.mouse != nil {
		v.mouse.Free()
		v.mouse = nil
	}
	if v.gesture != nil {
		v.gesture.Free(nil)
		v.gesture = nil
	}
}

// caret returns where input methods compose: the cursor's cell.
func (v *view) caret() ui.Rect {
	if v.scale == 0 {
		return ui.Rect{}
	}
	cw, ch := float32(v.cellW)/v.scale, float32(v.cellH)/v.scale
	x := float32(v.ox)/v.scale + float32(v.caretCell[0])*cw
	y := float32(v.oy)/v.scale + float32(v.caretCell[1])*ch
	return ui.Rect{X: x, Y: y, W: cw, H: ch}
}

// menu is the terminal's context menu.
func (v *view) menu(m *ui.Menu) {
	t := v.t
	cmd := ui.Cmd
	if runtime.GOOS != "darwin" {
		cmd |= ui.Shift
	}
	t.mu.Lock()
	text, selected := "", false
	if t.term != nil {
		text, selected = v.selectionText()
	}
	t.mu.Unlock()
	if m.Item("Copy").Shortcut(cmd, ui.KeyC).Disabled(!selected || text == "").Chosen() {
		v.services.WriteClipboard(text)
	}
	if m.Item("Paste").Shortcut(cmd, ui.KeyV).Chosen() {
		v.paste()
	}
	if m.Item("Select All").Chosen() {
		v.selectAll()
	}
	m.Separator()
	if m.Item("Clear Scrollback").Chosen() {
		t.Feed([]byte("\x1b[3J"))
	}
	if t.opts.OnSplit != nil {
		m.Separator()
		if m.Item("Split Pane Vertically").Shortcut(ui.Cmd, ui.KeyD).Chosen() {
			t.opts.OnSplit(false)
		}
		if m.Item("Split Pane Horizontally").Shortcut(ui.Cmd|ui.Shift, ui.KeyD).Chosen() {
			t.opts.OnSplit(true)
		}
	}
}

// input handles the input of the view as it comes.
func (v *view) input(ev ui.InputEvent) bool {
	v.t.in.mu.Lock()
	paused := v.t.in.paused
	v.t.in.mu.Unlock()
	if paused && (ev.Kind == ui.InputKeyDown || ev.Kind == ui.InputKeyUp || ev.Kind == ui.InputText || ev.Kind == ui.InputCompose || ev.Kind == ui.InputTextReplace || ev.Kind == ui.InputSelection || ev.Kind == ui.InputCommand && ev.Text == "paste") {
		v.pending, v.preedit = nil, ""
		return true
	}
	if ev.Kind == ui.InputKeyDown || ev.Kind == ui.InputKeyUp || ev.Kind == ui.InputPointerMove || ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerUp {
		v.linkMods = ev.Mods
	}
	switch ev.Kind {
	case ui.InputKeyDown:
		return v.keyDown(ev)
	case ui.InputKeyUp:
		return v.keyUp(ev)
	case ui.InputText:
		if ev.Mods != 0 {
			v.modifiedText(ev.Text, ev.Mods)
		} else {
			v.typed(ev.Text)
		}
		return true
	case ui.InputTextReplace:
		v.replaceInput(ev)
		return true
	case ui.InputSelection:
		v.moveInputCaret(ev.Caret)
		return true
	case ui.InputCompose:
		v.preedit, v.preeditCaret, v.pending = ev.Text, ev.Caret, nil
		return true
	case ui.InputCommand:
		switch ev.Text {
		case "copy":
			v.copy()
		case "paste":
			v.paste()
		case "selectAll":
			v.selectAll()
		default:
			return false
		}
		return true
	case ui.InputPointerDown, ui.InputPointerUp, ui.InputPointerMove, ui.InputPointerCancel, ui.InputLongPress:
		return v.pointerEvent(ev)
	case ui.InputScroll:
		if v.linkTap != nil {
			v.linkTap.cancelled = true
		}
		return v.scroll(ev)
	}
	return false
}

func (v *view) keyDown(ev ui.InputEvent) bool {
	t := v.t
	mac := runtime.GOOS == "darwin"
	mods, key := ev.Mods, ev.Key
	copyMods := ui.Ctrl | ui.Shift
	if mac {
		copyMods = ui.Super
	}
	switch {
	case mods == copyMods && key == ui.KeyC:
		v.copy()
		return true
	case mods == copyMods && key == ui.KeyV:
		v.paste()
		return true
	case runtime.GOOS == "ios" && mods == ui.Ctrl && key == ui.KeyV && t.opts.OnPaste != nil:
		v.paste()
		return true
	case runtime.GOOS != "ios" && mods == ui.Ctrl && key == ui.KeyV && t.opts.OnPaste != nil && t.opts.OnPaste(v.services):
		return true // A host may transfer a clipboard image before raw Ctrl+V.
	case mods == ui.Shift && (key == ui.KeyPageUp || key == ui.KeyPageDown) && v.scrollPage(key == ui.KeyPageUp):
		return true
	case mods&ui.Super != 0 && !ev.Software:
		return false // the app's shortcuts
	}
	k, unshifted := vtKey(key)
	if k == vt.KeyUnidentified {
		return false
	}
	altMeta := mods&ui.Alt != 0 && (!mac || t.opts.OptionAsAlt)
	// AltGr types text on Windows, where it is Control and Alt.
	altGr := runtime.GOOS == "windows" && mods&(ui.Ctrl|ui.Alt) == ui.Ctrl|ui.Alt
	if typesText(key) && (mods&(ui.Ctrl|ui.Alt) == 0 || mods&ui.Alt != 0 && !altMeta && mods&ui.Ctrl == 0 || altGr) {
		// The text it types comes next.
		v.pending = &pendingKey{key: k, unshifted: unshifted, mods: mods, repeat: ev.Repeat}
		return true
	}
	v.pending = nil
	text := ""
	if altMeta && typesText(key) && mods&ui.Ctrl == 0 {
		// Alt as Meta: the character, which the encoder prefixes with Esc.
		text = string(unshifted)
		if mods&ui.Shift != 0 {
			text = shiftedText(key, unshifted)
		}
	}
	action := vt.KeyPress
	if ev.Repeat {
		action = vt.KeyRepeat
	}
	t.contextKey(k, mods, text)
	v.sendKey(vt.KeyEvent{Action: action, Key: k, Mods: vtMods(mods), Text: text, Unshifted: unshifted})
	return true
}

// scrollPage scrolls the scrollback a page, but for full-screen programs,
// which get the key, and reports whether it did.
func (v *view) scrollPage(up bool) bool {
	t := v.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if v.screen() == nil || v.screen().AltScreen() {
		return false
	}
	_, rows := v.screen().Size()
	page := max(rows-1, 1)
	if up {
		page = -page
	}
	v.screen().ScrollBy(page)
	return true
}

func (v *view) keyUp(ev ui.InputEvent) bool {
	k, unshifted := vtKey(ev.Key)
	if k == vt.KeyUnidentified || ev.Mods&ui.Super != 0 && !ev.Software {
		return false
	}
	if p := v.pending; p != nil && p.key == k {
		// No text came, as for Control, Alt and a letter on Windows,
		// where they may type one: the key alone.
		v.pending = nil
		action := vt.KeyPress
		if p.repeat {
			action = vt.KeyRepeat
		}
		text := ""
		if p.mods&ui.Ctrl == 0 {
			text = string(p.unshifted) // what the key types on a US keyboard
			if p.mods&ui.Shift != 0 {
				text = shiftedText(ev.Key, p.unshifted)
			}
		}
		v.sendKey(vt.KeyEvent{Action: action, Key: p.key, Mods: vtMods(p.mods), Text: text, Unshifted: p.unshifted})
	}
	handled := v.encodeKey(vt.KeyEvent{Action: vt.KeyRelease, Key: k, Mods: vtMods(ev.Mods), Unshifted: unshifted}, false)
	if ev.Key == ui.KeyEnter && ev.Mods == 0 && v.t.opts.OnSubmit != nil {
		v.t.opts.OnSubmit(v.services)
		return true
	}
	return handled
}

// typed sends text typed or committed by an input method.
func (v *view) typed(text string) {
	if text == "" {
		v.preedit = ""
		return
	}
	v.t.contextText(text)
	v.preedit = ""
	p := v.pending
	v.pending = nil
	// A software keyboard's Return arrives as a committed newline, not as a
	// key: an input method has no KeyEvent for it. Sent as text it would reach
	// the program as a raw line feed, which leaves a shell waiting — Enter is
	// CR at a prompt, and a full-screen program reads LF as Control+J. The key
	// encoder knows the difference, so a newline is sent as the key it is.
	//
	// Only a whole newline converts. One inside a longer commit (a pasted
	// block, a finished composition) is text the program asked for and stays
	// part of it, which is why this runs before the multi-rune branch below.
	if p == nil && (text == "\n" || text == "\r") {
		v.sendKey(vt.KeyEvent{Action: vt.KeyPress, Key: vt.KeyEnter})
		return
	}
	// 🔴 Committed text with no key and no modifier is sent as text.
	//
	// It used to be inferred: the character was mapped back to the key that
	// would type it (keyOfRune) and sent through the encoder as a key press.
	// That is right when a key and its text are one act, and wrong for an
	// input method, which commits text with no key at all — the inference
	// threw away whatever the key could not express. One commit of "abcde"
	// went out as a single key press carrying one character, and the other
	// four never reached the program. That is the input loss this fixes.
	//
	// A modifier never reaches this function: input() sends text that carries
	// one to modifiedText, which is where Ctrl+c becomes Control+C. So text
	// arriving here with no pending key is text, and is sent as text.
	if p == nil {
		v.sendText(text)
		return
	}
	if p == nil {
		r, size := utf8.DecodeRuneInString(text)
		if size != len(text) || r == utf8.RuneError {
			v.sendText(text)
			return
		}
		k, unshifted, shift := keyOfRune(r)
		mods := ui.Modifiers(0)
		if shift {
			mods = ui.Shift
		}
		p = &pendingKey{key: k, unshifted: unshifted, mods: mods}
	}
	if utf8.RuneCountInString(text) > 1 {
		v.sendText(text)
		return
	}
	var consumed vt.Mods
	r, _ := utf8.DecodeRuneInString(text)
	if r != p.unshifted {
		// Shift or Option made the character.
		consumed = vtMods(p.mods & (ui.Shift | ui.Alt))
	}
	action := vt.KeyPress
	if p.repeat {
		action = vt.KeyRepeat
	}
	v.sendKey(vt.KeyEvent{Action: action, Key: p.key, Mods: vtMods(p.mods), Consumed: consumed, Text: text, Unshifted: p.unshifted})
}

// sendKey sends a key, scrolling to the bottom and clearing the selection
// as Ghostty does when it sends something.
func (v *view) sendKey(k vt.KeyEvent) {
	if v.encodeKey(k, true) {
		v.blinkStart = time.Now()
	}
}

func (v *view) encodeKey(k vt.KeyEvent, press bool) bool {
	t := v.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return false
	}
	if v.keys == nil {
		var err error
		if v.keys, err = vt.NewKeyEncoder(); err != nil {
			return false
		}
	}
	v.keys.Sync(t.term, t.opts.OptionAsAlt)
	b := v.keys.Encode(k)
	if len(b) == 0 {
		return false
	}
	t.in.push(append([]byte(nil), b...))
	if press {
		v.screen().ScrollToBottom()
		v.screen().SetSelection(nil)
	}
	return true
}

// sendText sends text that is no key, as of an input method.
func (v *view) sendText(text string) {
	t := v.t
	t.mu.Lock()
	if t.term != nil {
		v.screen().ScrollToBottom()
		v.screen().SetSelection(nil)
	}
	t.mu.Unlock()
	t.Send([]byte(text))
	v.blinkStart = time.Now()
}

func (v *view) copy() {
	t := v.t
	t.mu.Lock()
	text, ok := "", false
	if t.term != nil {
		text, ok = v.selectionText()
	}
	t.mu.Unlock()
	if ok && text != "" {
		v.services.WriteClipboard(text)
	}
}

func (v *view) paste() {
	if v.t.opts.OnPaste != nil && v.t.opts.OnPaste(v.services) {
		return
	}
	if text := v.services.ReadClipboard(); text != "" {
		v.t.Paste(text)
	}
}

// selectionText is shared by automatic, keyboard and context-menu copying.
// The caller holds the terminal lock.
func (v *view) selectionText() (string, bool) {
	if v.t.opts.CopyRawText {
		return v.screen().SelectionText()
	}
	return v.screen().VisibleSelectionText()
}

func (v *view) selectAll() {
	t := v.t
	t.mu.Lock()
	text := ""
	if v.screen() != nil {
		if s, ok := v.screen().SelectAll(); ok {
			v.screen().SetSelection(&s)
			text, _ = v.selectionText()
		}
	}
	t.mu.Unlock()
	if text != "" {
		v.services.WriteClipboard(text)
	}
	v.services.Invalidate()
}

// cellAt returns the cell under a point of the element, in DIPs, within
// the grid.
func (v *view) cellAt(x, y float32) (col, row int) {
	if v.cellW == 0 {
		return 0, 0
	}
	col = int(math.Floor(float64((x*v.scale - float32(v.ox)) / float32(v.cellW))))
	row = int(math.Floor(float64((y*v.scale - float32(v.oy)) / float32(v.cellH))))
	return max(0, min(col, v.cols-1)), max(0, min(row, v.rows-1))
}

// pointerEvent reports the pointer to the program that takes the mouse, or
// selects.
func (v *view) pointerEvent(ev ui.InputEvent) bool {
	t := v.t
	t.mu.Lock()
	v.pointer = [2]float32{ev.X, ev.Y}
	var copied string
	var opened Link
	defer func() {
		t.mu.Unlock()
		if opened.URL != "" || opened.Path != "" {
			if t.opts.OnOpenLink != nil {
				t.opts.OnOpenLink(opened)
			} else if opened.URL != "" {
				v.services.OpenURL(opened.URL)
			}
		}
		if copied != "" {
			v.services.WriteClipboard(copied)
		}
	}()
	if v.screen() == nil || v.cellW == 0 {
		return false
	}
	if ev.Kind == ui.InputPointerCancel {
		if v.reporting {
			ev.Kind = ui.InputPointerUp
			v.report(ev)
		}
		v.selecting, v.ticking, v.linkPressed = false, false, false
		v.touchSelecting = false
		v.linkTap = nil
		v.mousePress = nil
		if v.gesture != nil {
			v.gesture.Reset(v.screen())
		}
		return true
	}
	if v.linkPressed {
		if ev.Kind == ui.InputPointerUp && ev.Button == 0 {
			v.linkPressed = false
		}
		return true
	}
	if tap := v.linkTap; tap != nil {
		switch ev.Kind {
		case ui.InputLongPress:
			v.linkTap = nil // Continue into ordinary word selection.
		case ui.InputPointerMove:
			tap.cancelled = tap.cancelled || tap.moved(ev.X, ev.Y)
			return true
		case ui.InputPointerUp:
			v.linkTap = nil
			if ev.Button == 0 && !tap.cancelled && !tap.moved(ev.X, ev.Y) && v.linkAt(ev.X, ev.Y).Link == tap.link.Link {
				opened = tap.link.Link
			}
			return true
		}
	}
	if t.opts.SelectOnDrag && ev.Mods&ui.Alt == 0 && ev.Button == 1 && (ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputPointerUp) {
		return false // Agent panes retain the terminal's Copy/Split menu.
	}
	if ev.Kind == ui.InputPointerDown && ev.Button == 0 && ev.Mods&ui.Cmd != 0 {
		if link := v.linkAt(ev.X, ev.Y); link.valid() {
			opened, v.linkPressed = link.Link, true
			return true
		}
	}
	if t.opts.OpenLinksOnTap && ev.Kind == ui.InputPointerDown && ev.Button == 0 && ev.Mods == 0 {
		if link := v.linkAt(ev.X, ev.Y); link.URL != "" || link.Path != "" && t.opts.OnOpenLink != nil {
			v.linkTap = &linkTap{link: link, x: ev.X, y: ev.Y}
			return true
		}
	}
	longPress := ev.Kind == ui.InputLongPress
	if longPress {
		v.touchSelecting = true
	}
	tracking := !v.touchSelecting && !v.reflow && v.screen().MouseTracking() && ev.Mods&ui.Shift == 0
	// Preserve application-owned history unless the caller explicitly prefers
	// local selection in full-screen programs, such as editors.
	localDrag := t.opts.SelectOnDrag && (!v.screen().AltScreen() || t.opts.SelectOnDragInAltScreen)
	preferDrag := localDrag && ev.Mods&ui.Alt == 0 && ev.Kind == ui.InputPointerDown && ev.Button == 0
	if tracking && !v.selecting && !preferDrag || v.reporting {
		return v.report(ev)
	}
	if v.gesture == nil {
		g, err := vt.NewGesture(uint64(500*time.Millisecond), 5*float64(v.scale))
		if err != nil {
			return false
		}
		v.gesture = g
	}
	col, row := v.cellAt(ev.X, ev.Y)
	px, py := float64(ev.X*v.scale), float64(ev.Y*v.scale)
	switch ev.Kind {
	case ui.InputPointerDown, ui.InputLongPress:
		if ev.Button != 0 {
			return false // the context menu
		}
		ref, ok := v.screen().CellAt(col, row)
		if !ok {
			return false
		}
		v.selecting = true
		v.mousePress = nil
		if tracking && preferDrag {
			press := ev
			v.mousePress = &press
		}
		press := v.gesture.Press
		if longPress {
			press = v.gesture.Word
		}
		if s, ok := press(v.screen(), ref, px, py, uint64(time.Now().UnixNano())); ok {
			v.screen().SetSelection(&s)
			v.mousePress = nil // Double/triple clicks select words/lines.
		} else {
			v.screen().SetSelection(nil)
		}
		return true
	case ui.InputPointerMove:
		if !v.selecting {
			return false
		}
		ref, ok := v.screen().CellAt(col, row)
		if !ok {
			return true
		}
		px, py = v.selectionPosition(ev.X, ev.Y)
		if s, ok := v.gesture.Drag(v.screen(), ref, px, py, v.geometry(), ev.Mods&ui.Alt != 0); ok {
			v.screen().SetSelection(&s)
		}
		if v.gesture.Dragged(v.screen()) {
			v.mousePress = nil
		}
		v.ticking = v.gesture.Autoscroll(v.screen()) != 0
		return true
	case ui.InputPointerUp:
		if !v.selecting || ev.Button != 0 {
			return false
		}
		v.selecting, v.ticking = false, false
		v.touchSelecting = false
		ref, ok := v.screen().CellAt(col, row)
		if ok {
			// A quick drag can reach its final cell in the release event
			// before a motion event was delivered. Don't turn it into a
			// program click or lose the final selected column.
			if v.mousePress != nil {
				if s, ok := v.gesture.Drag(v.screen(), ref, px, py, v.geometry(), false); ok {
					v.screen().SetSelection(&s)
				}
				if v.gesture.Dragged(v.screen()) {
					v.mousePress = nil
				}
			}
			v.gesture.Release(v.screen(), &ref)
		} else {
			v.gesture.Release(v.screen(), nil)
		}
		if press := v.mousePress; press != nil {
			v.mousePress = nil
			v.report(*press)
			return v.report(ev)
		}
		// Copy only a completed, nonempty mouse selection. A plain click
		// or an empty selection leaves the user's clipboard intact.
		copied, _ = v.selectionText()
		return true
	}
	return false
}

// geometry is the grid in pixels, for selection gestures.
func (v *view) geometry() vt.Geometry {
	return vt.Geometry{Columns: v.cols, CellWidth: v.cellW, PadLeft: v.ox, Height: v.oy + v.rows*v.cellH}
}

// Reaching a visible viewport edge should scroll a drag even when the window
// prevents the pointer from leaving the grid. Only the gesture position is
// projected outside; hit testing and the selection anchor use the real point.
func (v *view) selectionPosition(x, y float32) (float64, float64) {
	px, py := float64(x*v.scale), float64(y*v.scale)
	top := max(v.oy, 0)
	bottom := min(v.oy+v.rows*v.cellH, int(math.Round(padY*float64(v.scale)))+v.viewportH)
	edge := min(int(math.Ceil(8*float64(v.scale))), max((bottom-top)/3, 1))
	if py < float64(top+edge) {
		py = -1
	} else if py > float64(bottom-edge) {
		py = float64(v.geometry().Height + 1)
	}
	return px, py
}

// autoscroll scrolls a selection dragged past the top or the bottom; t.mu
// is held.
func (v *view) autoscroll() {
	dir := v.gesture.Autoscroll(v.screen())
	if dir == 0 || !v.selecting {
		v.ticking = false
		return
	}
	v.screen().ScrollBy(dir)
	col, row := v.cellAt(v.pointer[0], v.pointer[1])
	px, py := v.selectionPosition(v.pointer[0], v.pointer[1])
	if s, ok := v.gesture.Tick(v.screen(), col, row, px, py, v.geometry(), false); ok {
		v.screen().SetSelection(&s)
	}
}

// report reports the pointer to the program; t.mu is held.
func (v *view) report(ev ui.InputEvent) bool {
	t := v.t
	if v.mouse == nil {
		m, err := vt.NewMouseEncoder()
		if err != nil {
			return false
		}
		v.mouse = m
	}
	button := []int{vt.ButtonLeft, vt.ButtonRight, vt.ButtonMiddle}
	var action vt.MouseAction
	b := vt.ButtonNone
	switch ev.Kind {
	case ui.InputPointerDown:
		action = vt.MousePress
		v.reporting = true
	case ui.InputPointerUp:
		action = vt.MouseRelease
		v.reporting = false
	default:
		action = vt.MouseMotion
	}
	if ev.Button >= 0 && ev.Button < len(button) {
		b = button[ev.Button]
	}
	pressed := v.reporting
	v.mouse.Sync(t.term, v.ox*2+v.cols*v.cellW, v.oy*2+v.rows*v.cellH, v.cellW, v.cellH, v.ox, v.oy, pressed)
	out := v.mouse.Encode(action, b, vtMods(ev.Mods), ev.X*v.scale, ev.Y*v.scale)
	if len(out) > 0 {
		t.in.push(append([]byte(nil), out...))
	}
	return ev.Kind != ui.InputPointerMove || len(out) > 0
}

// scroll scrolls the scrollback, or reports the wheel to the program.
func (v *view) scroll(ev ui.InputEvent) bool {
	t := v.t
	if v.cellH == 0 {
		return false
	}
	if v.fixedGrid {
		oldX, oldY := v.panX, v.panY
		v.panX = min(max(v.panX+int(math.Round(float64(ev.DX*v.scale))), 0), max(v.cols*v.cellW-v.viewportW, 0))
		v.panY = min(max(v.panY+int(math.Round(float64(ev.DY*v.scale))), 0), max(v.rows*v.cellH-v.viewportH, 0))
		if oldX != v.panX || oldY != v.panY {
			v.ox += oldX - v.panX
			v.oy += oldY - v.panY
			return true
		}
	}
	ch := float32(v.cellH) / v.scale
	dy := ev.DY
	if !ev.Precise {
		dy = dy / 40 * 3 * ch // a notch scrolls three rows
	}
	v.scrolled += dy
	rows := int(v.scrolled / ch)
	v.scrolled -= float32(rows) * ch
	if rows == 0 {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil {
		return false
	}
	if v.reflow {
		screen := v.screen()
		before := screen.Scrollbar().Offset
		screen.ScrollBy(rows)
		if !t.term.AltScreen() || screen.Scrollbar().Offset != before {
			return true
		}
		// At the local edge, let a full-screen program load more content.
	}
	switch {
	case t.term.MouseTracking() && ev.Mods&ui.Shift == 0:
		if v.mouse == nil {
			m, err := vt.NewMouseEncoder()
			if err != nil {
				return false
			}
			v.mouse = m
		}
		mx, my := ev.X*v.scale, ev.Y*v.scale
		if v.reflow {
			// Projected cells do not correspond to application coordinates.
			// Scroll the body at the center of its original grid instead.
			w, h := t.size.cols*v.cellW, t.size.rows*v.cellH
			v.mouse.Sync(t.term, w, h, v.cellW, v.cellH, 0, 0, false)
			mx, my = float32(w)/2, float32(h)/2
		} else {
			v.mouse.Sync(t.term, v.ox*2+v.cols*v.cellW, v.oy*2+v.rows*v.cellH, v.cellW, v.cellH, v.ox, v.oy, false)
		}
		button := vt.WheelDown
		if rows < 0 {
			button = vt.WheelUp
		}
		for range abs(rows) {
			if out := v.mouse.Encode(vt.MousePress, button, vtMods(ev.Mods), mx, my); len(out) > 0 {
				t.in.push(append([]byte(nil), out...))
			}
		}
	case t.term.AltScreen() && t.term.Mode(vt.ModeAltScroll):
		// Full-screen programs scroll with the arrows (mode 1007).
		if v.keys == nil {
			var err error
			if v.keys, err = vt.NewKeyEncoder(); err != nil {
				return false
			}
		}
		v.keys.Sync(t.term, false)
		key := vt.KeyArrowDown
		if rows < 0 {
			key = vt.KeyArrowUp
		}
		for range abs(rows) {
			t.in.push(append([]byte(nil), v.keys.Encode(vt.KeyEvent{Action: vt.KeyPress, Key: key})...))
		}
	default:
		t.term.ScrollBy(rows)
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// shiftedText returns what a key types with Shift on a US keyboard.
func shiftedText(k ui.Key, unshifted rune) string {
	for r, key := range shifted {
		if key == k {
			return string(r)
		}
	}
	if 'a' <= unshifted && unshifted <= 'z' {
		return string(unshifted - 'a' + 'A')
	}
	return string(unshifted)
}
