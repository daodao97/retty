package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/files"
	"retty/internal/gitstate"
	"retty/internal/rex"
)

type gitScope struct {
	host   *desktopHost
	client *rex.Client
	dir    string
}

type desktopGitEntry struct {
	status *gitstate.Status
	err    string
	next   time.Time
	busy   bool
}

type desktopGitState struct {
	entries map[gitScope]*desktopGitEntry
	pending int
}

func paneGitScope(p *Pane) gitScope {
	s := gitScope{host: p.host, dir: p.info.Dir}
	if s.dir == "" {
		s.dir = p.startDir
	}
	if p.host != nil {
		s.client = p.host.client
	}
	return s
}

func (a *App) paneGit(p *Pane) *gitstate.Status {
	if a.gitStatus == nil || prefs.HideGitStatus || p == nil || p.closed {
		return nil
	}
	if entry := a.gitStatus.entries[paneGitScope(p)]; entry != nil {
		return entry.status
	}
	return nil
}

// Git work is never done in a view build or on the PTY's control connection.
// A request belongs to its host, connection and directory, so a late remote
// response cannot fill another host's pane or a reconnected host's new cache.
func (a *App) syncGitStatus(c *ui.Context) {
	if prefs.HideGitStatus || a.quitting || a.win == nil && a.gitRead == nil {
		return
	}
	if a.gitStatus == nil {
		a.gitStatus = &desktopGitState{entries: make(map[gitScope]*desktopGitEntry)}
	}
	g := a.gitStatus
	var scopes []gitScope
	seen := make(map[gitScope]bool)
	add := func(p *Pane) {
		if p == nil || p.closed || p.host != nil && !p.host.connected() {
			return
		}
		scope := paneGitScope(p)
		if scope.dir != "" && !seen[scope] {
			scopes, seen[scope] = append(scopes, scope), true
		}
	}
	if t := a.tab(); t != nil {
		add(t.Focus)
		for _, p := range t.panes() {
			add(p)
		}
	}
	for _, t := range a.tabs {
		add(t.Focus)
	}
	for scope, entry := range g.entries {
		if !seen[scope] && !entry.busy {
			delete(g.entries, scope)
		}
	}
	if len(scopes) == 0 {
		return
	}
	c.After(3 * time.Second)
	for _, scope := range scopes {
		entry := g.entries[scope]
		if entry == nil {
			if len(g.entries) >= 256 {
				continue
			}
			entry = &desktopGitEntry{}
			g.entries[scope] = entry
		}
		if entry.busy || c.Now().Before(entry.next) || g.pending >= 4 {
			continue
		}
		entry.busy = true
		g.pending++
		entry.next = c.Now().Add(3 * time.Second)
		read := a.gitRead
		if read == nil {
			read = func(ctx context.Context, client *rex.Client, dir string) (files.Result, error) {
				return fileRequest(ctx, client, files.Request{Kind: "git-status", Path: dir})
			}
		}
		update, services := a.desktopDispatcher(), c.Services()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			result, err := read(ctx, scope.client, scope.dir)
			update(func() {
				g.pending--
				entry.busy = false
				if a.quitting || a.gitStatus != g || g.entries[scope] != entry {
					return
				}
				entry.err = ""
				if err != nil {
					entry.status = nil
					entry.err = err.Error()
					entry.next = time.Now().Add(15 * time.Second)
				} else {
					entry.status = result.Git
					if result.Git == nil {
						entry.next = time.Now().Add(30 * time.Second)
					}
				}
			})
			services.Invalidate()
		}()
	}
}

func gitSummary(s *gitstate.Status) string {
	if s == nil {
		return ""
	}
	parts := []string{s.Branch}
	if s.Operation != "" {
		parts = append(parts, s.Operation)
	}
	if s.Files > 0 {
		counts := fmt.Sprintf("+%d −%d", s.Added, s.Removed)
		if s.Truncated {
			counts += "（部分）"
		}
		parts = append(parts, counts)
	}
	return strings.Join(parts, " · ")
}

func (a *App) paneGitBar(c *ui.Context, k *colors, t *Tab, p *Pane) {
	s := a.paneGit(p)
	if s == nil || s.Files == 0 {
		return
	}
	label := gitSummary(s)
	tip := fmt.Sprintf("%s\n%s\n%d 个变更文件 · 上游 ↑%d ↓%d\n打开此窗格的 Git 变更", label, s.Root, s.Files, s.Ahead, s.Behind)
	b := ui.ButtonBase(c.Key("pane-git")).Label("Git 变更 "+label).Tooltip(tip).
		FillWidth().Height(23).Shrink(0).Padding(0, 12).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).FocusRing(false)
	if b.Hovered() || b.FocusVisible() {
		b.Background(k.hover)
	}
	b.Children(func() {
		ui.Icon(c, icon("git-branch")).Size(11, 11).TextColor(k.iconMuted).Shrink(0)
		ui.Text(c, label).FontSize(10).TextColor(k.textFaint).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
	})
	if b.Clicked() {
		a.later(c, func() { a.openPaneChanges(t, p, s.Root) })
	}
}

func (a *App) openPaneChanges(t *Tab, p *Pane, root string) {
	if p.closed || p.Tab != t {
		return
	}
	found := false
	for i, tab := range a.tabs {
		if tab == t {
			a.selectTab(i)
			found = true
			break
		}
	}
	if !found {
		return
	}
	t.setFocus(p)
	prefs.FilesOpen, prefs.FilesDiff = true, true
	prefs.FilesSidePanel = true
	a.focusReq = nil
	a.syncFiles()
	if b := a.files; b != nil && (!b.diff || b.dir != root) {
		b.diff = true
		a.loadFileDirectory(b, root)
	}
	saveSettings()
}
