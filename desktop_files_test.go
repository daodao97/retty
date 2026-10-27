package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/files"
	"retty/internal/rex"
)

func TestDesktopFilesClosePaneKeepsTerminal(t *testing.T) {
	for _, cmd := range []*command{&cmdClosePane} {
		for _, layout := range []string{"", "sidebar"} {
			t.Run(fmt.Sprintf("%s/layout=%s", cmd.Title, layout), func(t *testing.T) {
				previous := prefs
				prefs = settings{FontSize: defaultFontSize, DesktopLayout: layout}
				t.Cleanup(func() { prefs = previous })
				t.Setenv("RETTY_DIR", t.TempDir())
				a, tt := newStaticTestApp(t)
				original := a.tab()
				p, root := original.Focus, original.Root
				p.terminal().info.Dir = t.TempDir()
				tt.Frame()
				a.toggleFiles()
				tt.Frame()
				browser := a.focusedFiles()
				t.Cleanup(browser.stop)
				ctx := blockedDesktopFilePreview(t, a, browser)

				// Native menu accelerators dispatch these commands outside the tester.
				cmd.Run(a)
				tt.Frame()
				if prefs.FilesOpen || readSettings().FilesOpen || a.focusedFiles() != nil {
					t.Fatal("close command did not dismiss the file browser")
				}
				if len(a.tabs) != 1 || a.tab() != original || original.Root != root || original.Focus != p || p.closed || p.terminalView() == nil {
					t.Fatal("closing the file browser closed its terminal")
				}
				if !tt.Focused("Terminal") || !a.paneIsViewed(p) {
					t.Fatal("closing the file browser did not restore terminal focus")
				}
				if ctx.Err() == nil {
					t.Fatal("file preview request remained active")
				}
			})
		}
	}
}

func TestNewTabPreservesFilePane(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, layout := range []string{"", "sidebar"} {
			t.Run(fmt.Sprintf("remote=%t/layout=%s", remote, layout), func(t *testing.T) {
				previous := prefs
				prefs = settings{FontSize: defaultFontSize, DesktopLayout: layout}
				t.Cleanup(func() { prefs = previous })
				t.Setenv("RETTY_DIR", t.TempDir())
				client, _ := desktopFixtureClient(t)
				registerFonts()
				a := &App{client: client}
				if remote {
					h := fixtureDesktopHost(t, client)
					a.desktops.hosts = []*desktopHost{h}
					in, err := client.Create(rex.CreateOptions{Dir: t.TempDir(), Cols: 80, Rows: 24})
					if err != nil {
						t.Fatal(err)
					}
					a.openDesktopSession(h, in, true)
				} else {
					a.newLocalTab(t.TempDir())
				}
				t.Cleanup(func() {
					if a.focusedFiles() != nil {
						a.focusedFiles().stop()
					}
					for _, tab := range a.tabs {
						for _, p := range tab.panes() {
							a.endPane(p, false)
						}
					}
				})
				tt := ui.NewTester(a.view, 1000, 620)
				original := a.tab()
				a.toggleFiles()
				tt.Frame()
				browser := a.focusedFiles()
				ctx := blockedDesktopFilePreview(t, a, browser)
				if layout == "sidebar" {
					// Native menu accelerators dispatch commands outside the headless tester.
					cmdNewTab.Run(a)
				} else if err := tt.Click("New Tab"); err != nil {
					t.Fatal(err)
				}
				waitFor(t, tt, "new terminal tab", func() bool { return len(a.tabs) == 2 && !prefs.FilesOpen })
				tt.Frame()
				if a.tab() == original || a.focusedFiles() != nil || readSettings().FilesOpen || !a.paneIsViewed(a.tab().Focus) {
					t.Fatal("new tab did not replace the file workspace")
				}
				if ctx.Err() != nil || browser.pane.closed || browser.pane.files() != browser {
					t.Fatal("switching tabs disposed the file component")
				}
				t.Cleanup(browser.stop)
				if remote {
					waitFor(t, tt, "new tab input attachment", func() bool { return a.tab().Focus.terminal().remoteView.inputReady() })
				}
				tt.Type("printf 'new-tab-%s\\n' focused")
				tt.Key(0, ui.KeyEnter)
				waitFor(t, tt, "new tab input", func() bool { return strings.Contains(a.tab().Focus.terminal().term.Text(), "new-tab-focused") })
				infos, err := client.List()
				if err != nil || len(infos) != 2 || infos[0].Exited || infos[1].Exited {
					t.Fatal("original session was not preserved", infos, err)
				}
			})
		}
	}
}

