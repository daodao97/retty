package main

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// screenInput types what the phone's keyboard commits into the Mac, as the
// terminal's textInput types it into a session: a composition is only shown
// as candidates until its input method commits it, and what an unmark ends
// goes out before the commit that follows it, as Sogou ends a word and then
// commits only its Return. See internal/terminal/textinput.go.
type screenInput struct {
	m        *mobileApp
	text     string
	sel      ui.TextInputRange
	unmarked string
}

var _ ui.TextInputClient = (*screenInput)(nil)

// screenClient is the remote screen's text-input target, kept for the
// element's lifetime.
func (m *mobileApp) screenClient() *screenInput {
	if m.screen.input == nil {
		m.screen.input = &screenInput{m: m}
	}
	return m.screen.input
}

func (ti *screenInput) TextForRange(ui.TextInputRange) (string, ui.TextInputRange) {
	return "", ui.TextInputRange{}
}

func (ti *screenInput) Selection() ui.TextInputSelection {
	return ui.TextInputSelection{Range: ti.sel}
}

func (ti *screenInput) MarkedRange() (ui.TextInputRange, bool) {
	if ti.text == "" {
		return ui.TextInputRange{}, false
	}
	return ui.TextInputRange{End: ui.UTF16Len(ti.text)}, true
}

func (ti *screenInput) ReplaceText(_ *ui.TextInputRange, text string) {
	ti.text, ti.sel = "", ui.TextInputRange{}
	if carry := ti.unmarked; carry != "" {
		ti.unmarked = ""
		ti.m.screenTyped(carry, 0)
	}
	if text != "" {
		ti.m.screenTyped(text, 0)
	}
}

func (ti *screenInput) SetMarkedText(_ *ui.TextInputRange, text string, selected ui.TextInputRange) {
	ti.unmarked = ""
	ti.text, ti.sel = text, selected
}

func (ti *screenInput) UnmarkText() {
	ti.unmarked, ti.text, ti.sel = ti.text, "", ui.TextInputRange{}
}

// BoundsForRange anchors the candidate window to the keyboard's own
// element: the Mac's text caret is not on the phone.
func (ti *screenInput) BoundsForRange(ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	return ui.Rect{}, ui.TextInputRange{End: ui.UTF16Len(ti.text)}, true
}

func (ti *screenInput) IndexForPoint(ui.Point) (int, bool) { return 0, false }

// screenFlags turns MyGo's modifiers into CGEvent's.
func screenFlags(mods ui.Modifiers) uint64 {
	var flags uint64
	for _, m := range []struct {
		mod  ui.Modifiers
		flag uint64
	}{{ui.Super, screenCommand}, {ui.Alt, screenOption}, {ui.Ctrl, screenControl}, {ui.Shift, screenShift}} {
		if mods&m.mod != 0 {
			flags |= m.flag
		}
	}
	return flags
}

// screenKeyCodesFor are the Mac's key codes of the keys a phone keyboard
// sends as keys rather than text.
var screenKeyCodesFor = map[ui.Key]int{
	ui.KeyEnter: 36, ui.KeyBackspace: 51, ui.KeyTab: 48, ui.KeyEscape: 53, ui.KeyDelete: 117,
	ui.KeyLeft: 123, ui.KeyRight: 124, ui.KeyDown: 125, ui.KeyUp: 126,
}

// screenTyped sends what the keyboard typed. With Command, Option or
// Control held a single letter is a shortcut, as ⌘ and c make ⌘C; a
// newline is the Return key.
func (m *mobileApp) screenTyped(text string, mods ui.Modifiers) {
	if r := []rune(strings.ToLower(text)); mods&(ui.Super|ui.Alt|ui.Ctrl) != 0 && len(r) == 1 {
		if code, ok := screenKeyCodes[r[0]]; ok {
			m.screenSendKey(code, screenFlags(mods))
			return
		}
	}
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if i > 0 {
			m.screenSendKey(36, 0)
		}
		if line != "" {
			m.screenText(line)
		}
	}
}

