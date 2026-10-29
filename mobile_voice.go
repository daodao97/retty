package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/terminal"
)

type voicePhase uint8

const (
	voiceIdle voicePhase = iota
	voiceAuthorizing
	voicePreparing
	voiceListening
	voiceFinishing
	voiceDraft
)

type voiceRecording interface {
	Results() <-chan mygo.SpeechResult
	Stop()
	Cancel()
}
type mobileVoice struct {
	held, cancelArmed             bool
	authorized, permissionChecked bool
	phase                         voicePhase
	text, error                   string
	startY                        float32
	epoch                         uint64
	cancel                        context.CancelFunc
	recording                     voiceRecording
	term                          *terminal.Terminal
	sid                           string
	generation                    int
	// Allows other MyGo providers later without changing gestures or draft UI.
	provider  string
	start     func(context.Context, mygo.SpeechOptions) (voiceRecording, error)
	post      func(func())
	authorize func(context.Context) error
}

func (m *mobileApp) voiceInputReady() bool {
	return m.voiceEnabled && m.term != nil && !m.background && !m.selected.Exited && !m.needsRecovery() && (m.stream == nil || m.stream.inputReady())
}

func (m *mobileApp) setVoiceEnabled(enabled bool) {
	m.voicePreferenceTouched = true
	m.voiceEnabled = enabled
	if !enabled {
		m.cancelSpeech(true)
	}
	m.save("voice-input-enabled", keychainBool(enabled), func() { m.error = "无法保存语音输入设置"; m.invalidate() })
	m.invalidate()
}

func (m *mobileApp) applyLoadedVoicePreference(enabled bool) {
	if m.voicePreferenceTouched {
		return
	}
	m.voiceEnabled = enabled
	if !enabled {
		m.cancelSpeech(true)
	}
	m.invalidate()
}
func (m *mobileApp) voiceTargetCurrent() bool {
	return m.voice.term == m.term && m.voice.sid == m.selected.ID && m.voice.generation == m.generation
}

