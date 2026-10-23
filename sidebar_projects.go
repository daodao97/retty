package main

import (
	"fmt"
	"path/filepath"
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
		git := a.paneGit(tab.Focus)
		if git == nil || git.CommonDir == "" {
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
		key := host + "\x00" + git.CommonDir
		at, found := byKey[key]
		if !found {
			label := filepath.Base(git.ProjectDir)
			if hostName != "" {
				label += " · " + hostName
			}
			at = len(groups)
			byKey[key] = at
			groups = append(groups, sidebarProjectGroup{key: key, label: label, dir: git.ProjectDir})
		}
		groups[at].tabs = append(groups[at].tabs, sidebarProjectTab{i, tab})
	}
	return groups
}
