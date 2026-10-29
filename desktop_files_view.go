package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"retty/internal/files"
)

func (a *App) filesToggle(c *ui.Context, k *colors) {
	label := "打开文件浏览器"
	if a.focusedFiles() != nil {
		label = "收起文件浏览器"
	}
	if compactTitleButton(c.Key("files-toggle"), k, "folder-open", label, a.focusedFiles() != nil).Tooltip(label + "  ⇧⌘E").Clicked() {
		a.toggleFiles()
		c.Invalidate()
	}
}

func (a *App) filesPanel(c *ui.Context, k *colors, b *desktopFiles) {
	if b.width < 160 || b.width > 600 {
		b.width = 260
	}
	available := b.pane.bounds.W
	if b.pane.Tab.Zoom == b.pane || a.desktopContentWidth(c) < 720 {
		available = a.desktopContentWidth(c)
	}
	if available <= 0 {
		available = a.desktopContentWidth(c) / 2
	}
	shown := min(b.width, max(120, available-240))
	split := desktopSplit(c.Key("files-split"), k, &shown, false, false, func() {
		b.width, prefs.FilesWidth = 260, 260
		a.changed()
		saveSettings()
	}, func() ui.Element {
		return a.fileTreePanel(c, k, b)
	}, func() {
		a.fileContentPanel(c, k, b)
	}).Fill()
	if split.Changed() {
		b.width = min(max(shown, 120), 600)
		prefs.FilesWidth = b.width
		a.changed()
		a.filesWidthSaveAt = c.Now().Add(250 * time.Millisecond)
	}
}

// Coalesce continuous pointer and keyboard resizing into one settings write.
func (a *App) saveFileWidth(c *ui.Context) {
	if a.filesWidthSaveAt.IsZero() {
		return
	}
	if wait := a.filesWidthSaveAt.Sub(c.Now()); wait > 0 {
		c.After(wait)
		return
	}
	a.filesWidthSaveAt = time.Time{}
	saveSettings()
}

func (a *App) fileTreePanel(c *ui.Context, k *colors, b *desktopFiles) ui.Element {
	return ui.Column(c.Key("files-tree-panel")).Fill().MinHeight(0).Background(k.track).Children(func() {
		ui.Row(c).Height(50).Padding(0, spaceXL).Gap(spaceS).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon("folder-open")).Size(15, 15).TextColor(k.iconMuted)
			ui.Text(c, "文件").FontSize(textSmall).FontWeight(600).TextColor(k.textMuted)
			ui.Spacer(c)
			host := "本机"
			if b != nil && b.host != nil {
				host = b.host.name()
			}
			ui.Text(c, host).FontSize(textCaption).TextColor(k.textFaint).SingleLine().Ellipsis("…").MinWidth(0)
		})
		if b == nil {
			a.fileMessage(c, k, "选择一个窗格以浏览文件")
			return
		}
		ui.Row(c).Height(30).Margin(0, spaceL, spaceM, spaceL).Padding(0, spaceM).Gap(spaceS).Radius(radiusM).Background(k.hover).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon("search")).Size(13, 13).TextColor(k.iconMuted).Shrink(0)
			input := ui.TextInputBase(c.Key("files-filter"), &b.filter).Label("筛选文件").Placeholder("筛选文件…").FontSize(textCaption).Grow(1).MinWidth(0).TextColor(k.text)
			if input.Changed() {
				a.changed()
			}
			if input.Focused() && b.pane.Tab.Focus != b.pane {
				b.pane.Tab.setFocus(b.pane)
				a.changed()
			}
			if b.filterFocus {
				input.Focus()
				b.filterFocus = false
			}
		})
		if b.loading {
			ui.Box(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(18, 18) })
			return
		}
		if b.preview == "" && b.err != "" {
			a.fileMessage(c, k, b.err)
		}
		entries := b.treeEntries()
		b.list.Key = func(i int) any { return entries[i].path }
		ui.List(c.Key("file-tree"), &b.list, len(entries), func(i int) { a.fileTreeRow(c, k, b, entries[i]) }).Grow(1).MinHeight(0).FillWidth().Padding(0, spaceS).Role(ui.RoleTree).Label("文件树")
		label := fmt.Sprintf("%d 项", len(entries))
		if b.diff {
			label = fmt.Sprintf("%d 个变更文件", len(b.entries))
		}
		if len(entries) == 0 {
			if b.diff {
				label = "工作区没有变更"
			} else {
				label = "没有匹配的文件"
			}
		}
		if b.truncated {
			label += " · 仅显示前 5000 项"
		}
		ui.Row(c).Height(30).Padding(0, spaceL).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(k.headerBorder).Children(func() { ui.Text(c, label).FontSize(textCaption).TextColor(k.textFaint) })
	})
}