// Query never prompts on entry. The first intentional hold requests missing
// permissions without starting capture; a subsequent hold starts recording.
func (m *mobileApp) prepareVoicePermissions() {
	v := &m.voice
	if v.permissionChecked || runtime.GOOS != "ios" || v.provider != "" || m.background {
		return
	}
	v.permissionChecked = true
	epoch := v.epoch
	go func() {
		mic, micErr := mygo.Permissions.Query(mygo.PermissionMicrophone)
		speech, speechErr := mygo.Permissions.Query(mygo.PermissionSpeech)
		m.postVoice(func() {
			if v.epoch != epoch {
				return
			}
			v.authorized = micErr == nil && speechErr == nil && mic == mygo.PermissionGranted && speech == mygo.PermissionGranted
			m.invalidate()
		})
	}()
}
func (m *mobileApp) postVoice(fn func()) {
	if m.voice.post != nil {
		m.voice.post(fn)
	} else {
		mygo.RunOnMain(fn)
	}
}
func (m *mobileApp) authorizeVoice() {
	v := &m.voice
	v.phase = voiceAuthorizing
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	epoch := v.epoch
	authorize := v.authorize
	if authorize == nil {
		authorize = mygo.Speech.Authorize
	}
	go func() {
		defer cancel()
		err := authorize(ctx)
		m.postVoice(func() {
			if v.epoch != epoch || !m.voiceTargetCurrent() || !m.voiceInputReady() {
				return
			}
			v.phase, v.cancel = voiceIdle, nil
			v.authorized = err == nil
			if err != nil && !errors.Is(err, context.Canceled) {
				v.error = voiceError(err)
			}
			m.invalidate()
		})
	}()
	m.invalidate()
}
func (m *mobileApp) cancelSpeech(close bool) {
	v := &m.voice
	v.epoch++ // invalidate preparation, pending callbacks and the old session target
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	if v.recording != nil {
		v.recording.Cancel()
		v.recording = nil
	}
	v.phase, v.held, v.cancelArmed = voiceIdle, false, false
	v.text, v.error = "", ""
	if close {
		v.permissionChecked, v.authorized = false, false
	}
}
func (m *mobileApp) beginSpeech(y float32) bool {
	v := &m.voice
	if !m.voiceInputReady() || v.held || (v.phase == voiceFinishing || v.phase == voiceAuthorizing) {
		return false
	}
	m.cancelSpeech(false)
	v.term, v.sid, v.generation = m.term, m.selected.ID, m.generation
	if !v.authorized && (v.authorize != nil || runtime.GOOS == "ios" && v.provider == "") {
		m.authorizeVoice()
		return true
	}
	if m.term != nil {
		m.term.ClearSelection()
	}
	v.held, v.startY, v.phase = true, y, voicePreparing
	epoch := v.epoch
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	start := v.start
	if start == nil {
		start = func(ctx context.Context, o mygo.SpeechOptions) (voiceRecording, error) {
			session, err := mygo.Speech.Start(ctx, o)
			if err != nil {
				return nil, err
			}
			return session, nil
		}
	}
	post := mygo.RunOnMain
	if v.post != nil {
		post = v.post
	}
	options := mygo.SpeechOptions{Provider: v.provider, Language: "zh-CN", OnDevice: true, ContextualStrings: []string{"Retty", "MyGo", "Codex", "Claude Code"}}
	go func() {
		defer cancel()
		recording, err := start(ctx, options)
		post(func() {
			if v.epoch != epoch || !m.voiceTargetCurrent() || !m.voiceInputReady() || !v.held {
				if recording != nil {
					recording.Cancel()
				}
				return
			}
			if err != nil {
				v.held = false
				v.phase = voiceIdle
				v.cancel = nil
				if !errors.Is(err, context.Canceled) {
					v.error = voiceError(err)
				}
			} else {
				v.recording = recording
				v.phase = voiceListening
			}
			m.invalidate()
		})
		if err != nil || recording == nil {
			return
		}
		for result := range recording.Results() {
			post(func() { m.applySpeechResult(epoch, result) })
		}
	}()
	m.invalidate()
	return true
}
func (m *mobileApp) applySpeechResult(epoch uint64, r mygo.SpeechResult) {
	v := &m.voice
	if v.epoch != epoch {
		return
	}
	if !m.voiceTargetCurrent() || !m.voiceInputReady() {
		m.cancelSpeech(false)
		m.invalidate()
		return
	}
	v.text = r.Text // complete replacement, never append overlapping partials
	if r.Final || r.Err != nil {
		v.recording = nil
		v.cancel = nil
		v.held = false
		v.cancelArmed = false
		if errors.Is(r.Err, context.Canceled) {
			v.text = ""
			v.phase = voiceIdle
		} else {
			v.phase = voiceDraft
			if r.Err != nil {
				v.error = voiceError(r.Err)
				if strings.TrimSpace(r.Text) == "" {
					v.phase = voiceIdle
				}
			} else if strings.TrimSpace(r.Text) == "" {
				v.error = "没有听清，请再试一次"
				v.phase = voiceIdle
			}
		}
	}
	m.invalidate()
}
func voiceError(err error) string {
	if strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "权限") {
		return "请在系统设置中允许麦克风和语音识别"
	}
	if errors.Is(err, mygo.ErrUnsupported) {
		return "当前设备暂不支持语音输入"
	}
	return err.Error()
}
func (m *mobileApp) voicePointer(ev ui.InputEvent) bool {
	v := &m.voice
	switch ev.Kind {
	case ui.InputPointerDown:
		return ev.Button == 0 && m.beginSpeech(ev.Y)
	case ui.InputPointerMove:
		if !v.held {
			return false
		}
		v.cancelArmed = ev.Y < v.startY-64
		m.invalidate()
		return true
	case ui.InputPointerUp:
		if !v.held {
			return false
		}
		canceled := ev.Y < v.startY-64
		v.held = false
		if canceled || v.phase == voicePreparing {
			m.cancelSpeech(false)
		} else if v.recording != nil {
			v.phase = voiceFinishing
			v.recording.Stop()
		}
		v.cancelArmed = false
		m.invalidate()
		return true
	case ui.InputPointerCancel:
		if !v.held {
			return false
		}
		m.cancelSpeech(false)
		m.invalidate()
		return true
	}
	return false
}

