package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"retty/internal/rex"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

// screenTestServices are the last frame's, for actions that run outside one.
var screenTestServices ui.Services

// screenFixture renders the remote screen sheet over a 1600×1000 picture,
// collecting the events it would send to the Mac.
func screenFixture(t *testing.T) (*mobileApp, *ui.Tester, func() []string) {
	t.Helper()
	registerFonts()
	picture := image.NewRGBA(image.Rect(0, 0, 1600, 1000))
	for i := range picture.Pix {
		picture.Pix[i] = 0x80
	}
	picture.Set(0, 0, color.White)
	var data bytes.Buffer
	if err := jpeg.Encode(&data, picture, nil); err != nil {
		t.Fatal(err)
	}
	src, err := ui.DecodeImage(data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	m := &mobileApp{}
	m.screen.open, m.screen.image, m.screen.zoom = true, src, 1
	m.screen.cursorX, m.screen.cursorY = 0.5, 0.5
	m.screen.events = make(chan string, 256)
	tt := ui.NewTester(func(c *ui.Context) {
		screenTestServices = c.Services()
		m.screenDialog(c)
	}, 390, 844)
	tt.Frame()
	drain := func() []string {
		var out []string
		for {
			select {
			case e := <-m.screen.events:
				out = append(out, e)
			default:
				return out
			}
		}
	}
	return m, tt, drain
}

// verbs returns each event's operation and, for clicks, its count.
func verbs(events []string) []string {
	var out []string
	for _, e := range events {
		f := strings.Fields(e)
		if f[0] == "click" {
			out = append(out, "click"+f[4])
		} else {
			out = append(out, f[0])
		}
	}
	return out
}

func TestScreenTrackpadMovesTapsAndHolds(t *testing.T) {
	m, tt, drain := screenFixture(t)
	m.screen.mode = screenModeTrackpad
	r, _ := tt.Find("远程桌面画面")
	x, y := r.X+r.W/2, r.Y+r.H/2

	// A finger sliding right moves the pointer right, and clicks nothing.
	tt.Press(x, y)
	tt.Frame()
	tt.Move(x+40, y)
	tt.Frame()
	tt.Release(x+40, y)
	tt.Frame()
	if got := verbs(drain()); strings.Join(got, ",") != "move" || m.screen.cursorX <= 0.5 || m.screen.cursorY != 0.5 {
		t.Fatal("slide", got, m.screen.cursorX, m.screen.cursorY)
	}

	// Two quick taps, anywhere, click twice at the pointer as a double click.
	for range 2 {
		tt.Press(x-80, y+60)
		tt.Frame()
		tt.Release(x-80, y+60)
		tt.Frame()
		tt.Advance(100 * time.Millisecond)
	}
	if got := verbs(drain()); strings.Join(got, ",") != "click1,click2" {
		t.Fatal("taps", got)
	}

	// A press held still takes the button, moves drag, and lifting lets go.
	tt.Advance(time.Second)
	tt.Press(x, y)
	tt.Frame()
	tt.Advance(screenHold + 50*time.Millisecond)
	tt.Frame()
	tt.Move(x, y+40)
	tt.Frame()
	tt.Release(x, y+40)
	tt.Frame()
	if got := verbs(drain()); strings.Join(got, ",") != "down,drag,up" {
		t.Fatal("hold", got)
	}
}

func TestScreenPointDragsWhereTheFingerIs(t *testing.T) {
	m, tt, drain := screenFixture(t)
	r, _ := tt.Find("远程桌面画面")
	x, y := r.X+m.screen.ox+m.screen.dw/2, r.Y+m.screen.oy+m.screen.dh/2
	tt.Press(x, y)
	tt.Frame()
	tt.Advance(screenRightHold + 50*time.Millisecond)
	tt.Frame()
	tt.Move(x+30, y+10)
	tt.Frame()
	tt.Release(x+30, y+10)
	tt.Frame()
	events := drain()
	if got := verbs(events); strings.Join(got, ",") != "down,drag,up" || !strings.HasPrefix(events[0], "down 0.5000 0.5000") {
		t.Fatal(events)
	}
	for _, e := range events {
		if !remote.ValidScreenEvent(e) {
			t.Fatal("the bridge would drop", e)
		}
	}
}

// A quick tap presses and lifts before a frame sees the press; it clicks.
func TestScreenTapWithinOneFrameClicks(t *testing.T) {
	m, tt, drain := screenFixture(t)
	r, _ := tt.Find("远程桌面画面")
	x, y := r.X+m.screen.ox+m.screen.dw/2, r.Y+m.screen.oy+m.screen.dh/2
	tt.Press(x, y)
	tt.Release(x, y)
	tt.Frame()
	if got := verbs(drain()); strings.Join(got, ",") != "click1" {
		t.Fatal(got)
	}
}

// The phone's keyboard types into the Mac as it types into a session: a
// commit at once, a composition only once confirmed, even when the input
// method ends it before committing only the Return.
func TestScreenKeyboardTypesCommitsAndShortcuts(t *testing.T) {
	m, tt, drain := screenFixture(t)
	if err := tt.Click("打开键盘输入到电脑"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	saveSettingsImage(t, tt, "mobile-remote-screen")
	if m.screen.raiseKeys {
		t.Fatal("the keyboard was not raised")
	}
	ti := m.screenClient()
	ti.ReplaceText(nil, "hi")
	ti.SetMarkedText(nil, "ni", ui.TextInputRange{Start: 2, End: 2})
	if got := drain(); strings.Join(got, "|") != "text aGk=" {
		t.Fatal("composition typed early", got)
	}
	ti.SetMarkedText(nil, "你", ui.TextInputRange{Start: 1, End: 1})
	ti.UnmarkText()
	ti.ReplaceText(nil, "\n")
	m.screenInputEvent(ui.InputEvent{Kind: ui.InputText, Text: "c", Mods: ui.Super})
	m.screenInputEvent(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyBackspace})
	m.screenKeyboardAction(screenTestServices, "cmd")
	m.screenKeyboardAction(screenTestServices, "left")
	m.screenKeyboardAction(screenTestServices, "left")
	got := drain()
	want := []string{"text 5L2g", "key 36 0", fmt.Sprintf("key 8 %d", screenCommand), "key 51 0", fmt.Sprintf("key 123 %d", screenCommand), "key 123 0"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, e := range got {
		if !remote.ValidScreenEvent(e) {
			t.Fatal("the bridge would drop", e)
		}
	}
}

// The space below a picture that fits the width is a trackpad too.
func TestScreenSpaceBelowPictureMovesAndTaps(t *testing.T) {
	m, tt, drain := screenFixture(t)
	m.screen.mode = screenModeTrackpad
	r, _ := tt.Find("远程桌面画面")
	if m.screen.oy != 0 || m.screen.dw != r.W || r.H-m.screen.dh < 100 {
		t.Fatal("the picture does not fill the width at the top", r, m.screen.oy, m.screen.dw, m.screen.dh)
	}
	x, y := r.X+r.W/2, r.Y+r.H-80
	tt.Press(x, y)
	tt.Frame()
	tt.Move(x, y-40)
	tt.Frame()
	tt.Release(x, y-40)
	tt.Frame()
	tt.Press(x, y)
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	if got := verbs(drain()); strings.Join(got, ",") != "move,click1" || m.screen.cursorY >= 0.5 {
		t.Fatal(got, m.screen.cursorY)
	}
}

// Zoomed in, the picture grows past the top strip into the whole canvas.
func TestScreenZoomFillsTheCanvas(t *testing.T) {
	m, tt, _ := screenFixture(t)
	r, _ := tt.Find("远程桌面画面")
	m.screen.zoom = 3
	tt.Frame()
	if m.screen.dh < r.H || m.screen.dw < r.W {
		t.Fatal("zoomed picture stayed small", m.screen.dw, m.screen.dh, r)
	}
}

// By default a tap clicks where it lands, and a finger resting there
// right-clicks without a click on lifting.
func TestScreenPointTapsAndRestsForRightClick(t *testing.T) {
	m, tt, drain := screenFixture(t)
	if m.screen.mode != screenModePoint {
		t.Fatal("pointing is not the default")
	}
	r, _ := tt.Find("远程桌面画面")
	x, y := r.X+m.screen.ox+m.screen.dw/4, r.Y+m.screen.oy+m.screen.dh/4
	tt.Press(x, y)
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Press(x, y)
	tt.Frame()
	tt.Advance(screenRightHold + 50*time.Millisecond)
	tt.Frame()
	tt.Release(x, y)
	tt.Frame()
	events := drain()
	if len(events) != 2 || !strings.HasPrefix(events[0], "click 0.2500 0.2500 0 1") || !strings.HasPrefix(events[1], "click 0.2500 0.2500 1 1") {
		t.Fatal(events)
	}
}

// Two fingers zoom about themselves and carry the picture as they move,
// sending nothing to the Mac.
func TestScreenPinchZoomsAndPansWithoutInput(t *testing.T) {
	m, tt, drain := screenFixture(t)
	x, y := m.screen.ox+m.screen.dw/2, m.screen.oy+m.screen.dh/2
	// The picture point under the fingers, between 0 and 1.
	under := func(x, y float32) (float32, float32) {
		return (x - m.screen.ox) / (m.screen.dw * m.screen.zoom), (y - m.screen.oy) / (m.screen.dh * m.screen.zoom)
	}
	bx, by := under(x, y)
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "begin", X: x, Y: y, Scale: 1})
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "change", X: x, Y: y, Scale: 2})
	if m.screen.zoom != 2 {
		t.Fatal("zoom", m.screen.zoom)
	}
	if ax, ay := under(x, y); abs(ax-bx) > 0.001 || abs(ay-by) > 0.001 {
		t.Fatal("the point under the fingers moved", bx, by, ax, ay)
	}
	ox := m.screen.ox
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "change", X: x + 30, Y: y, Scale: 1})
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "end", X: x + 30, Y: y, Scale: 1})
	if m.screen.ox != ox+30 || m.screen.gesturing {
		t.Fatal("pan", ox, m.screen.ox, m.screen.gesturing)
	}
	tt.Frame()
	if events := drain(); len(events) != 0 {
		t.Fatal(events)
	}
}

