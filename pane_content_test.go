package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func TestComponentPanesKeepIndependentStateAndSessions(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newTestApp(t)
	tt.SetSize(1600, 900)
	source := a.tab().Focus
	dir := t.TempDir()
	path := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(path, []byte("component one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source.terminal().info.Dir = dir
	before, err := a.client.List()
	if err != nil || len(before) != 1 {
		t.Fatal(before, err)
	}
	first := a.openFilesPane(source, dir, false, false)
	waitFor(t, tt, "first file component", func() bool { return !first.files().loading })
	a.previewFile(first.files(), path, false, 256<<10)
	waitFor(t, tt, "first preview", func() bool { return first.files().previewCode != nil })
	state := first.files().previewCode
	saveSettingsImage(t, tt, "component-terminal-files")
	first.files().filter = "one"
	// A second file pane has its own directory, filter and selection.
	otherDir := t.TempDir()
	second := a.openFilesPane(first, otherDir, false, true)
	waitFor(t, tt, "second file component", func() bool { return !second.files().loading })
	if first.files() == second.files() || second.files().preview != "" || second.files().filter != "" {
		t.Fatal("file components share state")
	}
	if first.directory() != dir || second.directory() != otherDir || first.files().previewCode != state {
		t.Fatal("opening another file component replaced the first")
	}
	web, err := a.openBrowser(source, "about:blank", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	diff := a.openFilesPane(second, otherDir, true, true)
	if len(a.tab().panes()) != 5 || diff.content.kind() != paneDiff || diff.files().sourceSID != source.terminal().SID {
		t.Fatal("mixed layout lost component types or source context")
	}
	if len(slices.Collect(a.hostPanes(nil))) != 1 || web.terminal() != nil || first.terminal() != nil {
		t.Fatal("non-terminal content entered session reconciliation")
	}
	a.apply(map[string]rex.SessionInfo{before[0].ID: before[0]})
	if len(a.tab().panes()) != 5 {
		t.Fatal("session polling removed non-terminal panes")
	}
	// Commands requiring a session are absent for other components.
	for _, cmd := range []*command{&cmdRestart, &cmdClear, &cmdHistory} {
		if cmd.available(a) {
			t.Fatalf("%s enabled in a Diff pane", cmd.ID)
		}
	}
	a.restartFocused()
	if a.tab().Focus != diff || diff.closed {
		t.Fatal("restart replaced a non-terminal component")
	}
	for _, p := range []*Pane{web, second, diff} {
		a.closePane(p)
	}
	first.Tab.setFocus(first)
	a.focusReq = first
	tt.Frame()
	a.openFind()
	if !first.files().previewCode.Focused() && first.files().filterFocus {
		t.Fatal("find targeted the wrong file component")
	}
	a.closePane(first)
	tt.Frame()
	after, err := a.client.List()
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].PID != before[0].PID || after[0].Exited {
		t.Fatal("component lifecycle changed the terminal process", before, after, err)
	}
	if a.tab().Focus != source || source.closed || len(a.closedSessions) != 0 {
		t.Fatal("closing components did not return to the original session")
	}
}

func TestMixedComponentLayoutRestoresWithoutCreatingSessions(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newTestApp(t)
	source := a.tab().Focus
	dir := t.TempDir()
	path := filepath.Join(dir, "saved.txt")
	if err := os.WriteFile(path, []byte("saved content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source.terminal().info.Dir = dir
	p := a.openFilesPane(source, dir, false, false)
	b := p.files()
	waitFor(t, tt, "directory", func() bool { return !b.loading })
	a.previewFile(b, path, false, 256<<10)
	waitFor(t, tt, "preview", func() bool { return b.previewCode != nil })
	b.filter, b.width = "saved", 310
	web, err := a.openBrowser(source, "localhost:5173/preview", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	diff := a.openFilesPane(p, dir, true, true)
	diff.files().base = "branch"
	a.tab().setFocus(p)
	a.tab().Zoom = p
	saved := a.snapshot()
	before, _ := a.client.List()
	raw, _ := json.Marshal(saved)
	if err := a.client.SetLayout(raw); err != nil {
		t.Fatal(err)
	}
	restored := &App{client: a.client, hello: a.hello}
	if !restored.restore() {
		t.Fatal("mixed layout was not restored")
	}
	t.Cleanup(func() {
		for _, p := range restored.allPanes() {
			restored.endPane(p, false)
		}
	})
	ps := restored.tab().panes()
	if len(ps) != 4 || len(slices.Collect(restored.hostPanes(nil))) != 1 {
		t.Fatal("restored components became terminal sessions")
	}
	byKey := map[string]*Pane{}
	for _, p := range ps {
		byKey[p.identity()] = p
	}
	file := byKey[p.identity()]
	if file == nil || restored.tab().Focus != file || restored.tab().Zoom != file || file.files().width != 310 {
		t.Fatal("container identity, focus, zoom or component width was lost")
	}
	if byKey[web.identity()].browser().url != web.browser().url || byKey[diff.identity()].files().base != "branch" {
		t.Fatal("browser or Diff state was lost")
	}
	if file.files().sourceSID != source.terminal().SID || file.directory() != dir {
		t.Fatal("file component lost its host/directory/source")
	}
	// A save before async restoration finishes must retain the pending file.
	if file.content.snapshot(file).Files.Preview != path {
		t.Fatal("pending preview disappeared on save")
	}
	view := ui.NewTester(restored.view, 1000, 620)
	waitFor(t, view, "restored preview", func() bool { return file.files().previewCode != nil })
	if file.files().preview != path || file.files().filter != "saved" || string(file.files().previewData) != "saved content\n" {
		t.Fatal("file selection, filter or content was lost")
	}
	after, _ := a.client.List()
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].PID != before[0].PID {
		t.Fatal("restoration created or replaced a PTY")
	}
	// Also exercise remote layout restoration without touching a real host.
	host := fixtureDesktopHost(t, a.client)
	host.sessions, host.layout = after, saved
	remote := &App{client: a.client}
	remote.restoreDesktopLayout(host)
	t.Cleanup(func() {
		for _, p := range remote.allPanes() {
			remote.endPane(p, false)
		}
	})
	if len(remote.tabs) != 1 || len(remote.tab().panes()) != 4 {
		t.Fatal("remote mixed layout dropped components")
	}
	for _, p := range remote.tab().panes() {
		if p.host != host {
			t.Fatal("remote component lost its host")
		}
	}
}

func TestUnsupportedComponentNeverRestoresAsShell(t *testing.T) {
	a := &App{}
	saved := &savedNode{Kind: "future-component", PaneID: "stable", Dir: "/project"}
	p := a.restorePaneContent(saved, nil)
	if p == nil || p.terminal() != nil || p.identity() != "stable" || p.content.snapshot(p).Kind != saved.Kind {
		t.Fatal("unknown component lost its identity or became a shell")
	}
	a.endPane(p, true)
}

func TestLegacyFileWorkspaceMigratesOnceIntoPane(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	source := a.tab().Focus
	source.terminal().info.Dir = t.TempDir()
	prefs.FilesOpen = true
	tt.Frame()
	file := a.focusedFiles()
	if file == nil || file.cwd != source.directory() || prefs.FilesOpen || readSettings().FilesOpen {
		t.Fatal("legacy workspace did not become a component")
	}
	t.Cleanup(file.stop)
	tt.Frame()
	if len(a.tab().panes()) != 2 {
		t.Fatal("legacy workspace migrated more than once")
	}
	legacy := a.restorePaneContent(&savedNode{Browser: &savedBrowser{URL: "about:blank"}, Dir: source.directory()}, nil)
	if legacy == nil || legacy.browser() == nil || legacy.directory() != source.directory() {
		t.Fatal("legacy browser descriptor was not restored")
	}
	a.endPane(legacy, false)
}
