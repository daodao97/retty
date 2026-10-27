package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
)

func (p *Pane) acceptsFileDrop() bool {
	return p.terminalView() != nil && !p.closed && (p.host == nil || !p.terminal().info.Exited && !p.terminal().remoteYielded && p.host.connected() && p.terminal().remoteView != nil && p.terminal().remoteView.inputReady())
}

func (a *App) pasteDroppedFiles(t *Tab, p *Pane, paths []string) {
	if !p.acceptsFileDrop() {
		return
	}
	if p.host != nil {
		a.uploadDroppedFiles(t, p, paths)
		return
	}
	text := droppedFilePaths(paths)
	if text == "" {
		return
	}
	t.setFocus(p)
	a.focusReq = p
	a.changed()
	p.terminal().term.Paste(text)
}

type paneUpload struct {
	cancel      context.CancelFunc
	sent, total atomic.Int64
	prepared    atomic.Bool
}

func (p *Pane) cancelFileUpload() {
	if p.terminal() == nil {
		return
	}
	if p.terminal().upload != nil {
		p.terminal().upload.cancel()
		p.terminal().upload = nil
	}
}

func (a *App) uploadDroppedFiles(t *Tab, p *Pane, paths []string) {
	if droppedFilePaths(paths) == "" {
		return
	}
	paths = append([]string(nil), paths...)
	a.uploadPaneData(t, p, func(ctx context.Context, client *rex.Client, sid string, progress func(int64, int64)) ([]string, error) {
		return client.UploadFiles(ctx, sid, paths, progress)
	}, nil)
}

func (a *App) uploadPaneData(t *Tab, p *Pane, send func(context.Context, *rex.Client, string, func(int64, int64)) ([]string, error), valid func() bool) {
	if p.terminal().upload != nil {
		a.err = "当前窗格正在上传文件，请等待完成或取消"
		return
	}
	t.setFocus(p)
	a.focusReq = p
	a.changed()
	ctx, cancel := context.WithCancel(context.Background())
	upload := &paneUpload{cancel: cancel}
	p.terminal().upload = upload
	client, generation, sid, term, stream := p.host.client, p.host.generation, p.terminal().SID, p.terminal().term, p.terminal().stream
	win := a.win
	invalidate := func() {
		if win != nil {
			win.Invalidate()
		}
	}
	invalidate()
	go func() {
		defer cancel()
		remotePaths, err := send(ctx, client, sid, func(sent, total int64) {
			upload.sent.Store(sent)
			upload.total.Store(total)
			upload.prepared.Store(true)
			invalidate()
		})
		completed := err == nil && ctx.Err() == nil
		a.post(func() {
			if p.terminal().upload != upload {
				return
			}
			p.terminal().upload = nil
			if a.quitting || p.closed {
				return
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					a.err = err.Error()
				}
				return
			}
			// Never focus another pane or paste through a replacement connection.
			if !completed || p.host.client != client || p.host.generation != generation || p.terminal().SID != sid || p.terminal().term != term || p.terminal().stream != stream || a.tab() != t || t.Focus != p || !p.acceptsFileDrop() || valid != nil && !valid() {
				return
			}
			p.terminal().term.Paste(droppedFilePaths(remotePaths))
		})
		invalidate()
	}()
}

func (a *App) fileUploadBar(c *ui.Context, k *colors, p *Pane) {
	upload := p.terminal().upload
	ui.Row(c.Key("file-upload")).Height(30).Padding(0, 8+paneActionInset(p), 0, 12).AlignItems(ui.Center).Gap(8).Background(k.panel).Children(func() {
		message := "正在准备上传…"
		if upload.prepared.Load() {
			sent, total := upload.sent.Load(), upload.total.Load()
			if sent == total {
				message = "正在确认上传…"
			} else {
				message = fmt.Sprintf("上传文件 %.0f%% · %.1f / %.1f MB", float64(sent)*100/float64(total), float64(sent)/(1<<20), float64(total)/(1<<20))
			}
		}
		ui.Text(c, message).FontSize(12).TextColor(k.textMuted).Grow(1)
		if iconButton(c, k, "x", "取消文件上传", 24, 13).Clicked() {
			p.cancelFileUpload()
		}
	})
}

// Treat each path as one shell argument, never as shell syntax. Leave a space
// after the last path so another argument can be typed without joining it.
func droppedFilePaths(paths []string) string {
	var words []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		switch {
		case strings.IndexFunc(path, unicode.IsControl) >= 0:
			// ANSI-C quoting keeps newlines and other controls out of terminal
			// input events, where they could submit an Agent prompt.
			quoted := strconv.Quote(path)
			words = append(words, "$'"+strings.ReplaceAll(quoted[1:len(quoted)-1], "'", "\\'")+"'")
		case strings.IndexFunc(path, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_-.", r))
		}) < 0:
			words = append(words, path)
		default:
			words = append(words, "'"+strings.ReplaceAll(path, "'", "'\\''")+"'")
		}
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ") + " "
}