// One finger slides the zoomed picture without touching the Mac.
func TestScreenOneFingerPansThePicture(t *testing.T) {
	m, tt, drain := screenFixture(t)
	m.screen.zoom = 3
	tt.Frame()
	r, _ := tt.Find("远程桌面画面")
	x, y := r.X+r.W/2, r.Y+r.H/2
	before := m.screen.ox
	tt.Press(x, y)
	tt.Frame()
	// The zoomed picture's left edge is at the canvas's; it moves left.
	tt.Move(x-40, y)
	tt.Frame()
	tt.Release(x-40, y)
	tt.Frame()
	if events := drain(); len(events) != 0 || m.screen.ox >= before {
		t.Fatal(events, before, m.screen.ox)
	}
}

// Two fingers sliding together scroll the Mac, and stay scrolling.
func TestScreenTwoFingersScroll(t *testing.T) {
	m, _, drain := screenFixture(t)
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "begin", X: 100, Y: 100, Scale: 1})
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "change", X: 100, Y: 120, Scale: 1})
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "change", X: 100, Y: 130, Scale: 1.05})
	m.screenPinch(ui.GestureEvent{Kind: "pinch", Phase: "end", X: 100, Y: 130, Scale: 1})
	if got := drain(); strings.Join(got, "|") != "scroll 0 40|scroll 0 20" || m.screen.zoom != 1 {
		t.Fatal(got, m.screen.zoom)
	}
}

