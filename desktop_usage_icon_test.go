package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"
	"retty/internal/agentusage"
)

func TestUsageTrayIconPreferencePersistsWithoutChangingAccount(t *testing.T) {
	previous := prefs
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	prefs = settings{FontSize: defaultFontSize, UsageTrayAgent: "claude"}
	a := &App{}
	var item preferenceItem
	for _, candidate := range a.preferenceItems() {
		if candidate.id == "usage-icon" {
			item = candidate
			break
		}
	}
	if item.control == nil {
		t.Fatal("tray icon preference absent")
	}
	tt := ui.NewTester(item.control, 300, 80)
	if err := tt.Click("Agent"); err != nil {
		t.Fatal(err)
	}
	if !prefs.UsageTrayAgentIcon || !readSettings().UsageTrayAgentIcon || prefs.UsageTrayAgent != "claude" {
		t.Fatal("icon setting did not persist independently of account")
	}
	if err := tt.Click("Retty"); err != nil {
		t.Fatal(err)
	}
	if prefs.UsageTrayAgentIcon || readSettings().UsageTrayAgentIcon {
		t.Fatal("Retty icon could not be restored")
	}
}

func TestUsageTrayAgentIconsFollowAccountAndRemainMonochrome(t *testing.T) {
	readings := []agentusage.Reading{{Agent: "codex", Error: "not signed in"}, {Agent: "claude", Windows: []agentusage.Window{{Used: 20}}}}
	reading := selectedUsage(readings, "")
	if usageTrayIconAgent(reading, "", true) != "claude" {
		t.Fatal("automatic icon does not match displayed quota")
	}
	if usageTrayIconAgent(nil, "codex", true) != "codex" {
		t.Fatal("unavailable fixed account lost its identity")
	}
	if usageTrayIconAgent(nil, "claude", true) != "claude" {
		t.Fatal("unavailable Claude account lost its identity")
	}
	if usageTrayIconAgent(reading, "", false) != "" {
		t.Fatal("Retty style followed agent selection")
	}
	if bytes.Equal(usageTrayIconFor("codex"), usageTrayIconFor("claude")) || bytes.Equal(usageTrayIconFor("codex"), usageTrayIconFor("")) {
		t.Fatal("agent logos are not distinct")
	}
	for _, name := range []string{"codex", "claude"} {
		im, err := png.Decode(bytes.NewReader(usageTrayIconFor(name)))
		if err != nil || im.Bounds().Dx() != 36 || im.Bounds().Dy() != 36 {
			t.Fatal("invalid status icon", name, err)
		}
		ink, transparent := 0, 0
		for y := range 36 {
			for x := range 36 {
				r, g, b, a := im.At(x, y).RGBA()
				if a == 0 {
					transparent++
				} else {
					ink++
					if r != 0 || g != 0 || b != 0 {
						t.Fatal("template contains colored pixels", name)
					}
				}
			}
		}
		if ink < 60 || transparent < 100 {
			t.Fatal("empty or opaque template", name, ink, transparent)
		}
	}
}
