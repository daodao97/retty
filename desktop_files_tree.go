package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egoist/mygo/async"
	"github.com/egoist/mygo/ui"
	"retty/internal/files"
)

type fileDirectory struct {
	open, loading, truncated bool
	entries                  []files.Entry
	err                      string
	task                     *async.Task[files.Result]
}
type fileTreeEntry struct {
	path  string
	entry files.Entry
	depth int
}

// Expand only the directory the user requests. No recursive disk/network scan.
func (a *App) toggleFileDirectory(b *desktopFiles, path string) {
	if !a.filesCurrent(b) {
		return
	}
	if d := b.dirs[path]; d != nil {
		if d.err == "" || d.open {
			d.open = !d.open
			return
		}
		if d.task != nil {
			d.task.Close()
		}
		delete(b.dirs, path) // Reopening a failed directory retries its read.
	}
	d := &fileDirectory{open: true, loading: true}
	b.dirs[path] = d
	if len(b.dirs) > 256 {
		d.loading, d.err = false, "已加载目录过多，请刷新或切换根目录"
		return
	}
	read, client, hidden := b.reader(), b.client, prefs.FilesHidden
	d.task = async.New(a.desktopDispatcher(), func(state async.State[files.Result]) {
		if !a.filesCurrent(b) || b.dirs[path] != d {
			return
		}
		switch state.Phase {
		case async.Failed:
			d.loading = false
			d.err = state.Err.Error()
		case async.Succeeded:
			d.loading = false
			r := state.Value
			count := len(r.Entries)
			for _, cached := range b.dirs {
				count += len(cached.entries)
			}
			if count > files.MaxPreviewRows {
				d.err = "已加载目录过多，请刷新或切换根目录"
				return
			}
			d.entries, d.truncated = r.Entries, r.Truncated
		}
	})
	d.task.Start(context.Background(), func(ctx context.Context, publish func(files.Result)) error {
		r, err := read(ctx, client, files.Request{Kind: "list", Path: path, Hidden: hidden})
		if err == nil {
			publish(r)
		}
		return err
	})
}

func changedFileTree(root string, entries []files.Entry) map[string]*fileDirectory {
	dirs := map[string]*fileDirectory{root: {open: true}}
	for _, e := range entries {
		name := filepath.Clean(e.Name)
		if filepath.IsAbs(name) || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(root, e.Name)
		parent := filepath.Dir(path)
		for dir := parent; dir != root; dir = filepath.Dir(dir) {
			if _, found := dirs[dir]; found {
				break
			}
			dirs[dir] = &fileDirectory{open: true}
		}
		e.Name = filepath.Base(path)
		dirs[parent].entries = append(dirs[parent].entries, e)
	}
	// All ancestors exist before linking, regardless of the order of Git paths.
	for path := range dirs {
		if path == root {
			continue
		}
		parent := filepath.Dir(path)
		if dirs[parent] == nil {
			dirs[parent] = &fileDirectory{open: true}
		}
		dirs[parent].entries = append(dirs[parent].entries, files.Entry{Name: filepath.Base(path), Directory: true})
	}
	for _, d := range dirs {
		sort.Slice(d.entries, func(i, j int) bool {
			x, y := d.entries[i], d.entries[j]
			if x.Directory != y.Directory {
				return x.Directory
			}
			return strings.ToLower(x.Name) < strings.ToLower(y.Name)
		})
	}
	return dirs
}

func (b *desktopFiles) treeEntries() []fileTreeEntry {
	var result []fileTreeEntry
	filter := strings.ToLower(b.filter)
	var visit func(string, int) bool
	visit = func(dir string, depth int) bool {
		d := b.dirs[dir]
		if d == nil {
			return false
		}
		anyMatch := false
		for _, e := range d.entries {
			if len(result) >= files.MaxPreviewRows {
				break
			}
			path := filepath.Join(dir, e.Name)
			start := len(result)
			result = append(result, fileTreeEntry{path: path, entry: e, depth: depth})
			match := filter == "" || strings.Contains(strings.ToLower(e.Name), filter)
			if child := b.dirs[path]; e.Directory && child != nil && (child.open || filter != "") {
				match = visit(path, depth+1) || match
			}
			if !match {
				result = result[:start]
			} else {
				anyMatch = true
			}
		}
		return anyMatch
	}
	visit(b.dir, 0)
	return result
}

func (a *App) fileTreeRow(c *ui.Context, k *colors, b *desktopFiles, item fileTreeEntry) {
	e, path := item.entry, item.path
	row := ui.Row(c.Key(path)).Label(e.Name).Role(ui.RoleTreeItem).Focusable().Cursor(ui.CursorPointer).Height(30).FillWidth().Radius(5).Padding(0, 8, 0, float32(8+min(item.depth, 12)*16)).Gap(6).AlignItems(ui.Center)
	if b.preview == path {
		row.Background(k.panelSel)
	} else if row.Hovered() {
		row.Background(k.hover)
	}
	row.Children(func() {
		glyph := "file-code"
		if e.Directory {
			glyph = "folder-open"
			arrow := "chevron-right"
			if d := b.dirs[path]; d != nil && d.open {
				arrow = "chevron-down"
			}
			ui.Icon(c, icon(arrow)).Size(12, 12).Shrink(0).TextColor(k.iconMuted)
		} else {
			ui.Box(c).Width(12).Shrink(0)
		}
		ui.Icon(c, icon(glyph)).Size(14, 14).Shrink(0).TextColor(k.iconMuted)
		ui.Text(c, e.Name).FontSize(11.5).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").TextColor(k.text).Tooltip(path)
		if e.Status != "" {
			color := k.busy
			if strings.Contains(e.Status, "D") {
				color = ui.Hex("#e56c6c")
			}
			ui.Text(c, e.Status).FontSize(10).Font("monospace").TextColor(color).Shrink(0)
		}
		if d := b.dirs[path]; d != nil {
			if d.loading {
				ui.Spinner(c).Size(12, 12)
			}
			if d.err != "" || d.truncated {
				ui.Text(c, "!").FontSize(10).Tooltip(d.err + "（目录最多 5000 项）")
			}
		}
	})
	if row.Clicked() || row.Shortcut(0, ui.KeyEnter) {
		a.later(c, func() {
			if e.Directory {
				a.toggleFileDirectory(b, path)
			} else {
				a.previewFile(b, path, b.diff, 256<<10)
			}
		})
	}
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("复制路径").Chosen() {
			c.Services().WriteClipboard(path)
		}
		if m.Item("插入当前终端").Disabled(!b.pane.acceptsFileDrop()).Chosen() {
			a.insertFilePath(b, path)
		}
		if !e.Directory && e.Regular && !strings.Contains(e.Status, "D") && m.Item("另存为…").Disabled(b.downloading).Chosen() {
			a.saveBrowserFile(b, path)
		}
		if e.Directory && m.Item("设为根目录").Chosen() {
			a.loadFileDirectory(b, path)
		}
	})
}
