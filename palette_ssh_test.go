package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func sshConfigFixture(t *testing.T, text string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".ssh", "config")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitSSHHosts(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for a.paletteSSHLoading && time.Now().Before(deadline) {
		a.runPosted()
		time.Sleep(time.Millisecond)
	}
	if a.paletteSSHLoading || a.paletteSSHError != "" {
		t.Fatalf("SSH config load: busy=%v, error=%q", a.paletteSSHLoading, a.paletteSSHError)
	}
}

func TestPaletteSSHFilteringAndRefresh(t *testing.T) {
	path := sshConfigFixture(t, "Host alpha beta\nHost * !skip beta\n")
	a := &App{}
	a.openPalette()
	waitSSHHosts(t, a)
	for _, query := range []string{"ssh", " SSH ", "ssh   "} {
		a.paletteQuery = query
		items := a.paletteItems()
		if len(items) != 2 || items[0].id != "ssh:alpha" || items[1].title != "beta" {
			t.Fatalf("query %q: %#v", query, items)
		}
	}
	a.paletteQuery = "ssh bt"
	if items := a.paletteItems(); len(items) != 1 || items[0].title != "beta" {
		t.Fatalf("SSH fuzzy filter: %#v", items)
	}
	a.paletteQuery = "settings"
	if items := a.paletteItems(); len(items) != 1 || items[0].title != "Settings…" {
		t.Fatalf("ordinary commands changed: %#v", items)
	}
	if err := os.WriteFile(path, []byte("Host gamma\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.openPalette()
	waitSSHHosts(t, a)
	a.paletteQuery = "ssh"
	if items := a.paletteItems(); len(items) != 1 || items[0].title != "gamma" {
		t.Fatalf("reopened palette used stale SSH config: %#v", items)
	}
}

func TestPaletteSSHSelectionStartsLocalCommand(t *testing.T) {
	for _, action := range []string{"enter", "click"} {
		t.Run(action, func(t *testing.T) {
			config := sshConfigFixture(t, "Host fixture-$HOME\n")
			home := filepath.Dir(filepath.Dir(config))
			home, err := filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			// A fake SSH binary records argv and waits on its owned PTY; no
			// network connection or existing session receives test input.
			bin := t.TempDir()
			capture := filepath.Join(bin, "argv")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + capture + "'\nprintf 'SSH fixture ready\\n'\nwhile IFS= read -r line; do :; done\n"
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			a, tt := newTestApp(t)
			old := a.tab()
			// A focused remote tab must still launch local SSH using the
			// local config, rather than sending it to that desktop.
			old.Host = &desktopHost{}
			a.openPalette()
			waitSSHHosts(t, a)
			tt.Frame()
			tt.Type("ssh fixture")
			if !tt.HasText("fixture-$HOME") {
				t.Fatal("SSH host is missing from palette")
			}
			if action == "enter" {
				tt.Key(0, ui.KeyEnter)
			} else if err := tt.Click("fixture-$HOME"); err != nil {
				t.Fatal(err)
			}
			if a.paletteOpen || len(a.tabs) != 2 || a.tab() == old || a.tab().Host != nil {
				t.Fatal("selection did not open a new local tab")
			}
			if p := a.tab().Focus; p.terminal().info.Dir != home || p.terminal().SID == "" || p.host != nil {
				t.Fatalf("SSH pane: dir=%q, SID=%q, host=%v", p.terminal().info.Dir, p.terminal().SID, p.host)
			}
			tt.Frame()
			if !tt.Focused("Terminal") {
				t.Fatal("SSH terminal did not receive keyboard focus")
			}
			deadline := time.Now().Add(3 * time.Second)
			var argv []byte
			for time.Now().Before(deadline) {
				argv, _ = os.ReadFile(capture)
				if len(argv) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if string(argv) != "--\nfixture-$HOME\n" {
				t.Fatalf("SSH did not receive the literal alias: %q", argv)
			}
		})
	}
}

func TestPaletteSSHEmptyState(t *testing.T) {
	a := &App{paletteQuery: "ssh", paletteSSHLoading: true}
	if !strings.Contains(a.paletteEmptyMessage(), "正在读取") {
		t.Fatal("missing loading state")
	}
	a.paletteSSHLoading = false
	if !strings.Contains(a.paletteEmptyMessage(), "没有具体") {
		t.Fatal("missing empty config state")
	}
	a.paletteSSHError = "fixture read error"
	if a.paletteEmptyMessage() != a.paletteSSHError {
		t.Fatal("read error hidden")
	}
}
