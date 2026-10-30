package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

// Gesture modes of the remote screen, as touch modes of remote desktops on a
// phone have them. Pointing, the default, clicks where a finger taps, moves
// the zoomed picture where one slides, right-clicks where it rests, and
// drags the Mac's button when it rests then slides. As a trackpad, a finger
// moves the Mac's pointer from wherever it lands and a tap clicks there. In
// either, two fingers scroll the Mac by sliding and zoom by pinching.
const (
	screenModePoint = iota
	screenModeTrackpad
)

const (
	// screenHold is how long a still press waits before it holds the button.
	screenHold = 350 * time.Millisecond
	// screenRightHold is how long a still finger rests for a right click.
	screenRightHold = 500 * time.Millisecond
	// screenTapRepeat is how soon a tap after another counts as its double.
	screenTapRepeat = 400 * time.Millisecond
	// screenSpeed is how far the pointer moves for a finger's move across
	// the picture, as a trackpad accelerates a short reach.
	screenSpeed = 1.5
)

type mobileScreen struct {
	open, busy bool
	// macLocked tells that the Mac shows its lock screen.
	macLocked bool
	// unlock is the lock screen's password form.
	unlock        screenUnlock
	error, status string
	image         ui.ImageSource
	cancel        context.CancelFunc
	events        chan string
	epoch         uint64
	mode          int
	right         bool // the next tap is a right click
	zoom          float32
	panX, panY    float32
	// cursorX and cursorY are where the trackpad has put the Mac's pointer,
	// between 0 and 1 of its screen.
	cursorX, cursorY float32
	// A press in progress: when it began, whether it moved, and whether it
	// holds the button down.
	pressing, moved, holding bool
	pressAt                  time.Time
	// owner is the area, picture or pad, the press began on; longPressed
	// tells that it right-clicked.
	owner       string
	longPressed bool
	// Two fingers on the picture: where they were, how far they have
	// spread and slid since they began, and what they turned out to do.
	gesturing      bool
	pinchX, pinchY float32
	pinchScale     float32
	pinchSlide     float32
	pinchMode      int
	// Feedback drawn at once, before the Mac's picture shows the result:
	// where the finger is, and where it last clicked and when.
	fingerX, fingerY float32
	clickX, clickY   float32
	clickAt          time.Time
	// The last tap, to tell a double from two singles.
	lastTap    time.Time
	tapX, tapY float32
	taps       int
	// Where the picture was last drawn on its canvas, for gestures off it.
	ox, oy, dw, dh, cw, ch float32
	// latched holds the left button down until it is tapped again.
	latched bool
	// The phone's keyboard: its text-input target, the modifiers its
	// accessory bar has lit, and a request to raise it.
	input     *screenInput
	latch     ui.ModifierLatch
	keysMore  bool
	raiseKeys bool
}

func (m *mobileApp) openScreen() {
	m.screen.open, m.screen.error, m.screen.status = true, "", ""
	m.screen.zoom, m.screen.panX, m.screen.panY = 1, 0, 0
	m.screen.cursorX, m.screen.cursorY = 0.5, 0.5
	m.startScreen()
	m.invalidate()
}

func (m *mobileApp) closeScreen() {
	m.screen.epoch++
	if m.screen.cancel != nil {
		m.screen.cancel()
		m.screen.cancel = nil
	}
	m.screen.busy, m.screen.image, m.screen.events = false, nil, nil
}

