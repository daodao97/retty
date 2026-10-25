package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/async"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"retty/internal/files"
	"retty/internal/gitstate"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/terminal"
)

// File tasks use the same MyGo Task core as NewTask with Retty's queued
// window dispatcher. The browser owns their lifetime, including parallel
// directory reads; stop closes them before changing or disposing the owner.
type desktopFiles struct {
	base                                             string
	comparison                                       *gitstate.Comparison
	turn                                             string
	turnRetry                                        int
	turnRetryAt                                      time.Time
	host                                             *desktopHost
	pane                                             *Pane
	client                                           *rex.Client
	cwd, dir, pathInput, filter                      string
	entries                                          []files.Entry
	loading, truncated                               bool
	err                                              string
	directoryTask                                    *async.Task[files.Result]
	linkTask                                         *async.Task[files.Result]
	read                                             func(context.Context, *rex.Client, files.Request) (files.Result, error)
	list                                             ui.ListState
	filterFocus                                      bool
	pathEditing                                      bool
	dirs                                             map[string]*fileDirectory
	preview                                          string
	previewData                                      []byte
	previewEntry                                     *files.Entry
	previewCode                                      *editor.State
	previewDiff                                      *editor.DiffState
	previewLanguage                                  string
	previewAdded, previewRemoved                     int
	previewImage                                     *ui.Bitmap
	previewNote                                      string
	previewLoading, previewTruncated, diff, expanded bool
	previewLimit                                     int
	previewTask                                      *async.Task[desktopFilePreview]
	downloadCancel                                   context.CancelFunc
	downloadEpoch                                    uint64
	downloading                                      bool
	downloadProgress                                 string
}

func (b *desktopFiles) stop() {
	if b.directoryTask != nil {
		b.directoryTask.Close()
		b.directoryTask = nil
	}
	if b.linkTask != nil {
		b.linkTask.Close()
		b.linkTask = nil
	}
	if b.previewTask != nil {
		b.previewTask.Close()
		b.previewTask = nil
	}
	for _, d := range b.dirs {
		if d.task != nil {
			d.task.Close()
		}
	}
	if b.downloadCancel != nil {
		b.downloadCancel()
		b.downloadCancel = nil
	}
	b.downloadEpoch++
	b.downloading = false
}

func (b *desktopFiles) reader() func(context.Context, *rex.Client, files.Request) (files.Result, error) {
	if b.read != nil {
		return b.read
	}
	return fileRequest
}

func (a *App) toggleFiles() {
	prefs.FilesOpen = !prefs.FilesOpen
	if !prefs.FilesOpen {
		prefs.FilesSidePanel = false
	}
	saveSettings()
	if prefs.FilesOpen {
		a.focusReq = nil
	}
	if !prefs.FilesOpen {
		if a.files != nil {
			a.files.stop()
		}
		a.files = nil
		a.focusCurrentTerminal()
	}
}

func (a *App) syncFiles() {
	if !prefs.FilesOpen {
		return
	}
	var p *Pane
	if t := a.tab(); t != nil {
		p = t.Focus
	}
	if p == nil || p.closed {
		if a.files != nil {
			a.files.stop()
			a.files = nil
		}
		return
	}
	cwd := a.currentDir()
	var client *rex.Client
	if p.host != nil {
		client = p.host.client
	}
	if b := a.files; b != nil && b.pane == p && b.host == p.host && b.client == client && b.cwd == cwd {
		if b.diff && b.base == "turn" && b.turn != p.info.GitTurn {
			a.loadFileDirectory(b, cwd)
		}
		return
	}
	if a.files != nil {
		a.files.stop()
	}
	b := &desktopFiles{pane: p, host: p.host, client: client, cwd: cwd, diff: prefs.FilesDiff, base: prefs.FilesDiffBase}
	if b.base != "turn" && b.base != "branch" {
		b.base = "head"
	}
	a.files = b
	a.loadFileDirectory(b, cwd)
}

func fileRequest(ctx context.Context, client *rex.Client, req files.Request) (files.Result, error) {
	if client != nil {
		return remote.BrowseFiles(ctx, client, req)
	}
	return files.Do(files.WithSnapshotStore(ctx, filepath.Join(rex.Dir(), "git-snapshots")), req)
}