func (a *App) fileContentPanel(c *ui.Context, k *colors, b *desktopFiles) {
	canvas := k.canvas
	ui.Column(c.Key("files-content")).Label("文件内容区").Grow(1).Basis(0).FillHeight().MinWidth(0).MinHeight(0).AlignItems(ui.Stretch).Background(canvas).Children(func() {
		ui.Row(c).Height(50).Padding(0, spaceXL).Gap(spaceM).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(k.headerBorder).Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				if b == nil {
					ui.Text(c, "文件浏览器").FontSize(textBody).FontWeight(600)
					return
				}
				name := filepath.Base(b.dir)
				if name == "." || name == "/" || name == "" {
					name = "文件浏览器"
				}
				ui.Text(c, name).FontSize(textBody).FontWeight(600).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…").TextColor(k.text).Tooltip(b.dir)
				if b.preview != "" {
					dir := filepath.Dir(filePreviewName(b))
					if dir != "." {
						ui.Text(c, strings.ReplaceAll(dir, string(filepath.Separator), " / ")).FontSize(textCaption).TextColor(k.textFaint).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
					}
				}
			})
			a.fileModeControl(c, k, b)
			iconButton(c, k, "ellipsis", "文件选项", 26, 15).Focusable().Disabled(b == nil).Menu(func(m *ui.Menu) {
				if b == nil {
					return
				}
				if m.Item("打开目录…").Chosen() {
					b.pathInput = b.dir
					b.pathEditing = true
				}
				if m.Item("上级目录").Disabled(b.loading || b.diff || filepath.Dir(b.dir) == b.dir).Chosen() {
					a.loadFileDirectory(b, filepath.Dir(b.dir))
				}
				if m.Item("刷新文件").Disabled(b.loading).Chosen() {
					a.loadFileDirectory(b, b.dir)
				}
				if m.Item("显示隐藏文件").Checked(prefs.FilesHidden).Disabled(b.diff).Chosen() {
					prefs.FilesHidden = !prefs.FilesHidden
					saveSettings()
					a.loadFileDirectory(b, b.dir)
				}
				if m.Item("回到初始目录").Chosen() {
					a.loadFileDirectory(b, b.cwd)
				}
				if m.Item("复制目录路径").Chosen() {
					c.Services().WriteClipboard(b.dir)
				}
				if b.host == nil && m.Item("在 Finder 中显示").Chosen() {
					mygo.Shell.ShowItemInFolder(b.dir)
				}
				if m.Item("关闭窗格").Chosen() {
					a.later(c, func() { a.closePane(b.pane) })
				}
			})
			if iconButton(c, k, "x", "关闭文件窗格", 24, 12).Clicked() {
				a.later(c, func() { a.closePane(b.pane) })
			}
		})
		if b == nil {
			return
		}
		if b.diff {
			ui.Row(c).Padding(spaceS, spaceL).FillWidth().Children(func() { a.fileBaseControl(c, k, b) })
		}
		if b.preview == "" {
			ui.Box(c).Grow(1).FillWidth().Center().Children(func() {
				ui.Column(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("file-code")).Size(28, 28).TextColor(k.iconMuted.Alpha(.5))
					text := "从文件树选择文件"
					if b.diff && !b.loading && len(b.entries) == 0 && b.err == "" {
						text = "工作区没有 Git 变更"
					}
					ui.Text(c, text).FontSize(textSmall).TextColor(k.textFaint)
				})
			})
			return
		}
		if b.expanded {
			ui.Spacer(c)
			return
		}
		ui.Column(c.Key("file-preview-card")).Label("文件内容卡片").Grow(1).MinHeight(0).AlignSelf(ui.Stretch).Margin(spaceXL).Radius(radiusL).Border(1, k.panelBorder).Background(k.panel).Clip().Children(func() { a.filePreviewView(c, k, b) })
	})
}