func (m *mobileApp) startScreen() {
	if !m.connectionUsable() {
		m.screen.error = "请先连接主机"
		return
	}
	m.closeScreen()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan string, 64)
	m.screen.cancel, m.screen.events, m.screen.busy, m.screen.error = cancel, events, true, ""
	epoch, client := m.screen.epoch, m.client
	update := func(fn func()) {
		mygo.RunOnMain(func() {
			if epoch == m.screen.epoch {
				fn()
				m.invalidate()
			}
		})
	}
	go func() {
		defer cancel()
		// Wide enough to read text when zoomed, small enough for a relay.
		s, err := remote.DialScreen(ctx, client, 1440, 50, 30)
		if err != nil {
			update(func() { m.screen.busy, m.screen.error = false, err.Error() })
			return
		}
		defer s.Close()
		stop := context.AfterFunc(ctx, func() { s.Close() })
		defer stop()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-events:
					if s.Send(event) != nil {
						return
					}
				}
			}
		}()
		for {
			kind, payload, err := s.Next()
			if err != nil {
				if ctx.Err() == nil {
					update(func() { m.screen.busy, m.screen.error = false, "远程桌面已断开" })
				}
				return
			}
			switch kind {
			case remote.ScreenFrameJPEG:
				// Decoding off the main thread keeps gestures smooth.
				if image, err := ui.DecodeImage(payload); err == nil {
					update(func() { m.screen.image, m.screen.busy = image, false })
				}
			case remote.ScreenFrameLock:
				locked := len(payload) > 0 && payload[0] == '1'
				update(func() {
					m.screen.macLocked = locked
					if locked {
						m.prepareScreenUnlock()
					}
				})
			case remote.ScreenFrameStatus:
				status := string(payload)
				update(func() { m.screen.status = status })
			}
		}
	}()
}

// screenSend queues an event for the Mac; a full queue drops it, as a
// stalled link would delay it past use.
func (m *mobileApp) screenSend(event string) {
	if m.screen.events == nil {
		return
	}
	select {
	case m.screen.events <- event:
	default:
	}
}

// screenDialog fills the phone: the picture spans its width at the top, as
// large as a phone allows, with the controls below.
func (m *mobileApp) screenDialog(c *ui.Context) {
	if !m.screen.open {
		if m.screen.cancel != nil {
			m.closeScreen()
		}
		return
	}
	root, safe := c.Root().Bounds(), c.SafeArea()
	ui.DialogBase(c, &m.screen.open, func(back, panel ui.Element) {
		back.Background(c.Theme().Background)
		panel.Label("远程桌面").Size(root.W, root.H).Padding(safe.Top, safe.Right, safe.Bottom, safe.Left).Background(c.Theme().Background).Column().Gap(spaceM)
		// Everything above the controls shows the screen: the picture fits
		// its width at the top, and zooms into the whole of it.
		cw := root.W - safe.Left - safe.Right
		ch := root.H - safe.Top - safe.Bottom - mobileScreenControls - 2*spaceM - spaceS
		m.screenCanvas(c, max(cw, 1), max(ch, 1))
		ui.Column(c).FillWidth().Padding(0, spaceL, spaceS, spaceL).Gap(spaceM).Shrink(0).Children(func() { m.screenControls(c) })
	})
}

// screenCloseSize is the close button's size; the lock card sits below it.
const screenCloseSize = 36

// screenOverlay is the close button and the state, floating over the picture
// so it keeps every row of the phone.
func (m *mobileApp) screenOverlay(c *ui.Context) {
	k := colorsOf(c)
	close := ui.ButtonBase(c.Key("screen-close")).Label("关闭远程桌面").Role(ui.RoleButton).Size(screenCloseSize, screenCloseSize).Radius(screenCloseSize / 2).Center().
		Background(k.screenCanvas.Alpha(0.55)).Absolute().Top(spaceS).Right(spaceS)
	close.Children(func() { ui.Icon(c, icon("x")).Size(18, 18).TextColor(k.onScreenCanvas) })
	if close.Clicked() {
		m.screen.open = false
		m.closeScreen()
	}
	if m.screen.macLocked {
		m.screenUnlockCard(c, k)
	}
	status := m.screen.status
	if m.screen.busy && m.screen.image != nil {
		status = "正在连接…"
	}
	if status != "" && m.screen.image != nil {
		ui.Text(c, status).FontSize(textSmall).TextColor(k.onScreenCanvas).Padding(spaceXS, spaceS).Radius(radiusS).
			Background(k.screenCanvas.Alpha(0.55)).MaxWidth(320).Wrap().Absolute().Left(spaceS).Bottom(spaceS).PassThrough()
	}
}

