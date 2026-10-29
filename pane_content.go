package main

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"retty/internal/terminal"
)

type paneKind string

const (
	paneTerminal paneKind = "terminal"
	paneBrowser  paneKind = "browser"
	paneFiles    paneKind = "files"
	paneDiff     paneKind = "diff"
	paneBoard    paneKind = "board"
)

// Content owns its state and resources. The container owns placement, focus,
// bounds and identity. Session operations are a terminal capability only.
type paneContent interface {
	kind() paneKind
	label(*Pane) (string, string)
	render(*App, *ui.Context, *colors, *Pane)
	dispose(*App, *Pane, bool)
	snapshot(*Pane) *savedNode
	split(*App, *Pane, bool)
}

func (p *Pane) terminal() *terminalPane {
	if p == nil {
		return nil
	}
	v, _ := p.content.(*terminalPane)
	return v
}

func (p *Pane) browser() *browserPane {
	if p == nil {
		return nil
	}
	v, _ := p.content.(*browserPane)
	return v
}

func (p *Pane) files() *desktopFiles {
	if p == nil {
		return nil
	}
	v, _ := p.content.(*desktopFiles)
	return v
}

func (p *Pane) terminalView() *terminal.Terminal {
	if v := p.terminal(); v != nil {
		return v.term
	}
	return nil
}

func (p *Pane) sessionInfo() rex.SessionInfo {
	if v := p.terminal(); v != nil {
		return v.info
	}
	return rex.SessionInfo{}
}

func (p *Pane) directory() string {
	if p == nil {
		return ""
	}
	if v := p.terminal(); v != nil {
		if v.info.Dir != "" {
			return v.info.Dir
		}
		return v.startDir
	}
	if b := p.files(); b != nil && b.dir != "" {
		return b.dir
	}
	return p.dir
}

func (p *Pane) identity() string {
	if p.Key == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			panic(err)
		}
		p.Key = "pane-" + hex.EncodeToString(raw[:])
	}
	return p.Key
}

func (a *App) focusedFiles() *desktopFiles {
	if t := a.tab(); t != nil {
		return t.Focus.files()
	}
	return nil
}

// placePane is shared by all component types. Opening content never starts a
// PTY unless the chosen component is a terminal.
func (a *App) placePane(parent, p *Pane, down bool) {
	p.identity()
	if parent != nil {
		a.insertControlSplit(parent, p, down)
		return
	}
	t := a.singlePaneTab(p)
	at := min(a.active+1, len(a.tabs))
	a.tabs = slices.Insert(a.tabs, at, t)
	a.selectTab(at)
	a.focusReq = p
	a.changed()
}

func (a *App) split(down bool) {
	if t := a.tab(); t != nil && t.Focus != nil && t.Focus.content != nil {
		t.Focus.content.split(a, t.Focus, down)
	}
}

// Legacy layouts omit kind and identify browsers by their browser payload.
// Returning nil selects the existing session restoration path.
func (a *App) restorePaneContent(s *savedNode, host *desktopHost) *Pane {
	kind := s.Kind
	if kind == "" && s.Browser != nil {
		kind = paneBrowser
	}
	// The task board was a pane in an earlier version and is none now.
	if kind == "" || kind == paneTerminal || kind == paneBoard {
		return nil
	}
	var p *Pane
	switch kind {
	case paneBrowser:
		b := s.Browser
		if b == nil {
			b = &savedBrowser{URL: "about:blank"}
		}
		var err error
		p, err = a.newBrowserPane(b.ID, b.URL, host)
		if err != nil {
			p, _ = a.newBrowserPane(b.ID, "about:blank", host)
		}
	case paneFiles, paneDiff:
		b := &desktopFiles{cwd: s.Dir, dir: s.Dir, diff: kind == paneDiff, base: "head"}
		if saved := s.Files; saved != nil {
			b.cwd, b.dir, b.base, b.filter = saved.CWD, saved.Dir, saved.Base, saved.Filter
			b.sourceSID, b.turn, b.restorePreview = saved.SourceSID, saved.Turn, saved.Preview
			b.restoreFilter, b.width = saved.Filter, saved.Width
			if b.cwd == "" {
				b.cwd = s.Dir
			}
			if b.dir == "" {
				b.dir = b.cwd
			}
			if b.base != "turn" && b.base != "branch" {
				b.base = "head"
			}
		}
		p = &Pane{ID: a.id(), host: host, content: b}
		b.pane, b.host = p, host
	default:
		p = &Pane{ID: a.id(), host: host, content: &unavailablePane{saved: *s}}
	}
	p.Key, p.dir = s.PaneID, s.Dir
	return p
}

// A future/unsupported component must never be restored as a new shell.
type unavailablePane struct{ saved savedNode }

func (v *unavailablePane) kind() paneKind { return v.saved.Kind }
func (v *unavailablePane) label(*Pane) (string, string) {
	return "不支持的窗格", string(v.saved.Kind)
}
func (v *unavailablePane) render(a *App, c *ui.Context, k *colors, p *Pane) {
	ui.Text(c, "当前版本不支持此窗格："+string(v.saved.Kind)).Padding(spaceXL).TextColor(k.textMuted)
}
func (v *unavailablePane) dispose(*App, *Pane, bool) {}
func (v *unavailablePane) snapshot(*Pane) *savedNode { saved := v.saved; return &saved }
func (v *unavailablePane) split(*App, *Pane, bool)   {}

func filePaneLabel(b *desktopFiles) (string, string) {
	name := "文件"
	if b.diff {
		name = "Diff"
	}
	if b.preview != "" {
		return filepath.Base(b.preview), name
	}
	return name, shortDir(b.dir)
}