func (a *App) fileModeControl(c *ui.Context, k *colors, b *desktopFiles) {
	selected := 0
	if b != nil && b.diff {
		selected = 1
	}
	control := ui.SegmentedBase(c.Key("file-mode"), &selected, 2)
	control.Track.Label("文件浏览模式").Padding(space2XS).Gap(space2XS).Radius(radiusM).Background(k.hover).Disabled(b == nil).Children(func() {
		for i, label := range []string{"浏览", "Diff"} {
			button := control.Segment(i).Label(label).Height(25).MinWidth(43).Padding(0, spaceM).Radius(radiusS).Cursor(ui.CursorPointer)
			color, weight := k.textMuted, 400
			if i == selected {
				button.Background(k.panel)
				color, weight = k.text, 600
			} else if button.Hovered() {
				button.Background(k.pressed)
			}
			button.Children(func() { ui.Text(c, label).FontSize(textCaption).FontWeight(weight).TextColor(color) })
		}
	})
	if control.Track.Changed() && b != nil {
		a.later(c, func() { a.setFileMode(b, selected == 1) })
	}
}

func (a *App) setFileMode(b *desktopFiles, diff bool) {
	if !a.filesCurrent(b) || b.diff == diff {
		return
	}
	b.diff = diff
	prefs.FilesDiff = diff
	a.changed()
	saveSettings()
	a.loadFileDirectory(b, b.dir)
}

func (a *App) fileMessage(c *ui.Context, k *colors, text string) {
	ui.Text(c, text).FontSize(textCaption).TextColor(k.textMuted).Padding(spaceM, spaceM).FillWidth().Selectable()
}

func (a *App) filePreviewView(c *ui.Context, k *colors, b *desktopFiles) {
	header := ui.Row(c).Height(46).Padding(0, spaceL).Gap(spaceM).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(k.headerBorder)
	header.Children(func() {
		ui.Icon(c, icon("file-code")).Size(14, 14).TextColor(k.iconMuted).Shrink(0)
		name := filePreviewName(b)
		dir := filepath.Dir(name)
		ui.Row(c).Grow(1).MinWidth(0).Gap(0).AlignItems(ui.Center).Children(func() {
			if dir != "." {
				ui.Text(c, dir+string(filepath.Separator)).Font("JetBrains Mono, monospace").FontSize(textCaption).TextColor(k.textFaint).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			}
			ui.Text(c, filepath.Base(b.preview)).Label("预览文件名").Font("JetBrains Mono, monospace").FontSize(textSmall).FontWeight(600).TextColor(k.text).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(b.preview)
		})
		if b.previewDiff != nil {
			ui.Row(c).Padding(space2XS, spaceM).Gap(spaceS).Radius(radiusL).Background(k.hover).Shrink(0).Children(func() {
				ui.Text(c, fmt.Sprintf("+%d", b.previewAdded)).Font("monospace").FontSize(textCaption).FontWeight(600).TextColor(k.busy)
				ui.Text(c, fmt.Sprintf("−%d", b.previewRemoved)).Font("monospace").FontSize(textCaption).FontWeight(600).TextColor(colorsOf(c).danger)
			})
		} else if b.previewLanguage != "" {
			ui.Text(c, b.previewLanguage).FontSize(textCaption).TextColor(k.textFaint)
		}
		button := iconButton(c, k, "ellipsis", "预览操作", 24, 14).Focusable().Tooltip("文件操作")
		if !header.Hovered() && !button.Focused() && !b.expanded {
			button.Opacity(.3)
		}
		button.Menu(func(m *ui.Menu) {
			if m.Item("复制预览内容").Disabled(b.previewLoading || len(b.previewData) == 0 || b.previewCode == nil && !b.diff).Chosen() {
				c.Services().WriteClipboard(string(b.previewData))
			}
			if m.Item("复制路径").Chosen() {
				c.Services().WriteClipboard(b.preview)
			}
			if m.Item("插入当前终端").Disabled(b.sourcePane(a) == nil || !b.sourcePane(a).acceptsFileDrop()).Chosen() {
				a.insertFilePath(b, b.preview)
			}
			if m.Item("另存为…").Disabled(b.downloading).Chosen() {
				a.saveBrowserFile(b, b.preview)
			}
			label := "放大预览"
			if b.expanded {
				label = "收起预览"
			}
			if m.Item(label).Chosen() {
				a.later(c, func() { b.expanded = !b.expanded })
			}
			if m.Item("刷新预览").Disabled(b.previewLoading).Chosen() {
				a.previewFile(b, b.preview, b.diff, b.previewLimit)
			}
			if b.host == nil && m.Item("在 Finder 中显示").Chosen() {
				mygo.Shell.ShowItemInFolder(b.preview)
			}
		})
	})
	if b.err != "" {
		a.fileMessage(c, k, b.err)
	}
	if b.previewLoading {
		ui.Box(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(18, 18) })
		return
	}
	if b.previewNote != "" {
		a.fileMessage(c, k, b.previewNote)
	}
	if b.previewImage != nil {
		ui.Image(c, b.previewImage).Fit(ui.Contain).Grow(1).MinHeight(0).FillWidth().Label("图片预览")
	} else if b.previewDiff != nil {
		editor.DiffView(c.Key("file-preview-diff"), b.previewDiff).Grow(1).MinHeight(0).FillWidth().Label("文件变更预览")
	} else if b.previewCode != nil {
		editor.View(c.Key("file-preview-code"), b.previewCode).Grow(1).MinHeight(0).FillWidth()
	}
	if b.previewTruncated && !b.diff {
		label := "加载更多"
		if b.previewLimit >= files.MaxRead {
			label = "预览已达 4MB，请下载完整文件"
		}
		if ui.Button(c, label).FontSize(textCaption).Disabled(b.previewLimit >= files.MaxRead).Margin(spaceS, spaceM).Clicked() {
			a.later(c, func() { a.previewFile(b, b.preview, false, min(b.previewLimit*4, files.MaxRead)) })
		}
	}
	if !b.diff && b.previewEntry != nil {
		ui.Text(c, fileSize(b.previewEntry.Size)).FontSize(textCaption).TextColor(k.textFaint).Margin(spaceS, spaceL)
	}

	a.fileDownloadView(c, b)
}