// mobileScreenControls is the height of the mode, buttons and keys rows.
const mobileScreenControls = 3*40 + 2*spaceM

func (m *mobileApp) screenCanvas(c *ui.Context, cw, ch float32) {
	s := &m.screen
	canvas := ui.Box(c.Key("screen-canvas")).Label("远程桌面画面").Size(cw, ch).Shrink(0).Clip().Background(colorsOf(c).screenCanvas)
	for _, g := range canvas.Gestures() {
		if g.Kind == "pinch" {
			m.screenPinch(g)
		}
	}
	// The picture fits the canvas, then zooms and pans within it.
	var dw, dh, ox, oy float32
	if s.image != nil {
		iw, ih := ui.ImageSize(s.image)
		fit := min(cw/iw, ch/ih) * s.zoom
		dw, dh = iw*fit, ih*fit
		// panX and panY are the picture's corner once it outgrows the
		// canvas; until then it centers across and sits at the top.
		if dw > cw {
			s.panX = min(max(s.panX, cw-dw), 0)
			ox = s.panX
		} else {
			s.panX, ox = 0, (cw-dw)/2
		}
		if dh > ch {
			s.panY = min(max(s.panY, ch-dh), 0)
			oy = s.panY
		} else {
			s.panY, oy = 0, 0
		}
	}
	s.ox, s.oy, s.dw, s.dh, s.cw, s.ch = ox, oy, dw, dh, cw, ch
	m.screenGestures(c, canvas, "canvas", s.mode)
	canvas.Children(func() {
		switch {
		case s.image != nil:
			ui.Image(c, s.image).Size(dw, dh).Absolute().Left(ox).Top(oy).PassThrough()
			if s.mode == screenModeTrackpad {
				// A ring shows where the pointer is when the picture is small.
				ring := ui.Box(c.Key("screen-cursor")).Size(20, 20).Radius(10).Border(2, colorsOf(c).accent).PassThrough()
				ring.Absolute().Left(ox + s.cursorX*dw - 10).Top(oy + s.cursorY*dh - 10)
				if s.holding || s.latched {
					ring.Background(colorsOf(c).accent.Alpha(0.35))
				}
			}
		case s.error != "":
			ui.Text(c, s.error).FontSize(mobileSubhead).TextColor(colorsOf(c).onScreenCanvas).Padding(spaceXL).FillWidth().Wrap()
		case s.macLocked:
			// A locked Mac has no picture to give; the overlay says why.
		default:
			ui.Text(c, "正在连接电脑屏幕…").FontSize(mobileSubhead).TextColor(colorsOf(c).onScreenCanvas).Padding(spaceXL)
		}
		m.screenFeedback(c)
		if s.image != nil && ch-dh > 80 {
			hint := "轻点单击 · 长按右键 · 长按后拖动选择 · 双指滚动和缩放"
			if s.mode == screenModeTrackpad {
				hint = "滑动移动指针 · 轻点单击 · 长按后拖拽 · 双指滚动和缩放"
			}
			ui.Row(c).Absolute().Left(0).Right(0).Top(dh + (ch-dh)/2 - 10).Justify(ui.Center).PassThrough().Children(func() {
				ui.Text(c, hint).FontSize(textSmall).TextColor(colorsOf(c).onScreenCanvas.Alpha(0.5)).PassThrough()
			})
		}
		m.screenOverlay(c)
	})
}

// What two fingers turned out to do.
const (
	pinchUndecided = iota
	pinchZoom
	pinchScroll
)