func (a *App) filesCurrent(b *desktopFiles) bool {
	if a.files != b || !prefs.FilesOpen || a.quitting || b.pane.closed {
		return false
	}
	t := a.tab()
	return t != nil && t.Focus == b.pane && b.host == b.pane.host && (b.pane.host == nil || b.pane.host.client == b.client && b.pane.host.connected())
}

func (a *App) openTerminalFile(p *Pane, link terminal.Link) {
	if t := a.tab(); t == nil || t.Focus != p || p.closed {
		return
	}
	prefs.FilesOpen = true
	prefs.FilesSidePanel = false
	a.focusReq = nil
	saveSettings()
	a.syncFiles()
	b := a.files
	if b == nil || !a.filesCurrent(b) {
		return
	}
	// File links show the file itself, even when the browser was showing Diff.
	b.diff = false
	a.loadFileDirectory(b, filepath.Dir(link.Path))
	read, client := b.reader(), b.client
	b.linkTask = async.New(a.desktopDispatcher(), func(state async.State[files.Result]) {
		if !a.filesCurrent(b) {
			return
		}
		switch state.Phase {
		case async.Failed:
			b.err = state.Err.Error()
		case async.Succeeded:
			r := state.Value
			if r.Entry == nil || !r.Entry.Directory && !r.Entry.Regular {
				b.err = "此路径不是普通文件或目录"
				return
			}
			if r.Entry.Directory {
				a.loadFileDirectory(b, r.Path)
				return
			}
			limit := 256 << 10
			if link.Line > 0 {
				limit = files.MaxRead
			}
			a.previewFileAt(b, r.Path, false, limit, link.Line, link.Column)
		}
	})
	b.linkTask.Start(context.Background(), func(ctx context.Context, publish func(files.Result)) error {
		r, err := read(ctx, client, files.Request{Kind: "stat", Path: link.Path})
		if err == nil {
			publish(r)
		}
		return err
	})
}

func (a *App) loadFileDirectory(b *desktopFiles, path string) {
	b.stop()
	b.loading = true
	b.err = ""
	b.expanded = false
	b.preview = ""
	b.previewImage = nil
	b.previewData = nil
	b.previewCode, b.previewDiff = nil, nil
	b.previewLanguage = ""
	b.previewAdded, b.previewRemoved = 0, 0
	b.filter = ""
	b.dir, b.pathInput = path, path
	b.entries = nil
	b.dirs = map[string]*fileDirectory{}
	b.truncated = false
	b.comparison = nil
	b.list = ui.ListState{}
	if b.pane.host != nil && !b.pane.host.connected() {
		b.loading = false
		b.err = "远端未连接，请先连接这台主机"
		return
	}
	read, client := b.reader(), b.client
	hidden := prefs.FilesHidden
	diff := b.diff
	base, sid, turn := b.base, b.pane.SID, b.pane.info.GitTurn
	if b.turn != turn {
		b.turnRetry = 0
	}
	b.turn, b.turnRetryAt = turn, time.Time{}
	b.directoryTask = async.New(a.desktopDispatcher(), func(state async.State[files.Result]) {
		if !a.filesCurrent(b) {
			return
		}
		switch state.Phase {
		case async.Failed:
			b.loading = false
			b.err = state.Err.Error()
			if base == "turn" && turn != "" && b.turnRetry < 2 && (strings.Contains(b.err, "本轮快照未记录成功") || strings.Contains(b.err, "本轮基线尚未就绪")) {
				b.turnRetryAt = time.Now().Add(time.Second)
			}
		case async.Succeeded:
			b.loading = false
			r := state.Value
			b.dir, b.pathInput = r.Path, r.Path
			b.entries = r.Entries
			b.truncated = r.Truncated
			b.comparison = r.Comparison
			if diff {
				b.dirs = changedFileTree(r.Path, r.Entries)
			} else {
				b.dirs[r.Path] = &fileDirectory{open: true, entries: r.Entries}
			}
		}
	})
	b.directoryTask.Start(context.Background(), func(ctx context.Context, publish func(files.Result)) error {
		kind := "list"
		if diff {
			kind = "changes"
		}
		if diff && base != "head" {
			kind = "git-changes"
		}
		r, err := read(ctx, client, files.Request{Kind: kind, Path: path, Hidden: hidden, Base: base, Session: sid, Turn: turn})
		if err == nil {
			publish(r)
		}
		return err
	})
}