// Normalize spoken or edited control characters before the explicit Send
// action, so only the single confirmation Return submits the input.
func voiceDraftText(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text))
}
func (m *mobileApp) sendVoiceDraft(s ui.Services) {
	if m.voice.phase != voiceDraft || !m.voiceTargetCurrent() || !m.voiceInputReady() {
		return
	}
	if text := voiceDraftText(m.voice.text); text != "" {
		m.term.Paste(text)
		m.term.SendKey(ui.KeyEnter, 0)
		m.cancelSpeech(false)
		m.dismissKeyboard(s)
		m.invalidate()
	}
}
func (m *mobileApp) voiceView(c *ui.Context, terminalElement ui.Element) {
	if !m.voiceEnabled {
		return
	}
	v := &m.voice
	if !m.voiceTargetCurrent() {
		m.cancelSpeech(true)
		v.term, v.sid, v.generation = m.term, m.selected.ID, m.generation
	} else if v.phase != voiceIdle && !m.voiceInputReady() {
		m.cancelSpeech(false)
	}
	m.prepareVoicePermissions()
	keyboardFocused := terminalElement.Focused()
	if keyboardFocused && v.held {
		m.cancelSpeech(false)
	}
	// Keep the review editor available while typing, but give the terminal the
	// whole remaining viewport when keyboard input replaces the voice dock.
	if keyboardFocused && v.phase != voiceDraft {
		return
	}
	theme := *c.Theme()
	ui.Column(c.Key("voice-input")).FillWidth().Padding(spaceM, spaceL).Gap(spaceM).Background(theme.Background).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border.Mix(theme.Background, .5)).Children(func() {
		if v.phase == voiceDraft {
			ui.Column(c.Key("voice-review")).FillWidth().Padding(spaceM, spaceL).Gap(spaceS).Radius(radiusXL).Background(theme.Surface).Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(spaceM).Children(func() {
					ui.Text(c, "识别结果").FontSize(textSmall).TextColor(theme.TextMuted).Grow(1)
					if ui.ButtonBase(c).Label("取消语音草稿").Size(32, 32).Radius(16).KeepFocus().Children(func() {
						ui.Icon(c, icon("x")).Size(15, 15).TextColor(theme.TextMuted)
					}).Clicked() {
						m.cancelSpeech(false)
						m.dismissKeyboard(c.Services())
					}
					if ui.PrimaryButton(c, "发送").Label("发送语音文字").Height(32).Radius(16).KeepFocus().Disabled(!m.voiceInputReady() || voiceDraftText(v.text) == "").Clicked() {
						m.sendVoiceDraft(c.Services())
					}
				})
				editor := ui.TextAreaBase(c.Key("voice-draft"), &v.text).Label("语音草稿").Placeholder("语音识别结果").Height(80).FillWidth().FontSize(mobileBody).InputOptions(ui.InputOptions{Return: ui.ReturnDefault})
				keyboardFocused = keyboardFocused || editor.Focused()
			})
		}
		if v.error != "" {
			ui.Row(c.Key("voice-error")).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.error).FontSize(textBody).TextColor(theme.Danger).Grow(1).MinWidth(0)
				if v.error == "请在系统设置中允许麦克风和语音识别" {
					if mobileTextAction(c, "打开语音权限设置", "去设置").Clicked() {
						if err := mygo.Permissions.OpenSettings(); err != nil {
							v.error = voiceError(err)
						}
					}
				}
			})
		}
		if keyboardFocused {
			return
		}
		// Stable keys and KeepFocus retain touch capture and the keyboard viewport
		// across partial transcripts, cancellation previews and overlay rebuilds.
		ui.Row(c.Key("voice-controls")).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() {
			focused := terminalElement.Focused()
			if ui.ButtonBase(c).Label("切换键盘输入").Size(44, 44).Radius(22).KeepFocus().Children(func() {
				ui.Box(c).Size(32, 32).Radius(16).Border(1.4, theme.TextMuted).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Icon(c, icon("keyboard")).Size(19, 19).TextColor(theme.TextMuted)
				})
			}).Clicked() {
				if v.phase != voiceDraft {
					m.cancelSpeech(false)
				}
				if focused {
					m.dismissKeyboard(c.Services())
				} else {
					terminalElement.Focus()
				}
			}
			label := "按住说话"
			if v.held {
				label = "松开结束"
				if v.cancelArmed {
					label = "松开取消"
				}
			}
			if v.phase == voiceAuthorizing {
				label = "正在请求权限…"
			} else if v.phase == voiceFinishing {
				label = "正在完成识别…"
			}
			fill := theme.Surface
			if v.held {
				fill = theme.Accent.Mix(theme.Surface, .9)
				if v.cancelArmed {
					fill = theme.Danger.Mix(theme.Surface, .94)
				}
			}
			button := ui.Box(c.Key("voice-hold")).Label("按住说话").Role(ui.RoleButton).Height(44).Grow(1).MinWidth(0).Radius(22).Background(fill).Border(1, theme.Border.Mix(fill, .65)).KeepFocus().AlignItems(ui.Center).Justify(ui.Center).Disabled(!m.voiceInputReady() || v.phase == voiceFinishing || v.phase == voiceAuthorizing).HandleInput(m.voicePointer).Children(func() {
				ui.Text(c, label).FontSize(mobileCallout).Bold().TextColor(theme.Text).PassThrough()
			})
			button.PointerPosition()
		})
	})
}