// screenPinch takes a two-finger update. The fingers spreading or closing
// first zoom the picture about them; sliding together first scrolls the
// Mac. The first decides the whole gesture, so a zoom never scrolls.
func (m *mobileApp) screenPinch(g ui.GestureEvent) {
	s := &m.screen
	if g.Phase == "begin" {
		// A second finger ends the first one's press, letting go of a
		// button it holds, so two fingers never click.
		if s.holding {
			m.screenSend(fmt.Sprintf("up %.4f %.4f 0", s.cursorX, s.cursorY))
		}
		s.gesturing, s.pressing, s.holding, s.longPressed = true, false, false, false
		s.pinchX, s.pinchY, s.pinchScale, s.pinchSlide, s.pinchMode = g.X, g.Y, 1, 0, pinchUndecided
	}
	dx, dy := g.X-s.pinchX, g.Y-s.pinchY
	s.pinchScale *= g.Scale
	s.pinchSlide += abs(dx) + abs(dy)
	if s.pinchMode == pinchUndecided {
		switch {
		case abs(s.pinchScale-1) > 0.08:
			s.pinchMode = pinchZoom
		case s.pinchSlide > 12:
			s.pinchMode = pinchScroll
		}
	}
	switch s.pinchMode {
	case pinchZoom:
		// What was under the fingers stays under them as they spread, pinch
		// and move: the picture zooms about them and follows them.
		old := s.zoom
		s.zoom = min(max(s.zoom*g.Scale, 1), 4)
		f := s.zoom / old
		s.ox, s.oy = g.X-(s.pinchX-s.ox)*f, g.Y-(s.pinchY-s.oy)*f
		s.panX, s.panY = s.ox, s.oy
	case pinchScroll:
		// Natural scrolling: the content follows the fingers.
		if dx != 0 || dy != 0 {
			m.screenSend(fmt.Sprintf("scroll %d %d", int(dx*2), int(dy*2)))
		}
	}
	s.pinchX, s.pinchY = g.X, g.Y
	if g.Phase == "end" || g.Phase == "cancel" {
		s.gesturing = false
	}
}

// screenGestures turns one finger on el into the Mac's input in mode.
func (m *mobileApp) screenGestures(c *ui.Context, el ui.Element, owner string, mode int) {
	s := &m.screen
	ox, oy, dw, dh, cw, ch := s.ox, s.oy, s.dw, s.dh, s.cw, s.ch
	// norm turns a point on the canvas into the Mac's screen, between 0 and 1.
	norm := func(x, y float32) (float32, float32, bool) {
		if dw <= 0 || dh <= 0 {
			return 0, 0, false
		}
		nx, ny := (x-ox)/dw, (y-oy)/dh
		return nx, ny, nx >= 0 && nx <= 1 && ny >= 0 && ny <= 1
	}
	send := func(verb string, x, y float32, rest string) {
		m.screenSend(fmt.Sprintf("%s %.4f %.4f %s", verb, min(max(x, 0), 1), min(max(y, 0), 1), rest))
	}
	px, py, _ := el.PointerPosition()
	pressed, clicked := el.Pressed(), el.Clicked()
	dx, dy, _ := el.Dragged()
	now := c.Now()
	// While two fingers zoom or scroll, one finger's moves are theirs.
	if s.gesturing {
		s.pressing = false
		return
	}
	if pressed && !s.pressing {
		s.pressing, s.moved, s.pressAt, s.owner, s.longPressed = true, false, now, owner, false
	}
	if pressed {
		s.fingerX, s.fingerY = px, py
	}
	moving := pressed && (dx != 0 || dy != 0)
	// A finger resting still for a moment is a long press: lifted, it
	// right-clicks; sliding on, it drags with the button held.
	if pressed && s.owner == owner && !s.moved && !s.longPressed && dw > 0 && !s.latched {
		hold := screenRightHold
		if mode == screenModeTrackpad {
			hold = screenHold
		}
		if held := now.Sub(s.pressAt); held >= hold {
			s.longPressed = true
		} else {
			c.After(hold - held)
		}
	}
	switch mode {
	case screenModePoint:
		if moving {
			s.moved = true
			if s.longPressed || s.latched {
				if !s.holding {
					if x, y, ok := norm(px-dx, py-dy); ok {
						send("down", x, y, "0")
						s.holding = true
					}
				}
				if x, y, _ := norm(px, py); s.holding {
					send("drag", x, y, "0")
				}
			} else {
				// One finger moves the zoomed picture, as a photo moves.
				s.panX, s.panY = s.panX+dx, s.panY+dy
			}
		}
	case screenModeTrackpad:
		if moving && dw > 0 {
			s.moved = true
			if s.longPressed && !s.holding {
				send("down", s.cursorX, s.cursorY, "0")
				s.holding = true
			}
			s.cursorX = min(max(s.cursorX+dx/dw*screenSpeed, 0), 1)
			s.cursorY = min(max(s.cursorY+dy/dh*screenSpeed, 0), 1)
			verb := "move"
			if s.holding || s.latched {
				verb = "drag"
			}
			send(verb, s.cursorX, s.cursorY, "0")
			// A zoomed picture follows the pointer, so it never leaves view.
			const margin = 32
			cx, cy := ox+s.cursorX*dw, oy+s.cursorY*dh
			s.panX += max(margin-cx, 0) - max(cx-(cw-margin), 0)
			s.panY += max(margin-cy, 0) - max(cy-(ch-margin), 0)
		}
	}
	// A quick tap can press and lift within one frame, which then never sees
	// the press: its click still counts.
	if !pressed && s.pressing && s.owner == owner || clicked && !s.pressing {
		s.pressing = false
		x, y, ok := s.cursorX, s.cursorY, true
		if mode == screenModePoint {
			x, y, ok = norm(px, py)
		}
		switch {
		case s.holding:
			send("up", x, y, "0")
			s.holding = false
		case s.latched:
		case s.longPressed && !s.moved && ok && mode == screenModePoint:
			send("click", x, y, "1 1")
			s.clickX, s.clickY, s.clickAt = px, py, now
		case !s.moved && !s.longPressed && ok:
			m.screenTap(x, y, now)
			s.clickX, s.clickY, s.clickAt = px, py, now
			if mode == screenModeTrackpad {
				s.clickX, s.clickY = ox+x*dw, oy+y*dh
			}
		}
		s.moved, s.longPressed = false, false
	}
}

