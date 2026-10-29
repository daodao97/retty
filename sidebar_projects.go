package main

import (
	"fmt"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
)

type sidebarProjectTab struct {
	index int
	tab   *Tab
}

type sidebarProjectGroup struct {
	key, label, dir string
	tabs            []sidebarProjectTab
}

// sidebarProjects changes presentation only. Stable tab IDs, saved order,
// selection and pane resume identities remain owned by the existing tabs.
// A project's identity always includes the host: identical paths on two
// machines never become a single group.
func (a *App) sidebarProjects() []sidebarProjectGroup {
	var groups []sidebarProjectGroup
	byKey := make(map[string]int)
	for i, tab := range a.tabs {
		p := tab.Focus
		for _, candidate := range tab.panes() {
			if candidate.terminal() != nil {
				p = candidate
				break
			}
		}
		git := a.paneGit(p)
		if prefs.SidebarSessionMode == "project" && a.gitStatus != nil && p != nil {
			if entry := a.gitStatus.entries[paneGitScope(p)]; entry != nil {
				git = entry.status
			}
		}
		identity, dir := "", ""
		if git != nil && git.CommonDir != "" {
			identity, dir = "git:"+git.CommonDir, git.ProjectDir
		}
		if identity == "" && prefs.SidebarSessionMode == "project" {
			dir = strings.ReplaceAll(p.directory(), "\\", "/")
			if dir != "" {
				dir = path.Clean(dir)
			}
			identity = "dir:" + dir
		}
		if identity == "" {
			groups = append(groups, sidebarProjectGroup{key: fmt.Sprintf("tab:%d", tab.ID), tabs: []sidebarProjectTab{{i, tab}}})
			continue
		}
		host, hostName := "local", ""
		h := tab.Host
		if h == nil && tab.Focus != nil {
			h = tab.Focus.host
		}
		if h != nil {
			host, hostName = "remote:"+desktopLayoutKey(h), h.name()
			if host == "remote:" {
				host = fmt.Sprintf("remote:%p", h)
			}
		}
		key := host + "\x00" + identity
		at, found := byKey[key]
		if !found {
			label := path.Base(strings.ReplaceAll(dir, "\\", "/"))
			if dir == "" {
				label = "其他会话"
			}
			if hostName != "" {
				label += " · " + hostName
			}
			at = len(groups)
			byKey[key] = at
			groups = append(groups, sidebarProjectGroup{key: key, label: label, dir: dir})
		}
		groups[at].tabs = append(groups[at].tabs, sidebarProjectTab{i, tab})
	}
	return groups
}

func (a *App) sidebarProjectTabs(c *ui.Context, k *colors, groups []sidebarProjectGroup) {
	for _, group := range groups {
		open := a.sidebarProjectOpen[group.key]
		name, tooltip := group.label, group.dir
		ui.Column(c.Key(group.key)).FillWidth().Shrink(0).Gap(spaceXS).Children(func() {
			header := ui.ButtonBase(c.Key("project-header")).Label("项目 "+name).Tooltip(tooltip).
				Expanded(open).FillWidth().Height(36).Shrink(0).Padding(0, spaceM).Gap(spaceM).Radius(radiusM).AlignItems(ui.Center)
			if header.Hovered() {
				header.Background(k.hover)
			}
			header.Children(func() {
				ui.Icon(c, icon("folder-open")).Size(17, 17).TextColor(k.iconMuted).Shrink(0)
				ui.Text(c, name).FontSize(textSmall).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
				glyph := "chevron-right"
				if open {
					glyph = "chevron-down"
				}
				ui.Icon(c, icon(glyph)).Size(12, 12).TextColor(k.iconMuted).Shrink(0)
			})
			if header.Clicked() {
				a.sidebarProjectOpen[group.key] = !open
				c.Invalidate()
			}
			if open {
				ui.Column(c.Key("project-tabs")).FillWidth().Shrink(0).Padding(0, 0, 0, spaceXL).Gap(spaceXS).Children(func() {
					for _, item := range group.tabs {
						a.sidebarTab(c, k, item.index, item.tab)
					}
				})
			}
		})
	}
}