func (a *App) previewFile(b *desktopFiles, path string, diff bool, limit int) {
	a.previewFileAt(b, path, diff, limit, 0, 0)
}

type desktopFilePreview struct {
	result         files.Result
	bitmap         *ui.Bitmap
	code           *editor.State
	changes        *editor.DiffState
	note, language string
	added, removed int
}

func (a *App) previewFileAt(b *desktopFiles, path string, diff bool, limit, line, column int) {
	if !a.filesCurrent(b) {
		return
	}
	if b.linkTask != nil {
		b.linkTask.Close()
		b.linkTask = nil
	}
	b.preview, b.diff, b.previewLimit = path, diff, limit
	b.previewLoading = true
	b.err, b.previewNote = "", ""
	b.previewImage, b.previewData = nil, nil
	b.previewCode, b.previewDiff = nil, nil
	b.previewLanguage = ""
	b.previewAdded, b.previewRemoved = 0, 0
	b.previewEntry = nil
	b.previewTruncated = false
	if b.previewTask == nil {
		b.previewTask = async.New(a.desktopDispatcher(), func(state async.State[desktopFilePreview]) {
			if !a.filesCurrent(b) {
				return
			}
			switch state.Phase {
			case async.Failed:
				b.previewLoading = false
				b.err = state.Err.Error()
			case async.Succeeded:
				b.previewLoading = false
				p := state.Value
				b.previewData = p.result.Data
				b.previewCode, b.previewDiff = p.code, p.changes
				b.previewLanguage = p.language
				b.previewAdded, b.previewRemoved = p.added, p.removed
				b.previewImage = p.bitmap
				b.previewEntry = p.result.Entry
				b.previewTruncated = p.result.Truncated
				b.previewNote = p.note
			}
		})
	}
	read, client, comparison := b.reader(), b.client, b.comparison
	b.previewTask.Start(context.Background(), func(ctx context.Context, publish func(desktopFilePreview)) error {
		kind := "read"
		if diff {
			kind = "diff"
		}
		req := files.Request{Kind: kind, Path: path, Length: limit}
		if diff && comparison != nil {
			req.Kind, req.Root, req.OldTree, req.NewTree = "git-diff", comparison.Root, comparison.Base, comparison.Current.Tree
		}
		r, err := read(ctx, client, req)
		if err != nil {
			return err
		}
		preview := prepareFilePreview(ctx, r, path, diff, line, column)
		if err := ctx.Err(); err != nil {
			return err
		}
		publish(preview)
		return nil
	})
}

func prepareFilePreview(ctx context.Context, r files.Result, path string, diff bool, line, column int) desktopFilePreview {
	if ctx.Err() != nil {
		return desktopFilePreview{}
	}
	var bitmap *ui.Bitmap
	var code *editor.State
	var changes *editor.DiffState
	var note string
	if diff {
		if len(r.Data) == 0 {
			note = "此文件没有 Git 变更"
		} else if parsed, parseErr := fileDiffPreview(path, string(r.Data)); parseErr != nil {
			note = "无法解析 Git 变更：" + parseErr.Error()
			if text, ok := filePreviewText(r.Data, false); ok {
				code = fileCodePreview("preview.diff", text)
			}
		} else if parsed == nil {
			note = "此文件包含二进制或文件属性变更，可复制变更内容查看"
		} else {
			changes = parsed
		}
	}
	if !diff {
		config, _, imageErr := image.DecodeConfig(bytes.NewReader(r.Data))
		if imageErr == nil {
			if r.Truncated {
				note = "图片超过预览范围，请加载更多或下载"
			} else if config.Width <= 0 || config.Height <= 0 || config.Width > 16_000_000/config.Height {
				note = "图片尺寸过大，请下载查看"
			} else if img, _, err := image.Decode(bytes.NewReader(r.Data)); err == nil {
				bitmap = ui.NewBitmap(img)
			} else {
				note = "图片无法解码"
			}
		} else if _, ok := filePreviewText(r.Data, r.Truncated); !ok {
			note = "二进制文件，请下载查看"
		} else {
			text, _ := filePreviewText(r.Data, r.Truncated)
			code = fileCodePreview(path, text)
		}
	}

	language := filePreviewLanguage(path)
	if code != nil && line > 0 {
		code.GoTo(editor.Pos{Line: line - 1, Col: max(0, column-1)})
	}
	add, del := 0, 0
	if changes != nil {
		add, del = changes.Stats()
	}
	return desktopFilePreview{result: r, bitmap: bitmap, code: code, changes: changes, note: note, language: language, added: add, removed: del}
}