func TestDesktopFilesBrowsePreviewAndLayouts(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0700)
	os.WriteFile(filepath.Join(dir, "src", "nested.go"), []byte("package nested\n"), 0600)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Fixture\n文件预览\n"), 0600)
	os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0600)
	img, _ := os.Create(filepath.Join(dir, "image.png"))
	bitmap := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			bitmap.Set(x, y, color.RGBA{R: 80, G: 160, B: 220, A: 255})
		}
	}
	png.Encode(img, bitmap)
	img.Close()
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.terminal().info.Dir = dir
	p.terminal().term.Feed([]byte("Retty file browser fixture\r\n$ agent is running\r\n"))
	root := a.tab().Root
	t.Cleanup(func() {
		if a.focusedFiles() != nil {
			a.focusedFiles().stop()
		}
	})
	tt.Frame()
	if err := tt.Click("打开文件浏览器"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "directory", func() bool {
		return a.focusedFiles() != nil && !a.focusedFiles().loading && len(a.focusedFiles().entries) == 3
	})
	if len(a.tab().panes()) != 2 || a.tab().Root != root || a.paneIsViewed(p) || p.closed {
		t.Fatal("file split did not preserve the terminal")
	}
	a.toggleZoom()
	tt.Frame()
	content, ok := tt.Find("文件内容区")
	if !ok || content.W != 739 || content.Y != compactTitleH || content.H != 620-compactTitleH {
		t.Fatal("content does not occupy remaining workspace", content)
	}
	if !tt.HasText("文件树") || !tt.HasText("浏览") || !tt.HasText("Diff") {
		t.Fatal(tt.Texts())
	}
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "nested directory", func() bool { return tt.HasText("nested.go") })
	if a.focusedFiles().dir != dir {
		t.Fatal("expanding tree changed root")
	}
	if err := tt.Click("nested.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "nested preview", func() bool {
		return !a.focusedFiles().previewLoading && strings.Contains(string(a.focusedFiles().previewData), "package nested")
	})
	if err := tt.Click("src"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focusedFiles().dirs[filepath.Join(dir,"src")].open {
 t.Fatal("directory did not collapse")
 }

	if err := tt.Click("文件选项"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("显示隐藏文件"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "hidden files", func() bool { return !a.focusedFiles().loading && len(a.focusedFiles().entries) == 4 })
	if !readSettings().FilesHidden {
		t.Fatal("hidden preference did not persist")
	}
	if err := tt.Click("文件选项"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("打开目录…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.focusedFiles().pathEditing {
		t.Fatal("directory dialog did not open")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	tt.Frame()
	if a.focusedFiles() == nil {
		t.Fatal("directory input leaked shortcut to workspace")
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focusedFiles().pathEditing {
		t.Fatal("directory dialog did not close")
	}

	divider, ok := tt.Find("Divider")
	if !ok {
		t.Fatal("missing divider")
	}
	x, y := divider.X+divider.W/2, divider.Y+20
	tt.Press(x, y)
	tt.Move(x+40, y)
	tt.Release(x+40, y)
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	if prefs.FilesWidth != 300 || readSettings().FilesWidth != 300 {
		t.Fatal("divider width did not persist", prefs.FilesWidth, readSettings().FilesWidth)
	}
	tt.Key(0, ui.KeyRight)
	a.saveNow() // Closing the GUI must also flush a pending width save.
	if prefs.FilesWidth != 310 || readSettings().FilesWidth != 310 {
		t.Fatal("keyboard divider adjustment did not persist", prefs.FilesWidth, readSettings().FilesWidth)
	}
	divider, _ = tt.Find("Divider")
	x, y = divider.X+divider.W/2, divider.Y+20
	tt.Advance(time.Second)
	tt.ClickAt(x, y)
	tt.ClickAt(x, y)
	if prefs.FilesWidth != 260 || readSettings().FilesWidth != 260 {
		t.Fatal("double click did not restore the default width", prefs.FilesWidth, readSettings().FilesWidth)
	}
	tt.Frame()
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "text preview", func() bool {
		return a.focusedFiles().preview != "" && !a.focusedFiles().previewLoading && a.focusedFiles().previewCode != nil
	})
	if !tt.HasText("文件预览") {
		t.Fatal(tt.Texts())
	}
	card, ok := tt.Find("文件内容卡片")
	if !ok || card.X != content.X+14 || card.W != content.W-28 {
		t.Fatal("card margins or width incorrect", card, content)
	}
	if _, ok := tt.Find("文件目录路径"); ok {
		t.Fatal("persistent path input remains")
	}
	if _, ok := tt.Find("复制预览内容"); ok {
		t.Fatal("low-frequency actions remain outside menu")
	}

	chooseFileAction(t, tt, "复制预览内容")
	if tt.Clipboard() != "# Fixture\n文件预览\n" {
		t.Fatal(tt.Clipboard())
	}
	// The official editor must support selection across lines without editing.
	if err := tt.Click("文件预览"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("accidental edit")
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != "# Fixture\n文件预览\n" || a.focusedFiles().previewCode.Buffer.String() != string(a.focusedFiles().previewData) {
		t.Fatal("read-only preview changed text or lost its multiline selection", tt.Clipboard())
	}
	// App and native-menu Find commands must search the loaded file.
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	tt.Type("文件预览")
	tt.Frame()
	if count, _ := a.focusedFiles().previewCode.FindMatches(); count != 1 || a.focusedFiles().filter != "" || p.terminal().find.open {
		t.Fatal("file search went to the tree or the hidden terminal", count)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	chooseFileAction(t, tt, "放大预览")
	tt.Frame()
	if !a.focusedFiles().expanded {
		t.Fatal("preview did not expand")
	}
	if a.paneIsViewed(p) {
		t.Fatal("expanded preview marked obscured terminal as viewed")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.focusedFiles().expanded {
		t.Fatal("escape did not dismiss preview")
	}
	if err := tt.Click("image.png"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "image preview", func() bool { return !a.focusedFiles().previewLoading && a.focusedFiles().previewImage != nil })
	if path := os.Getenv("RETTY_FILES_SNAPSHOT_DIR"); path != "" {
		os.MkdirAll(path, 0700)
		f, err := os.Create(filepath.Join(path, "files-image.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
	chooseFileAction(t, tt, "放大预览")
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	prefs.DesktopLayout = "sidebar"
	tt.Frame()
	content, ok = tt.Find("文件内容区")
	if !ok || content.W != 515 || content.X != 485 {
		t.Fatal("sidebar and workspace overlap", content)
	}
	prefs.FilesWidth = 420
	a.focusedFiles().width = 420
	tt.SetSize(560, 620)
	content, ok = tt.Find("文件内容区")
	if !ok || content.W < 200 || content.X+content.W > 560 || prefs.FilesWidth != 420 {
		t.Fatal("narrow sidebar workspace clipped the preview or lost the chosen width", content, prefs.FilesWidth)
	}
	tt.SetSize(1000, 620)
	content, ok = tt.Find("文件内容区")
	if !ok || content.X != 645 || prefs.FilesWidth != 420 {
		t.Fatal("widening the window did not restore the chosen tree width", content, prefs.FilesWidth)
	}
	prefs.FilesWidth = 260
	a.focusedFiles().width = 260
	if err := tt.Click("收起文件浏览器"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if prefs.FilesOpen || p.bounds.W != 776 || a.tab().Root != root {
		t.Fatal("collapse did not restore terminal", p.bounds)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	tt.Frame()
	if a.focusedFiles() == nil {
		t.Fatal("file shortcut did not reopen panel")
	}
}

func TestDesktopFilesKeepsContextAndRejectsOfflineRemote(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "local-only.txt"), []byte("local"), 0600)
	a, tt := newStaticTestApp(t)
	source := a.tab().Focus
	source.terminal().info.Dir = dir
	first := a.openFilesPane(source, dir, false, false).files()
	t.Cleanup(first.stop)
	waitFor(t, tt, "local directory", func() bool { return !first.loading })
	source.terminal().info.Dir = t.TempDir()
	a.syncFiles()
	if first.cwd != dir || first.dir != dir || len(first.entries) != 1 {
		t.Fatal("file component followed focus or source cwd")
	}
	remote, err := a.openBrowser(nil, "about:blank", &desktopHost{}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.endPane(remote, false) })
	second := a.openFilesPane(remote, dir, false, false).files()
	t.Cleanup(second.stop)
	if second == first || second.err == "" || second.loading || len(second.entries) != 0 {
		t.Fatal("offline host fell back to local files")
	}
	a.loadFileDirectory(second, dir)
	if second.err == "" || second.loading {
		t.Fatal("offline path input started local read")
	}
	a.runPosted()
	if len(second.entries) != 0 || len(first.entries) != 1 {
		t.Fatal("components shared file results")
	}
}

func TestDownloadCancellationAndChangesLeaveDestinationIntact(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "changed"}[change], func(t *testing.T) {
			dir := t.TempDir()
			source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
			data := bytes.Repeat([]byte("a"), 2<<20)
			os.WriteFile(source, data, 0600)
			os.WriteFile(dest, []byte("original"), 0600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := downloadFile(ctx, nil, source, dest, func(done, total int64) {
				if change {
					os.WriteFile(source, []byte("changed"), 0600)
				} else {
					cancel()
				}
			})
			if err == nil || !change && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(dest)
			if string(got) != "original" {
				t.Fatal("partial download replaced destination")
			}
			partial, _ := filepath.Glob(filepath.Join(dir, ".retty-download-*"))
			if len(partial) != 0 {
				t.Fatal("partial retained", partial)
			}
		})
	}
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "dest")
	os.WriteFile(source, []byte("complete"), 0600)
	if err := downloadFile(context.Background(), nil, source, dest, func(int64, int64) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "complete" {
		t.Fatal(string(got))
	}
	if text, ok := filePreviewText([]byte("中文")[:5], true); !ok || text != "中" {
		t.Fatal(text, ok)
	}
}

func TestDesktopFileGitPreview(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	name := filepath.Join(dir, "new.go")
	os.WriteFile(name, []byte("package example\n\n// A new function\nfunc Added() { println(\"Hello, 世界\") }\n"), 0600)
	a, tt := newStaticTestApp(t)
	a.tab().Focus.terminal().info.Dir = dir
	a.toggleFiles()
	tt.Frame()
	t.Cleanup(func() {
		if a.focusedFiles() != nil {
			a.focusedFiles().stop()
		}
	})
	waitFor(t, tt, "git directory", func() bool { return a.focusedFiles() != nil && !a.focusedFiles().loading })
	if err := tt.Click("new.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "preview", func() bool { return a.focusedFiles().preview != "" && !a.focusedFiles().previewLoading })
	if err := tt.Click("浏览"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight)
	waitFor(t, tt, "git changes", func() bool { return a.focusedFiles().diff && !a.focusedFiles().loading })
	if err := tt.Click("new.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "diff preview", func() bool { return !a.focusedFiles().previewLoading && a.focusedFiles().previewDiff != nil })
	if !tt.HasText("文件树") || a.focusedFiles().previewLanguage != "Go" || a.focusedFiles().previewDiff.New.Language() == nil {
		t.Fatal("diff tree or syntax missing", a.focusedFiles().previewLanguage)
	}

	if a.focusedFiles().err != "" || !strings.Contains(string(a.focusedFiles().previewData), "+func Added()") {
		t.Fatal(a.focusedFiles().err, string(a.focusedFiles().previewData))
	}
	if path := os.Getenv("RETTY_FILES_SNAPSHOT_DIR"); path != "" {
		os.MkdirAll(path, 0700)
		f, _ := os.Create(filepath.Join(path, "files-diff.png"))
		png.Encode(f, tt.Image())
		f.Close()
	}
	if err := tt.Click("Diff"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyLeft)
	waitFor(t, tt, "normal mode", func() bool { return !a.focusedFiles().diff && !a.focusedFiles().loading })
	if readSettings().FilesDiff {
		t.Fatal("mode was not persisted")
	}
	if err := tt.Click("new.go"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "normal content", func() bool {
		return !a.focusedFiles().previewLoading && strings.Contains(string(a.focusedFiles().previewData), "Hello, 世界")
	})
	if !tt.HasText("文件树") || a.focusedFiles().previewLanguage != "Go" {
		t.Fatal("normal tree or syntax missing")
	}
	chooseFileAction(t, tt, "复制预览内容")
	if tt.Clipboard() != string(a.focusedFiles().previewData) {
		t.Fatal("highlighting changed copied content")
	}

	if path := os.Getenv("RETTY_FILES_SNAPSHOT_DIR"); path != "" {
		f, err := os.Create(filepath.Join(path, "files-code.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
		dark := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1000, 620)
		dark.Frame()
		f, err = os.Create(filepath.Join(path, "files-code-dark.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, dark.Image())
		f.Close()
	}

	tt.SetSize(640, 620)
	tt.Frame()
	card, ok := tt.Find("文件内容卡片")
	if !ok || card.X+card.W > 626 || card.W < 300 {
		t.Fatal("narrow card clipped", card)
	}
	for _, label := range []string{"浏览", "Diff", "文件选项", "预览操作"} {
		r, ok := tt.Find(label)
		if !ok || r.X+r.W > 640 {
			t.Fatal("narrow control clipped", label, r)
		}
	}
	if path := os.Getenv("RETTY_FILES_SNAPSHOT_DIR"); path != "" {
		f, err := os.Create(filepath.Join(path, "files-code-narrow.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}

}

func TestChangedFileTreeRetainsAncestorsAndCollapses(t *testing.T) {
	root := t.TempDir()
	b := &desktopFiles{dir: root, dirs: changedFileTree(root, []files.Entry{{Name: "a/b/deleted.go", Status: "D"}, {Name: "a/changed.go", Status: "M"}, {Name: "top.go", Status: "??"}})}
	rows := b.treeEntries()
	if len(rows) != 5 || rows[2].path != filepath.Join(root, "a/b/deleted.go") || rows[2].depth != 2 {
		t.Fatal(rows)
	}
	b.dirs[filepath.Join(root, "a")].open = false
	if rows = b.treeEntries(); len(rows) != 2 {
		t.Fatal(rows)
	}
	b.filter = "deleted"
	rows = b.treeEntries()
	if len(rows) != 3 || !strings.HasSuffix(rows[2].path, "deleted.go") {
		t.Fatal(rows)
	}
}

func chooseFileAction(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click("预览操作"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}
