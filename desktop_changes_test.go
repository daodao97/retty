package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"retty/internal/gitstate"
)

func TestTurnDiffIsReadOnlyBesideTerminalAndRejectsMissingBaseline(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	data := t.TempDir()
	t.Setenv("RETTY_DIR", data)
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-qb", "main", repo).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	file := filepath.Join(repo, "file.txt")
	os.WriteFile(file, []byte("already dirty\n"), 0600)
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.info.Dir, p.SID, p.info.GitTurn = repo, "fixture-pane", "fixture-turn"
	p.term.Feed([]byte("\x1b[H\x1b[2J$ echo 'Agent fixture is still running'\r\nAgent fixture is still running\r\n"))
	store := gitstate.SnapshotStore{Dir: filepath.Join(data, "git-snapshots")}
	if _, err := store.CaptureTurn(context.Background(), repo, p.SID, p.info.GitTurn); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, []byte("this turn\n"), 0600)
	a.openPaneChanges(a.tab(), p, repo)
	tt.Frame()
	b := a.files
	t.Cleanup(func() { b.stop() })
	a.setFileBase(b, "turn")
	waitFor(t, tt, "turn change list", func() bool { return !b.loading && b.comparison != nil })
	terminal, terminalShown := tt.Find("Terminal")
	panel, panelShown := tt.Find("会话变更面板")
	if len(b.entries) != 1 || b.entries[0].Name != "file.txt" || !terminalShown || !panelShown || terminal.W <= 0 || terminal.X+terminal.W > panel.X+1 {
		t.Fatal("turn diff hid the terminal or listed wrong changes", b.entries, tt.Texts())
	}
	if err := tt.Click("查看变更 file.txt"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "immutable turn preview", func() bool { return !b.previewLoading && b.previewDiff != nil })
	if !strings.Contains(string(b.previewData), "-already dirty") || !strings.Contains(string(b.previewData), "+this turn") {
		t.Fatal("preview used HEAD instead of this turn", string(b.previewData))
	}
	saveSettingsImage(t, tt, "turn-diff-beside-terminal")
	// Narrow windows retain the same preview and offer a reachable close.
	tt.SetSize(560, 400)
	tt.Frame()
	if _, ok := tt.Find("关闭变更面板"); !ok || b.previewDiff == nil || p.closed {
		t.Fatal("narrow review lost the preview or terminal")
	}
	p.info.GitTurn = "next-turn-not-captured"
	a.loadFileDirectory(b, repo)
	waitFor(t, tt, "missing next-turn baseline", func() bool { return !b.loading })
	if b.err == "" || b.comparison != nil || len(b.entries) != 0 {
		t.Fatal("missing next turn silently reused old results", b.err, b.entries)
	}
	a.toggleFiles()
	tt.Frame()
	if prefs.FilesOpen || readSettings().FilesSidePanel || p.closed || !tt.Focused("Terminal") {
		t.Fatal("closing review did not restore the existing terminal")
	}
}
