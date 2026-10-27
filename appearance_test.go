package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func colorContrast(a, b ui.Color) float64 {
	luminance := func(c ui.Color) float64 {
		channel := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= .04045 {
				return x / 12.92
			}
			return math.Pow((x+.055)/1.055, 2.4)
		}
		return .2126*channel(c.R) + .7152*channel(c.G) + .0722*channel(c.B)
	}
	l1, l2 := luminance(a), luminance(b)
	return (max(l1, l2) + .05) / (min(l1, l2) + .05)
}

func TestCodexInputPanelFollowsAppearance(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.terminal().info.Program, p.terminal().info.Idle, p.terminal().info.Args = "codex", false, nil
	tt.SetScale(2)
	for _, cachedDark := range []bool{false, true} {
		tt.SetDark(cachedDark)
		tt.Frame()
		bg, fg := 240, 225
		if cachedDark {
			bg, fg = 42, 70
		}
		p.terminal().term.Feed([]byte(fmt.Sprintf("\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"+
			"OpenAI Codex\r\n\r\n"+
			"\x1b[48;2;%d;%d;%d;38;2;%d;%d;%dm\x1b[2KAsk Codex to do anything\x1b[0m", bg, bg, bg, fg, fg, fg)))
		for _, dark := range []bool{cachedDark, !cachedDark, cachedDark} {
			tt.SetDark(dark)
			tt.Frame()
			// Capture the settled chrome rather than the transition between
			// themes; terminal cell colors update on the first frame.
			tt.Advance(180 * time.Millisecond)
			r, ok := tt.Find("Terminal")
			if !ok {
				t.Fatal("missing terminal")
			}
			_, rows := p.terminal().term.Size()
			saveSettingsImage(t, tt, fmt.Sprintf("codex-input-cached-%t-dark-%t", cachedDark, dark))
			pixel := tt.Image().RGBAAt(int((r.X+r.W-2)*2), int((r.Y+r.H/float32(rows)*2.5)*2))
			if dark == cachedDark {
				if pixel.R != uint8(bg) || pixel.G != uint8(bg) || pixel.B != uint8(bg) {
					t.Fatalf("matching appearance changed input panel: %v", pixel)
				}
			} else if (pixel.R < 128) != dark {
				t.Fatalf("dark=%v: cached panel %v", dark, pixel)
			}
		}
	}
	// Appearance adaptation must also work in ordinary, non-Agent programs.
	p.terminal().info.Program = "vim"
	tt.SetDark(false)
	tt.Frame()
	r, _ := tt.Find("Terminal")
	_, rows := p.terminal().term.Size()
	pixel := tt.Image().RGBAAt(int((r.X+r.W-2)*2), int((r.Y+r.H/float32(rows)*2.5)*2))
	if pixel.R < 128 {
		t.Fatalf("editor did not inherit terminal color adaptation: %v", pixel)
	}
}

func TestLightAppearanceReadability(t *testing.T) {
	for i, fg := range lightTerm.Palette {
		if ratio := colorContrast(fg, lightTerm.Background); ratio < 4.5 {
			t.Errorf("ANSI color %d has contrast %.2f", i, ratio)
		}
	}
	for _, bg := range []ui.Color{lightColors.track, lightTerm.Background, lightColors.panel, ui.RGB(255, 255, 255)} {
		for _, fg := range []ui.Color{lightColors.text, lightColors.textMuted, lightColors.textFaint} {
			if ratio := colorContrast(fg, bg); ratio < 4.5 {
				t.Errorf("UI text %v on %v has contrast %.2f", fg, bg, ratio)
			}
		}
	}
	if colorContrast(lightTerm.SelectionText, lightTerm.Selection) < 4.5 {
		t.Fatal("selected text has insufficient contrast")
	}
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	p := a.tab().Focus
	waitFor(t, tt, "shell prompt", func() bool { return strings.Contains(p.terminal().term.Text(), "$") })
	var output strings.Builder
	output.WriteString("\x1b[?25l\x1b[H\x1b[2JAgent output / 终端文字对比度\r\n\r\n")
	for i := range 16 {
		code := 30 + i
		if i >= 8 {
			code = 90 + i - 8
		}
		fmt.Fprintf(&output, "\x1b[%dmANSI %02d 文字 / build output\x1b[0m\r\n", code, i)
	}
	output.WriteString("\r\n\x1b[38;5;154m256色绿色：tests passed\x1b[0m\r\n" +
		"\x1b[38;2;210;220;235mRGB 浅灰文字：Agent 自定义颜色\x1b[0m\r\n" +
		"\x1b[2m弱化文字：会话说明和进度\x1b[0m\r\n" +
		"\x1b[48;2;35;40;45;38;2;90;100;110m自定义深色背景上的文字\x1b[0m\r\n")
	p.terminal().term.Feed([]byte(output.String()))
	tt.SetSize(1200, 780)
	tt.SetScale(2)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		saveSettingsImage(t, tt, fmt.Sprintf("terminal-appearance-dark-%t", dark))
	}
}
