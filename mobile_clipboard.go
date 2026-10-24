package main

import (
	"context"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"runtime"
	"slices"
)

func (m *mobileApp) pasteClipboard(s ui.Services) bool {
	if m.term == nil || m.reconnecting || m.background || m.imagePasteBusy {
		return true
	}
	if m.selected.Exited {
		m.error = "会话已结束，请重新选择会话"
		m.invalidate()
		return true
	}
	// Desktop headless previews use the UI tester's text clipboard. Actual iOS
	// uses the same MyGo clipboard API as native desktop image consumers.
	if runtime.GOOS == "ios" && slices.Contains(mygo.Clipboard.Formats(), transfer.PNG) {
		image, err := mygo.Clipboard.ReadFormat(transfer.PNG)
		if err != nil {
			m.error = "无法读取图片，请允许粘贴后重试"
			m.invalidate()
			return true
		}
		m.pasteClipboardImage(image)
	} else {
		m.term.Paste(s.ReadClipboard())
	}
	return true
}

func (m *mobileApp) pasteClipboardImage(image []byte) {
	if m.client == nil || m.term == nil || m.reconnecting || m.imagePasteBusy {
		return
	}
	if err := rex.ValidateClipboardImage(image); err != nil {
		m.error = err.Error()
		m.invalidate()
		return
	}
	m.imagePasteBusy = true
	m.error = ""
	m.imagePasteEpoch++
	epoch := m.imagePasteEpoch
	client, sid, term, generation := m.client, m.selected.ID, m.term, m.generation
	codex := sessionProgramName(m.selected) == "codex"
	thread := sessionAgentState(m.selected).SessionID
	ctx, cancel := context.WithCancel(context.Background())
	m.imagePasteCancel = cancel
	m.invalidate()
	post := mygo.RunOnMain
	if m.imagePastePost != nil {
		post = m.imagePastePost
	}
	go func() {
		defer cancel()
		var err error
		var path string
		if codex {
			path, err = client.UploadClipboardImage(ctx, sid, image, nil)
		} else {
			err = client.PasteImage(ctx, sid, image)
		}
		completed := err == nil && ctx.Err() == nil
		post(func() {
			if m.imagePasteEpoch != epoch {
				return
			}
			m.imagePasteBusy = false
			m.imagePasteCancel = nil
			if err != nil {
				m.error = err.Error()
			} else if codex && completed && m.client == client && m.generation == generation && m.selected.ID == sid && m.term == term && !m.background && !m.needsRecovery() && sessionProgramName(m.selected) == "codex" && sessionAgentState(m.selected).SessionID == thread {
				m.term.Paste(droppedFilePaths([]string{path}))
			}
			m.invalidate()
		})
	}()
}
func (m *mobileApp) cancelImagePaste() {
	m.imagePasteEpoch++
	if m.imagePasteCancel != nil {
		m.imagePasteCancel()
		m.imagePasteCancel = nil
	}
	m.imagePasteBusy = false
}