// screenFeedback answers a finger at once, before the Mac's picture can: a
// dot under a pressing finger, filled once it rests long enough to hold the
// button, and a ring spreading where it clicked.
func (m *mobileApp) screenFeedback(c *ui.Context) {
	s := &m.screen
	k := colorsOf(c)
	if s.pressing && s.mode == screenModePoint {
		dot := ui.Box(c.Key("screen-finger")).Size(28, 28).Radius(14).Border(2, k.accent).PassThrough()
		dot.Absolute().Left(s.fingerX - 14).Top(s.fingerY - 14)
		if s.longPressed || s.holding {
			dot.Background(k.accent.Alpha(0.45))
		}
	}
	const ripple = 300 * time.Millisecond
	if since := c.Now().Sub(s.clickAt); since < ripple {
		t := float32(since) / float32(ripple)
		r := 10 + 18*t
		ui.Box(c.Key("screen-click")).Size(2*r, 2*r).Radius(r).Border(2, k.accent.Alpha(1-t)).PassThrough().
			Absolute().Left(s.clickX - r).Top(s.clickY - r)
		c.After(16 * time.Millisecond)
	}
}

// screenTap clicks at x, y, counting taps in quick succession at one spot as
// a double or triple click, as macOS needs the count to see one.
func (m *mobileApp) screenTap(x, y float32, now time.Time) {
	s := &m.screen
	count := 1
	if now.Sub(s.lastTap) < screenTapRepeat && abs(x-s.tapX) < 0.02 && abs(y-s.tapY) < 0.02 {
		count = min(s.taps+1, 3)
	}
	s.lastTap, s.tapX, s.tapY, s.taps = now, x, y, count
	button := "0"
	if s.right {
		button, s.right = "1", false
	}
	m.screenSend(fmt.Sprintf("click %.4f %.4f %s %d", x, y, button, count))
}

// The modifier flags of CGEvent, as the helper posts them.
const (
	screenShift   uint64 = 1 << 17
	screenControl uint64 = 1 << 18
	screenOption  uint64 = 1 << 19
	screenCommand uint64 = 1 << 20
)

