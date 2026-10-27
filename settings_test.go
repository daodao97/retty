package main

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsNavigationSearchAndDefaults(t *testing.T) {
	// Installed user hooks are modified settings too; use a fresh profile
	// when asserting the empty state after restoring the font default.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	previous, previousFont := prefs, termFont
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs, termFont = previous, previousFont })
	a, tt := newTestApp(t)
	p := a.tab().Focus
	sid := p.terminal().SID
	tt.Key(ui.Cmd, ui.KeyComma)
	if panel, ok := tt.Find("Settings"); !ok || panel.X != 0 || panel.Y != 0 || panel.W != 1000 || panel.H != 620 {
		t.Fatalf("settings should fill the window: %+v", panel)
	}
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Type("font size")
	if _, ok := tt.Find("Terminal font size"); !ok {
		t.Fatal("search did not find a setting in another section")
	}
	if _, ok := tt.Find("Color theme"); ok {
		t.Fatal("search kept unrelated settings")
	}
	if err := tt.Click("Terminal font size"); err != nil {
		t.Fatal(err)
	}
	before := prefs.FontSize
	tt.Key(0, ui.KeyUp)
	if prefs.FontSize <= before || readSettings().FontSize != prefs.FontSize {
		t.Fatal("font stepper did not apply and persist its new value")
	}
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("color theme")
	if err := tt.Click("Only modified settings"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.Find("Color theme"); ok {
		t.Fatal("modified filter included a default setting")
	}
	if err := tt.Click("Clear settings search"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Restore default Terminal font size"); err != nil {
		t.Fatal(err)
	}
	if prefs.FontSize != defaultFontSize || readSettings().FontSize != defaultFontSize {
		t.Fatal("restoring a default did not apply and persist")
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "所有设置均使用默认值") {
		t.Fatal("modified filter has no empty state")
	}
	for _, section := range []string{"Appearance", "Terminal settings", "About Retty", "Appearance"} {
		if err := tt.Click(section); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := tt.Find("Connections"); ok {
		t.Fatal("connection management remains in settings")
	}
	// Capture both appearances and the minimum size when requested for QA.
	tt.SetSize(1512, 948)
	tt.SetScale(2)
	tt.SetDark(true)
	saveSettingsImage(t, tt, "settings-appearance-dark")
	tt.SetDark(false)
	saveSettingsImage(t, tt, "settings-appearance-light")
	tt.SetSize(560, 340)
	tt.SetDark(true)
	saveSettingsImage(t, tt, "settings-small-dark")
	for _, label := range []string{"Done", "Close Settings", "Only modified settings"} {
		if r, ok := tt.Find(label); !ok || r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > 560 || r.Y+r.H > 340 {
			t.Fatalf("small-window control %q is not reachable: %+v", label, r)
		}
	}
	tt.Key(0, ui.KeyEscape)
	if a.settingsOpen || p.terminal().SID != sid {
		t.Fatal("closing settings changed the session")
	}
	if !tt.Focused("Terminal") {
		t.Fatal("closing settings did not restore terminal focus")
	}
}

func saveSettingsImage(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("MYGO_TEST_IMAGES")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// Legacy layout choices cannot opt users back into the removed layout.
func TestLegacyLayoutSettingsIgnored(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	for _, layout := range []bool{false, true} {
		data, _ := json.Marshal(map[string]any{
			"compactMode": layout, "hideHost": layout, "hideSessionHeader": layout,
			"appearance": "dark", "fontSize": 14, "copyRawText": true,
		})
		if err := os.WriteFile(settingsPath(), data, 0o600); err != nil {
			t.Fatal(err)
		}
		prefs = readSettings()
		if prefs.Appearance != "dark" || prefs.FontSize != 14 || !prefs.CopyRawText {
			t.Fatalf("legacy settings lost other preferences: %+v", prefs)
		}
		saveSettings()
		data, err := os.ReadFile(settingsPath())
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"compactMode", "hideHost", "hideSessionHeader"} {
			if _, ok := saved[key]; ok {
				t.Fatalf("saved removed setting %s", key)
			}
		}
	}
}

func TestDesktopCompactLayoutAndSettingsFocus(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newTestApp(t)
	tt.SetTitleBar(ui.TitleBar{Left: 80, Height: 44})
	p := a.tab().Focus
	waitFor(t, tt, "shell", func() bool { return strings.Contains(p.terminal().term.Text(), "$") })
	sid := p.terminal().SID
	if p.bounds.X != 0 || p.bounds.Y != compactTitleH || p.bounds.W != 1000 || p.bounds.Y+p.bounds.H != 620 {
		t.Fatalf("terminal does not reach window edges: %+v", p.bounds)
	}
	if tabs, ok := tt.Find("Tabs"); !ok || tabs.X < 80 || tabs.H != compactTitleH || tabs.W < 800 {
		t.Fatalf("compact tab strip: %+v", tabs)
	}
	for _, label := range []string{"Split Right", "Command Palette", "连接手机", "手机已连接"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("expanded layout control remains: %s", label)
		}
	}
	for _, item := range a.preferenceItems() {
		if item.id == "compact" || item.id == "host" || item.id == "session-header" {
			t.Fatalf("removed layout setting remains: %s", item.id)
		}
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if !tt.Focused("Search settings") {
		t.Fatal("settings did not take focus")
	}
	tt.Key(0, ui.KeyEscape)
	if p.terminal().SID != sid || !tt.Focused("Terminal") {
		t.Fatal("settings changed session or lost terminal focus")
	}
	tt.Type("echo compact-focus")
	waitFor(t, tt, "terminal focus", func() bool { return strings.Contains(p.terminal().term.Text(), "echo compact-focus") })
	tt.Key(ui.Ctrl, ui.KeyU)
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Type("compact")
	tt.Key(ui.Cmd, ui.KeyComma)
	if err := tt.Click("Done"); err != nil {
		t.Fatal(err)
	}
	tt.Type("-query")
	if p.terminal().find.query != "compact-query" {
		t.Fatalf("search focus after settings: %q", p.terminal().find.query)
	}
	a.closeFind(p)
	tt.Frame()
	saveSettingsImage(t, tt, "desktop-compact")
}