func TestScreenShowsTheMacIsLocked(t *testing.T) {
	m, tt, _ := screenFixture(t)
	if _, ok := tt.Find("电脑已锁屏"); ok {
		t.Fatal("lock hint while unlocked")
	}
	m.screen.macLocked = true
	tt.Frame()
	if _, ok := tt.Find("电脑已锁屏"); !ok {
		t.Fatal("no lock hint")
	}
	m.screen.macLocked = false
	tt.Frame()
	if _, ok := tt.Find("电脑已锁屏"); ok {
		t.Fatal("lock hint after unlock")
	}
}

func TestScreenShowsTheLockBeforeAnyPicture(t *testing.T) {
	m, tt, _ := screenFixture(t)
	m.screen.image, m.screen.busy, m.screen.macLocked = nil, true, true
	tt.Frame()
	if _, ok := tt.Find("电脑已锁屏"); !ok {
		t.Fatal("no lock hint without a picture")
	}
	if _, ok := tt.Find("正在连接电脑屏幕…"); ok {
		t.Fatal("connecting while locked")
	}
}

func TestScreenUnlockAsksForThePassword(t *testing.T) {
	m, tt, _ := screenFixture(t)
	m.screen.macLocked = true
	m.prepareScreenUnlock()
	tt.Frame()
	if _, ok := tt.Find("电脑登录密码"); !ok {
		t.Fatal("no password field")
	}
	if _, ok := tt.Find("解锁"); !ok {
		t.Fatal("no unlock button")
	}
	m.screen.unlock.saved = true
	tt.Frame()
	if _, ok := tt.Find("电脑登录密码"); ok {
		t.Fatal("password field with a saved password")
	}
	if _, ok := tt.Find("更换保存的密码"); !ok {
		t.Fatal("no way to change the saved password")
	}
}

func TestScreenUnlockSendsTheTypedPassword(t *testing.T) {
	m, _, _ := screenFixture(t)
	got := make(chan string, 1)
	old := screenUnlockRemote
	screenUnlockRemote = func(ctx context.Context, _ *rex.Client, password string) error {
		got <- password
		return nil
	}
	defer func() { screenUnlockRemote = old }()
	m.screen.macLocked = true
	m.prepareScreenUnlock()
	m.startScreenUnlock()
	if m.screen.unlock.error == "" || m.screen.unlock.busy {
		t.Fatal("sent an empty password")
	}
	m.screen.unlock.password = "pässw0rd"
	m.startScreenUnlock()
	if m.screen.unlock.password != "" {
		t.Fatal("the form kept the password")
	}
	select {
	case p := <-got:
		if p != "pässw0rd" {
			t.Fatal("password", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no password sent")
	}
}

func TestScreenUnlockCanRevealThePassword(t *testing.T) {
	m, tt, _ := screenFixture(t)
	m.screen.macLocked = true
	m.prepareScreenUnlock()
	tt.Frame()
	if _, ok := tt.Find("显示输入的密码"); !ok {
		t.Fatal("no reveal toggle")
	}
	if _, ok := tt.Find("关闭远程桌面"); !ok {
		t.Fatal("no close button")
	}
}