// screenKeyCodes are the US layout's key codes of the letters and digits,
// for a shortcut typed with a modifier lit: text alone carries no modifier.
var screenKeyCodes = map[rune]int{
	'a': 0, 's': 1, 'd': 2, 'f': 3, 'h': 4, 'g': 5, 'z': 6, 'x': 7, 'c': 8, 'v': 9,
	'b': 11, 'q': 12, 'w': 13, 'e': 14, 'r': 15, 'y': 16, 't': 17, '1': 18, '2': 19,
	'3': 20, '4': 21, '6': 22, '5': 23, '9': 25, '7': 26, '8': 28, '0': 29, 'o': 31,
	'u': 32, 'i': 34, 'p': 35, 'l': 37, 'j': 38, 'k': 40, 'n': 45, 'm': 46,
	' ': 49, ',': 43, '.': 47, '/': 44, '-': 27, '=': 24, ';': 41, '\'': 39,
}

// Keys a phone keyboard lacks or sends differently.
var screenKeys = []struct {
	label string
	code  int
}{{"Esc", 53}, {"Tab", 48}, {"⌫", 51}, {"↩", 36}, {"←", 123}, {"↓", 125}, {"↑", 126}, {"→", 124}}

// Shortcuts used most from a phone.
var screenShortcuts = []struct {
	label string
	code  int
	mods  uint64
}{{"⌘C", 8, screenCommand}, {"⌘V", 9, screenCommand}, {"⌘Z", 6, screenCommand}, {"⌘A", 0, screenCommand}, {"⌘Tab", 48, screenCommand}, {"⌘空格", 49, screenCommand}}

// screenSendKey presses a key with CGEvent modifier flags.
func (m *mobileApp) screenSendKey(code int, flags uint64) {
	m.screenSend(fmt.Sprintf("key %d %d", code, flags))
}

// screenText types text, in event lines of at most a few kilobytes.
func (m *mobileApp) screenText(text string) {
	for text != "" {
		n := len(text)
		if n > 2000 {
			n = 2000
			for n > 0 && !utf8.RuneStart(text[n]) {
				n--
			}
		}
		m.screenSend("text " + base64.StdEncoding.EncodeToString([]byte(text[:n])))
		text = text[n:]
	}
}

// screenToggle is a button lit while on.
func screenToggle(c *ui.Context, label, name string, on bool) bool {
	b := ui.Button(c, label).Label(name).Height(40).Grow(1).Basis(0).MinWidth(0)
	if on {
		b.Background(colorsOf(c).panelSel)
	}
	return b.Clicked()
}

func (m *mobileApp) screenControls(c *ui.Context) {
	s := &m.screen
	ui.Segmented(c, &s.mode, "点按", "触控板").Label("手势模式").FillWidth().Height(40).Shrink(0)
	ui.Row(c).FillWidth().Height(40).Shrink(0).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
		keys := m.screenKeyboard(c)
		if screenToggle(c, "键盘", "打开键盘输入到电脑", keys.Focused()) {
			if keys.Focused() {
				m.screenKeyboardDone(c.Services())
			} else {
				s.raiseKeys = true
			}
		}
		if screenToggle(c, "右键", "下一次点按为右键", s.right) {
			s.right = !s.right
		}
		if screenToggle(c, "按住", "按住左键拖拽", s.latched) {
			s.latched = !s.latched
			verb := "up"
			if s.latched {
				verb = "down"
			}
			m.screenSend(fmt.Sprintf("%s %.4f %.4f 0", verb, s.cursorX, s.cursorY))
		}
	})
	ui.Scroll(c.Key("screen-keys")).FillWidth().Height(40).Shrink(0).Row().Gap(spaceXS).Children(func() {
		for _, key := range screenShortcuts {
			if ui.Button(c, key.label).Label("快捷键 " + key.label).Height(40).Clicked() {
				m.screenSendKey(key.code, key.mods)
			}
		}
		for _, key := range screenKeys {
			if ui.Button(c, key.label).Label("按键 " + key.label).Height(40).Clicked() {
				m.screenSendKey(key.code, 0)
			}
		}
	})
}