func (a *App) insertFilePath(b *desktopFiles, path string) {
	if !a.filesCurrent(b) {
		return
	}
	p := b.pane
	if !p.acceptsFileDrop() {
		b.err = "当前窗格不可输入，请先接入会话"
		return
	}
	p.term.Paste(droppedFilePaths([]string{path}))
	b.expanded = false
	a.toggleFiles()
	a.focusReq = p
}

// Download writes only a user-chosen local destination. Temporary partial
// files are owned by this request and removed when it is cancelled or fails.
func downloadFile(ctx context.Context, client *rex.Client, source, destination string, progress func(int64, int64)) error {
	r, err := fileRequest(ctx, client, files.Request{Kind: "stat", Path: source})
	if err != nil {
		return err
	}
	if r.Entry == nil || !r.Entry.Regular {
		return errors.New("仅支持下载普通文件")
	}
	if r.Entry.Size > 1<<30 {
		return errors.New("单个文件下载最多 1GB")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".retty-download-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	for offset := int64(0); offset < r.Entry.Size; {
		part, err := fileRequest(ctx, client, files.Request{Kind: "read", Path: source, Offset: offset, Length: 1 << 20, Version: r.Entry.Version})
		if err != nil {
			return err
		}
		if part.Offset != offset || len(part.Data) == 0 || offset+int64(len(part.Data)) > r.Entry.Size {
			return io.ErrUnexpectedEOF
		}
		if _, err = f.Write(part.Data); err != nil {
			return err
		}
		offset += int64(len(part.Data))
		progress(offset, r.Entry.Size)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, destination)
}

func (a *App) saveBrowserFile(b *desktopFiles, path string) {
	if !a.filesCurrent(b) || b.downloading {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.downloadCancel = cancel
	b.downloading = true
	b.downloadEpoch++
	epoch := b.downloadEpoch
	b.downloadProgress = "选择保存位置…"
	client, win := b.client, a.win
	go func() {
		defer cancel()
		destination, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: "保存文件", DefaultPath: filepath.Base(path)})
		if err == nil && destination != "" && ctx.Err() == nil {
			err = downloadFile(ctx, client, path, destination, func(done, total int64) {
				a.post(func() {
					if a.files == b && b.downloadEpoch == epoch {
						b.downloadProgress = fmt.Sprintf("%s / %s", fileSize(done), fileSize(total))
					}
				})
				if win != nil {
					win.Invalidate()
				}
			})
		}
		a.post(func() {
			if a.files != b || b.downloadEpoch != epoch {
				return
			}
			b.downloading = false
			b.downloadCancel = nil
			b.downloadProgress = ""
			if err != nil && !errors.Is(err, context.Canceled) {
				b.err = err.Error()
			}
		})
		if win != nil {
			win.Invalidate()
		}
	}()
}

func fileSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1<<20 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	if n < 1<<30 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

func filePreviewText(data []byte, truncated bool) (string, bool) {
	if bytes.ContainsRune(data, 0) {
		return "", false
	}
	if utf8.Valid(data) {
		return string(data), true
	}
	if truncated {
		for n := 1; n <= 3 && n <= len(data); n++ {
			if utf8.Valid(data[:len(data)-n]) && !utf8.FullRune(data[len(data)-n:]) {
				return string(data[:len(data)-n]), true
			}
		}
	}
	return "", false
}
