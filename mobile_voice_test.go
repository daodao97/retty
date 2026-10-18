package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"retty/internal/terminal"
)

type voiceTestRecording struct {
	results           chan mygo.SpeechResult
	stopped, canceled atomic.Int32
	once              sync.Once
}

func (r *voiceTestRecording) Results() <-chan mygo.SpeechResult { return r.results }
func (r *voiceTestRecording) Stop()                             { r.stopped.Add(1) }
func (r *voiceTestRecording) Cancel()                           { r.canceled.Add(1); r.once.Do(func() { close(r.results) }) }
func voiceTestFixture(t *testing.T) (*mobileApp, *ui.Tester, *voiceTestRecording, chan func(), net.Conn) {
	t.Helper()
	registerFonts()
	conn, peer := net.Pipe()
	term, err := terminal.New(terminal.Options{Conn: conn, Font: terminal.Font{Family: termFont.Family, Size: 13}, Theme: lightTerm, DarkTheme: darkTerm})
	if err != nil {
		t.Fatal(err)
	}
	m := &mobileApp{term: term, selected: rex.SessionInfo{ID: "voice-fixture"}, voiceEnabled: true}
	recording := &voiceTestRecording{results: make(chan mygo.SpeechResult, 8)}
	queue := make(chan func(), 16)
	m.voice.post = func(fn func()) { queue <- fn }
	m.voice.start = func(ctx context.Context, o mygo.SpeechOptions) (voiceRecording, error) {
		if !o.OnDevice {
			t.Error("voice unexpectedly enabled network recognition")
		}
		return recording, nil
	}
	tt := ui.NewTester(m.view, 390, 750)
	if _, ok := tt.Find("按住说话"); !ok {
		t.Fatal("voice dock not shown on entry")
	}
	if _, ok := tt.Find("语音输入"); ok {
		t.Fatal("obsolete header voice entry remains")
	}
	t.Cleanup(func() { m.cancelSpeech(true); recording.Cancel(); term.Close(); peer.Close() })
	return m, tt, recording, queue, peer
}
func voiceTestDispatch(t *testing.T, tt *ui.Tester, q chan func()) {
	t.Helper()
	select {
	case fn := <-q:
		fn()
		tt.Frame()
	case <-time.After(time.Second):
		t.Fatal("voice callback missing")
	}
}
func voiceTestPress(t *testing.T, tt *ui.Tester) (float32, float32) {
	t.Helper()
	b, ok := tt.Find("按住说话")
	if !ok {
		t.Fatal("hold button missing")
	}
	x, y := b.X+b.W/2, b.Y+b.H/2
	tt.Press(x, y)
	return x, y
}
func TestMobileVoiceHoldEditAndSendOnConfirm(t *testing.T) {
	for _, bracketed := range []bool{false, true} {
		name := "plain"
		if bracketed {
			name = "bracketed-paste"
		}
		t.Run(name, func(t *testing.T) { testMobileVoiceHoldEditAndSend(t, bracketed) })
	}
}
func testMobileVoiceHoldEditAndSend(t *testing.T, bracketed bool) {
	m, tt, r, q, peer := voiceTestFixture(t)
	if bracketed {
		m.term.Feed([]byte("\x1b[?2004h"))
	}
	x, y := voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	r.results <- mygo.SpeechResult{Text: "先检查"}
	voiceTestDispatch(t, tt, q)
	r.results <- mygo.SpeechResult{Text: "先检查当前改动"}
	voiceTestDispatch(t, tt, q)
	saveDesktopImage(t, tt, "mobile-voice-recording.png")
	if m.voice.text != "先检查当前改动" || m.voice.phase != voiceListening || !tt.HasText("先检查当前改动") {
		t.Fatal("live replacement transcript was not visible while held")
	}
	if tt.HasText("转文字") {
		t.Fatal("live dictation offered a redundant conversion mode")
	}
	tt.Release(x, y)
	if r.stopped.Load() != 1 || m.voice.phase != voiceFinishing {
		t.Fatal("release did not finish capture")
	}
	r.results <- mygo.SpeechResult{Text: "先检查当前改动。", Final: true}
	voiceTestDispatch(t, tt, q)
	saveDesktopImage(t, tt, "mobile-voice-draft.png")
	if m.voice.phase != voiceDraft || !tt.HasText("发送") {
		t.Fatal("no reviewable draft")
	}
	peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	data := make([]byte, 256)
	if n, _ := peer.Read(data); n != 0 {
		t.Fatalf("recognition wrote terminal data: %q", data[:n])
	}
	peer.SetReadDeadline(time.Time{})
	if err := tt.Click("语音草稿"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("检查改动\n不要提交")
	want := "检查改动 不要提交\r"
	if bracketed {
		want = "\x1b[200~检查改动 不要提交\x1b[201~\r"
	}
	received := make(chan string, 1)
	go func() {
		peer.SetReadDeadline(time.Now().Add(time.Second))
		output := make([]byte, len(want))
		n, _ := io.ReadFull(peer, output)
		received <- string(output[:n])
	}()
	if err := tt.Click("发送语音文字"); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-received:
		if text != want {
			t.Fatalf("incorrect submission: %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("no submitted text")
	}
	if m.voice.phase != voiceIdle || m.voice.text != "" {
		t.Fatal("draft persisted after sending")
	}
	peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, _ := peer.Read(data); n != 0 {
		t.Fatalf("duplicate submission: %q", data[:n])
	}
}
func TestMobileVoiceSlideCancelAndMoveBack(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		name := "cancel"
		if !cancel {
			name = "move-back"
		}
		t.Run(name, func(t *testing.T) {
			m, tt, r, q, _ := voiceTestFixture(t)
			x, y := voiceTestPress(t, tt)
			voiceTestDispatch(t, tt, q)
			tt.Move(x, y-90)
			if !m.voice.cancelArmed || !tt.HasText("松开取消") {
				t.Fatal("slide did not arm cancellation")
			}
			if cancel {
				tt.Release(x, y-90)
				if r.canceled.Load() != 1 || r.stopped.Load() != 0 {
					t.Fatal("canceled recording was finalized")
				}
			} else {
				tt.Move(x, y)
				if m.voice.cancelArmed {
					t.Fatal("moving back did not undo cancellation")
				}
				tt.Release(x, y)
				if r.stopped.Load() != 1 || r.canceled.Load() != 0 {
					t.Fatal("normal release canceled")
				}
			}
		})
	}
}
func TestMobileVoiceReleaseDuringPreparation(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	x, y := voiceTestPress(t, tt)
	tt.Release(x, y)
	voiceTestDispatch(t, tt, q)
	if m.voice.phase != voiceIdle || r.canceled.Load() != 1 {
		t.Fatal("authorization/preparation completed into unwanted recording")
	}
}
func TestMobileVoiceBackgroundAndSessionChangeDiscardLateResults(t *testing.T) {
	for _, background := range []bool{true, false} {
		name := "background"
		if !background {
			name = "session-change"
		}
		t.Run(name, func(t *testing.T) {
			m, tt, r, q, _ := voiceTestFixture(t)
			voiceTestPress(t, tt)
			voiceTestDispatch(t, tt, q)
			epoch := m.voice.epoch
			if background {
				m.enterBackground()
			} else {
				m.selected.ID = "other"
				tt.Frame()
			}
			m.applySpeechResult(epoch, mygo.SpeechResult{Text: "stale", Final: true})
			if m.voice.text != "" || r.canceled.Load() != 1 {
				t.Fatal("stale capture survived navigation/background")
			}
		})
	}
}
func TestMobileVoicePointerCancelReleasesCapture(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	if !m.voicePointer(ui.InputEvent{Kind: ui.InputPointerCancel}) || r.canceled.Load() != 1 {
		t.Fatal("interrupted gesture retained microphone")
	}
}
func TestVoiceDraftTextRemovesTerminalControls(t *testing.T) {
	text := voiceDraftText("  hello\r\n\x1b\x00\t世界  ")
	if strings.ContainsAny(text, "\r\n\x1b\x00\t") || !strings.Contains(text, "世界") {
		t.Fatal(text)
	}
}

func TestMobileVoiceStartFailureIsRecoverable(t *testing.T) {
	m, tt, _, q, _ := voiceTestFixture(t)
	m.voice.start = func(context.Context, mygo.SpeechOptions) (voiceRecording, error) {
		return nil, errors.New("mygo: microphone permission denied")
	}
	voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	if m.voice.held || m.voice.phase != voiceIdle || !tt.HasText("去设置") {
		t.Fatal("permission failure left an active recording UI")
	}
	if m.voice.text != "" {
		t.Fatal("permission error became terminal draft")
	}
}

func TestMobileVoiceAuthorizationPanelKeepsSessionTarget(t *testing.T) {
	m, tt, _, _, _ := voiceTestFixture(t)
	m.voice.phase = voiceAuthorizing
	epoch := m.voice.epoch
	tt.Frame()
	if m.voice.phase != voiceAuthorizing || m.voice.epoch != epoch {
		t.Fatal("panel rebuild canceled permission preparation")
	}
	if !tt.HasText("正在请求权限…") {
		t.Fatal("authorization progress missing")
	}
}
func TestMobileVoiceConnectionLossReleasesMicrophone(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	m.reconnecting = true
	tt.Frame()
	if r.canceled.Load() != 1 || m.voice.held {
		t.Fatal("disconnected session retained microphone")
	}
}

func TestMobileVoiceOverlayKeepsTerminalViewport(t *testing.T) {
	_, tt, r, q, _ := voiceTestFixture(t)
	before, _ := tt.Find("Terminal")
	holdBefore, _ := tt.Find("按住说话")
	x, y := voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	r.results <- mygo.SpeechResult{Text: "检查当前改动"}
	voiceTestDispatch(t, tt, q)
	after, _ := tt.Find("Terminal")
	holdAfter, _ := tt.Find("按住说话")
	if before != after || holdBefore != holdAfter {
		t.Fatalf("recording moved viewport or hold target: %v/%v -> %v/%v", before, holdBefore, after, holdAfter)
	}
	tt.Release(x, y)
}
func TestMobileVoiceDockHidesAndReturnsWithKeyboard(t *testing.T) {
	_, tt, _, _, _ := voiceTestFixture(t)
	before, _ := tt.Find("Terminal")
	if err := tt.Click("切换键盘输入"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("Terminal") {
		t.Fatal("keyboard entry did not focus terminal")
	}
	for _, label := range []string{"按住说话", "切换键盘输入"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("keyboard retained voice control: %s", label)
		}
	}
	keyboard, _ := tt.Find("Terminal")
	if keyboard.H <= before.H {
		t.Fatal("hidden voice row still reserved space")
	}
	saveDesktopImage(t, tt, "mobile-voice-hidden-keyboard.png")
	if err := tt.Click("收起"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("按住说话"); !ok {
		t.Fatal("closing keyboard did not restore voice dock")
	}
	after, _ := tt.Find("Terminal")
	if before != after {
		t.Fatalf("reading viewport did not recover: %v -> %v", before, after)
	}
}
func TestMobileVoiceSlideUpCancelsInEitherDirection(t *testing.T) {
	for _, dx := range []float32{-75, 75} {
		t.Run(fmt.Sprint(dx), func(t *testing.T) {
			m, tt, r, q, _ := voiceTestFixture(t)
			x, y := voiceTestPress(t, tt)
			voiceTestDispatch(t, tt, q)
			tt.Move(x+dx, y-100)
			if !m.voice.cancelArmed {
				t.Fatal("upward slide did not arm cancellation")
			}
			tt.Release(x+dx, y-100)
			if r.stopped.Load() != 0 || r.canceled.Load() != 1 || m.voice.phase != voiceIdle {
				t.Fatal("upward release finalized canceled speech")
			}
		})
	}
}
func TestMobileVoiceFirstAuthorizationNeverStartsCapture(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	granted := make(chan struct{})
	m.voice.authorize = func(ctx context.Context) error {
		select {
		case <-granted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	x, y := voiceTestPress(t, tt)
	if m.voice.phase != voiceAuthorizing || m.voice.held {
		t.Fatal("permission prompt started capture")
	}
	tt.Release(x, y)
	close(granted)
	voiceTestDispatch(t, tt, q)
	if !m.voice.authorized || m.voice.phase != voiceIdle || r.stopped.Load() != 0 || r.canceled.Load() != 0 {
		t.Fatal("authorization left unwanted capture")
	}
	select {
	case <-q:
		t.Fatal("permission gesture started recording")
	default:
	}
	x, y = voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	if m.voice.phase != voiceListening || !m.voice.held {
		t.Fatal("authorized hold did not start capture")
	}
	tt.Release(x, y)
}
func TestMobileVoiceReferenceScreens(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	m.selected.Title = "Review 当前更改"
	m.term.Feed([]byte("\r\n  Retty  ·  当前任务\r\n\r\n  已检查当前改动，等待下一步指令。\r\n\r\n  › "))
	tt.Frame()
	saveDesktopImage(t, tt, "mobile-voice-idle-light.png")
	tt.SetDark(true)
	tt.Frame()
	saveDesktopImage(t, tt, "mobile-voice-idle-dark.png")
	x, y := voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	r.results <- mygo.SpeechResult{Text: "先检查当前改动，不要提交"}
	voiceTestDispatch(t, tt, q)
	saveDesktopImage(t, tt, "mobile-voice-recording-dark.png")
	tt.SetDark(false)
	tt.Frame()
	saveDesktopImage(t, tt, "mobile-voice-recording-light.png")
	tt.SetDark(true)
	tt.Frame()
	tt.Move(x-70, y-100)
	saveDesktopImage(t, tt, "mobile-voice-cancel-dark.png")
	tt.Move(x, y)
	tt.SetSize(390, 360)
	tt.Frame()
	saveDesktopImage(t, tt, "mobile-voice-recording-keyboard.png")
	tt.SetSize(390, 750)
	tt.Frame()
	tt.Release(x, y)
	r.results <- mygo.SpeechResult{Text: "先检查当前改动，不要提交。", Final: true}
	voiceTestDispatch(t, tt, q)
	saveDesktopImage(t, tt, "mobile-voice-draft-dark.png")
	tt.SetSize(320, 568)
	tt.Frame()
	saveDesktopImage(t, tt, "mobile-voice-draft-small.png")
}

func TestMobileVoiceLiveTextVisibleInCompactViewport(t *testing.T) {
	_, tt, r, q, _ := voiceTestFixture(t)
	tt.SetSize(390, 360)
	x, y := voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	r.results <- mygo.SpeechResult{Text: "检查当前改动"}
	voiceTestDispatch(t, tt, q)
	if !tt.HasText("检查当前改动") {
		t.Fatal("live transcript hidden above keyboard")
	}
	panel, ok := tt.Find("语音识别面板")
	if !ok || panel.Y < 52 || panel.Y+panel.H > 284 {
		t.Fatalf("live panel outside reduced viewport: %v", panel)
	}
	tt.Release(x, y)
	if !tt.HasText("检查当前改动") {
		t.Fatal("pending final result lost live transcript")
	}
}
func TestMobileVoiceKeyboardInputPreservesReviewAndHidesDock(t *testing.T) {
	m, tt, _, _, _ := voiceTestFixture(t)
	m.voice.phase, m.voice.text = voiceDraft, "检查当前改动"
	tt.Frame()
	if err := tt.Click("切换键盘输入"); err != nil {
		t.Fatal(err)
	}
	if m.voice.phase != voiceDraft || m.voice.text != "检查当前改动" {
		t.Fatal("keyboard entry discarded draft")
	}
	if _, ok := tt.Find("按住说话"); ok {
		t.Fatal("terminal keyboard retained voice dock")
	}
	if err := tt.Click("语音草稿"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("语音草稿") {
		t.Fatal("draft did not get input focus")
	}
	if _, ok := tt.Find("按住说话"); ok {
		t.Fatal("draft keyboard retained voice dock")
	}
	if _, ok := tt.Find("发送语音文字"); !ok {
		t.Fatal("editing hid review action")
	}
	saveDesktopImage(t, tt, "mobile-voice-editing-no-dock.png")
}

func TestMobileVoiceDisabledHidesDockAndCancelsCapture(t *testing.T) {
	m, tt, r, q, _ := voiceTestFixture(t)
	before, _ := tt.Find("Terminal")
	voiceTestPress(t, tt)
	voiceTestDispatch(t, tt, q)
	epoch := m.voice.epoch
	m.setVoiceEnabled(false)
	m.applySpeechResult(epoch, mygo.SpeechResult{Text: "迟到的结果", Final: true})
	tt.Frame()
	if r.canceled.Load() != 1 || m.voice.phase != voiceIdle || m.voice.text != "" || m.beginSpeech(0) {
		t.Fatal("disabled voice retained recording, accepted a late result or allowed capture")
	}
	for _, label := range []string{"按住说话", "切换键盘输入", "语音草稿", "语音识别面板"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("disabled voice retained %s", label)
		}
	}
	after, _ := tt.Find("Terminal")
	if after.H <= before.H {
		t.Fatal("disabled voice still reserved terminal space")
	}
	m.setVoiceEnabled(true)
	tt.Frame()
	if _, ok := tt.Find("按住说话"); !ok {
		t.Fatal("enabling voice did not restore dock")
	}
	m.voice.phase, m.voice.text = voiceDraft, "未发送草稿"
	m.setVoiceEnabled(false)
	tt.Frame()
	if m.voice.phase != voiceIdle || m.voice.text != "" {
		t.Fatal("disabling retained voice draft")
	}
	if err := tt.Click("Terminal"); err != nil {
		t.Fatal(err)
	}
	if !tt.Focused("Terminal") {
		t.Fatal("disabling voice prevented keyboard input")
	}
}

func TestMobileVoicePreferenceRestorePreservesNewChoice(t *testing.T) {
	m := &mobileApp{}
	if m.voiceEnabled {
		t.Fatal("voice should default to disabled")
	}
	m.applyLoadedVoicePreference(false)
	if m.voiceEnabled {
		t.Fatal("saved disabled preference was not restored")
	}
	m.setVoiceEnabled(true)
	m.applyLoadedVoicePreference(false)
	if !m.voiceEnabled {
		t.Fatal("delayed storage read overwrote a new choice")
	}
}

func TestMobileVoiceSettingsSwitch(t *testing.T) {
	registerFonts()
	for _, size := range [][2]int{{320, 568}, {390, 750}, {750, 390}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := &mobileApp{client: &rex.Client{}}
			m.hello.Version = 6
			tt := ui.NewTester(m.view, size[0], size[1])
			if err := tt.Click("会话列表设置"); err != nil {
				t.Fatal(err)
			}
			panel, ok := tt.Find("会话列表设置面板")
			if !ok || panel.Y < 0 || panel.Y+panel.H > float32(size[1]) {
				t.Fatalf("settings panel outside viewport: %v", panel)
			}
			if err := tt.Click("语音输入"); err != nil {
				t.Fatal(err)
			}
			if !m.voiceEnabled {
				t.Fatal("switch did not enable voice")
			}
			tt.Click("关闭会话列表设置")
			tt.Click("会话列表设置")
			if !m.voiceEnabled {
				t.Fatal("reopening settings lost the choice")
			}
			if err := tt.Click("语音输入"); err != nil {
				t.Fatal(err)
			}
			if m.voiceEnabled {
				t.Fatal("switch did not disable voice")
			}
		})
	}
}
func TestVoiceLivePreviewKeepsLatestWords(t *testing.T) {
	preview := voiceLivePreview("前面说的文字，最后说的新内容", 8)
	if preview != "…最后说的新内容" {
		t.Fatal(preview)
	}
	if len([]rune(preview)) != 8 || !strings.HasPrefix(preview, "…") {
		t.Fatal("preview failed to retain bounded latest text")
	}
}