// screenInputEvent takes the keys a keyboard sends as keys, the text a
// hardware keyboard types, and Paste.
func (m *mobileApp) screenInputEvent(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputKeyDown:
		if code, ok := screenKeyCodesFor[ev.Key]; ok {
			m.screenSendKey(code, screenFlags(ev.Mods))
			return true
		}
	case ui.InputText:
		m.screenTyped(ev.Text, ev.Mods)
		return true
	case ui.InputCommand:
		if ev.Text == "paste" {
			m.screenPaste()
			return true
		}
	}
	return false
}

// screenPaste types the phone's clipboard text on the Mac.
func (m *mobileApp) screenPaste() {
	if text := mygo.Clipboard.ReadText(); text != "" {
		m.screenTyped(text, 0)
	}
}

// The keyboard's accessory bar: the session's, with Return for its newline.
var screenKeyboardActions = []ui.InputAction{
	{ID: "escape", Label: "Esc"},
	{ID: "ctrl", Label: "Ctrl", LongPressID: "lock:ctrl"},
	{ID: "option", Label: "Option", Symbol: "option", LongPressID: "lock:option"},
	{ID: "cmd", Label: "Cmd", Symbol: "command", LongPressID: "lock:cmd"},
	{ID: "left", Label: "←", Symbol: "arrow.left"},
	{ID: "right", Label: "→", Symbol: "arrow.right"},
	{ID: "more", Label: "更多", Symbol: "ellipsis", Items: []ui.InputAction{
		{ID: "shift", Label: "Shift", Symbol: "shift", LongPressID: "lock:shift"},
		{ID: "tab", Label: "Tab"},
		{ID: "up", Label: "↑", Symbol: "arrow.up"}, {ID: "down", Label: "↓", Symbol: "arrow.down"},
		{ID: "return", Label: "回车"},
		{ID: "paste", Label: "粘贴"},
	}},
	{ID: "dismiss", Label: "收起", Symbol: "chevron.down"},
}

var screenActionKeys = map[string]int{"escape": 53, "tab": 48, "up": 126, "down": 125, "left": 123, "right": 124, "return": 36}

// screenKeyboardAction runs an accessory bar button, outside a build pass.
func (m *mobileApp) screenKeyboardAction(s ui.Services, id string) {
	latch := &m.screen.latch
	if mod := keyboardModifier(strings.TrimPrefix(id, "lock:")); mod != 0 {
		if strings.HasPrefix(id, "lock:") {
			latch.Lock(mod)
		} else {
			latch.Tap(mod)
		}
		s.Invalidate()
		return
	}
	if code, ok := screenActionKeys[id]; ok {
		m.screenSendKey(code, screenFlags(latch.Active()))
		latch.Consume()
		s.Invalidate()
		return
	}
	switch id {
	case "paste":
		m.screenPaste()
	case "dismiss":
		m.screenKeyboardDone(s)
	}
	s.Invalidate()
}

func (m *mobileApp) screenKeyboardDone(s ui.Services) {
	m.screen.keysMore = false
	m.screen.latch.Clear()
	s.Blur()
	mygo.App.DismissKeyboard()
	s.Invalidate()
}

// screenKeyboard is the invisible element the phone's keyboard types into,
// raised by the keyboard button.
func (m *mobileApp) screenKeyboard(c *ui.Context) ui.Element {
	s := &m.screen
	// Any input method types, as in a session; with a modifier lit, the
	// ASCII keyboard makes the next key a letter for its shortcut.
	keyboard := ui.KeyboardText
	if s.latch.Active() != 0 {
		keyboard = ui.KeyboardASCII
	}
	keys := ui.Box(c.Key("screen-keyboard")).Label("电脑键盘输入").Size(1, 1).Shrink(0).
		HandleTextInput(m.screenClient()).HandleInput(m.screenInputEvent).
		InputOptions(ui.InputOptions{Keyboard: keyboard, Return: ui.ReturnDefault, Correction: ui.CorrectionOff, Capitalization: ui.CapitalizeNone}).
		InputModifiers(s.latch.Active(), func() { s.latch.Consume(); m.invalidate() }).
		InputAccessory(s.latch.Decorate(screenKeyboardActions, keyboardModifier), func(id string) { m.screenKeyboardAction(c.Services(), id) })
	if s.raiseKeys {
		s.raiseKeys = false
		keys.Focus()
	}
	return keys
}