func (a *App) fileDownloadView(c *ui.Context, b *desktopFiles) {
	if b.downloading {
		ui.Row(c).Padding(spaceS, spaceM).Gap(spaceS).Children(func() {
			ui.Text(c, b.downloadProgress).FontSize(textCaption).Grow(1)
			if ui.Button(c, "取消下载").FontSize(textCaption).Clicked() && b.downloadCancel != nil {
				b.downloadCancel()
			}
		})
	}
}

func (a *App) expandedFilePreview(c *ui.Context, k *colors) {
	b := a.focusedFiles()
	if b == nil || !b.expanded || b.preview == "" {
		return
	}
	ui.DialogBase(c.Key("expanded-file-preview"), &b.expanded, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		w, h := c.Size()
		panel.Column().Width(min(1000, w-48)).Height(h - 64).MinHeight(200).Background(k.panel).Radius(radiusL).Clip().Children(func() { a.filePreviewView(c, k, b) })
	})
}

func filePreviewName(b *desktopFiles) string {
	if path, err := filepath.Rel(b.dir, b.preview); err == nil {
		return path
	}
	return filepath.Base(b.preview)
}

func (a *App) fileDirectoryDialog(c *ui.Context, k *colors) {
	b := a.focusedFiles()
	if b == nil || !b.pathEditing {
		return
	}
	ui.DialogBase(c.Key("files-directory-dialog"), &b.pathEditing, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		width, _ := c.Size()
		panel.Column().Width(min(440, width-48)).Padding(spaceXL).Gap(spaceL).Background(k.panel).Radius(radiusL).Children(func() {
			ui.Text(c, "打开目录").FontSize(textTitle).FontWeight(600)
			input := ui.TextInput(c.Key("files-path"), &b.pathInput).Label("文件目录路径").Placeholder("输入绝对路径").FillWidth().FontSize(textSmall).AutoFocus()
			submit := input.Shortcut(0, ui.KeyEnter)
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "取消").Clicked() {
					b.pathEditing = false
				}
				if ui.Button(c, "打开").Clicked() {
					submit = true
				}
			})
			if submit {
				path := b.pathInput
				a.later(c, func() { b.pathEditing = false; a.loadFileDirectory(b, path) })
			}
		})
	})
}
