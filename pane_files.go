package main

import (
	"cmp"

	"github.com/egoist/mygo/ui"
)

type savedFiles struct {
	CWD       string  `json:"cwd,omitempty"`
	Dir       string  `json:"dir,omitempty"`
	Preview   string  `json:"preview,omitempty"`
	Filter    string  `json:"filter,omitempty"`
	Base      string  `json:"base,omitempty"`
	SourceSID string  `json:"sourceSid,omitempty"`
	Turn      string  `json:"turn,omitempty"`
	Width     float32 `json:"width,omitempty"`
}

func (b *desktopFiles) kind() paneKind {
	if b.diff {
		return paneDiff
	}
	return paneFiles
}
func (b *desktopFiles) label(*Pane) (string, string) { return filePaneLabel(b) }
func (b *desktopFiles) render(a *App, c *ui.Context, k *colors, p *Pane) {
	if a.focusReq == p {
		b.filterFocus = true
		a.focusReq = nil
	}
	a.filesPanel(c, k, b)
}
func (b *desktopFiles) dispose(a *App, p *Pane, end bool) { b.stop() }
func (b *desktopFiles) snapshot(*Pane) *savedNode {
	return &savedNode{Files: &savedFiles{CWD: b.cwd, Dir: b.dir, Preview: cmp.Or(b.preview, b.restorePreview),
		Filter: cmp.Or(b.filter, b.restoreFilter), Base: b.base, SourceSID: b.sourceSID, Turn: b.turn, Width: b.width}}
}
func (b *desktopFiles) split(a *App, p *Pane, down bool) {
	if next := a.openFilesPane(p, b.dir, b.diff, down); next != nil {
		next.files().base = b.base
		a.loadFileDirectory(next.files(), b.dir)
	}
}