// Live feedback shares the application's theme and sits above the dock without
// moving the terminal or the captured pointer. Partial results are visible as
// they arrive, including in the reduced viewport above an open keyboard.
func (m *mobileApp) voiceOverlay(c *ui.Context, keyboardFocused bool) {
	v := &m.voice
	if !m.voiceEnabled || !v.held && v.phase != voiceFinishing {
		return
	}
	theme := *c.Theme()
	bottom := float32(76)
	if keyboardFocused {
		bottom = 16
	}
	maxHeight := min(float32(196), max(float32(116), c.Root().Bounds().H-bottom-88))
	text, textColor := v.text, theme.Text
	if strings.TrimSpace(text) == "" {
		text, textColor = "请开始说话", theme.TextMuted
	}
	// Keep a short utterance compact and retain the newest words of a long
	// utterance. The draft still holds the complete transcript.
	lines := max(1, int((maxHeight-88)/24))
	charsPerLine := max(8, int((c.Root().Bounds().W-56)/17))
	preview := voiceLivePreview(text, charsPerLine*lines)
	visibleLines := max(1, min(lines, (len([]rune(preview))+charsPerLine-1)/charsPerLine))
	height := min(maxHeight, max(float32(124), 88+float32(visibleLines)*24))
	color := theme.Accent
	title, hint := "正在识别", "松开结束 · 上滑取消"
	if v.cancelArmed {
		color, title, hint = theme.Danger, "松开取消", "滑回继续说话"
	} else if v.phase == voicePreparing {
		title = "正在准备麦克风…"
	} else if v.phase == voiceFinishing {
		title, hint = "正在完成识别…", "识别结果可编辑后发送"
	}
	ui.Column(c.Key("voice-feedback")).Label("语音识别面板").Absolute().Left(12).Right(12).Bottom(bottom).Height(height).Padding(spaceXL).Gap(spaceL).Radius(radiusXL).Background(theme.Surface).Border(1, theme.Border).Shadow(0, 4, 18, 0, colorsOf(c).popoverShadow).PassThrough().Children(func() {
		ui.Row(c).FillWidth().Gap(spaceM).AlignItems(ui.Center).PassThrough().Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(color).PassThrough()
			ui.Text(c, title).FontSize(textBody).TextColor(color).PassThrough()
		})
		ui.Text(c.Key("voice-transcript"), preview).FillWidth().Grow(1).MinHeight(0).FontSize(mobileHeadline).LineHeight(1.4).MaxLines(lines).TextColor(textColor).PassThrough()
		ui.Text(c, hint).FontSize(textSmall).TextColor(theme.TextMuted).PassThrough()
	})
}
func voiceLivePreview(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return "…" + string(runes[len(runes)-limit+1:])
	}
	return text
}
