package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"retty/internal/agents"
	"retty/internal/push"
	"retty/internal/rex"
	"strings"
)

var settingsSections = []struct{ title, label, glyph string }{
	{"外观", "Appearance", "sun"},
	{"终端", "Terminal settings", "square-terminal"},
	{"Agent", "Agent integrations", "bot"},
	{"关于", "About Retty", "circle-dot"},
	{"诊断", "Hook and version diagnostics", "circle-dot"},
}

type preferenceItem struct {
	id, title, label, detail, group string
	section                         int
	modified, disabled              bool
	control                         func(*ui.Context)
	reset                           func()
}

func (a *App) openSettings() {
	a.finishDesktopDialog()
	a.refreshAgentHooks()
	a.settingsOpen = true
	a.settingsQuery = ""
	a.settingsInitialFocus = true
	a.paletteOpen, a.renaming = false, nil
	a.focusReq = nil
}

// The full-window modal keeps typing away from the shell and restores
// terminal or search focus when it closes.
func (a *App) settingsPage(c *ui.Context, k *colors) {
	if !a.settingsOpen {
		return
	}
	original := c.Theme()
	theme := *original
	side, selected := ui.Hex("#f0f1f4"), ui.Hex("#dbe6fa")
	theme.Background, theme.Text, theme.TextMuted = ui.Hex("#fafbfc"), ui.Hex("#24272d"), ui.Hex("#777c85")
	if original.Dark {
		side, selected = ui.Hex("#202327"), ui.Hex("#293d60")
		theme.Background, theme.Text, theme.TextMuted = ui.Hex("#191c20"), ui.Hex("#d5d7db"), ui.Hex("#8d9198")
		theme.Surface, theme.Border = ui.Hex("#25282d"), ui.Hex("#3b3f46")
	}
	theme.Accent, theme.AccentHover, theme.AccentPressed = ui.Hex("#85aaf4"), ui.Hex("#99b9fa"), ui.Hex("#7099e5")
	theme.FontSize, theme.Spacing, theme.Radius = 13, 3.5, 5
	c.SetTheme(&theme)
	defer c.SetTheme(original)
	width, height := c.Size()
	sideWidth, contentPad := float32(216), float32(32)
	if width < 720 {
		sideWidth, contentPad = 152, 18
	}
	ui.DialogBase(c, &a.settingsOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(theme.Background)
		panel.Label("Settings").Size(width, height).Radius(0).Background(theme.Background).Clip()
		ui.Row(c).Fill().Children(func() {
			ui.Column(c.Key("settings-sidebar")).Width(sideWidth).FillHeight().Shrink(0).
				Background(side).BorderWidth(0, 1, 0, 0).BorderColor(theme.Border).Children(func() {
				ui.Box(c).FillWidth().Height(48).DragWindow()
				ui.Column(c).FillWidth().Padding(8, 12, 0, 12).Gap(12).Children(func() {
					ui.Text(c, "设置").Margin(0, 8).FontSize(11).TextColor(theme.TextMuted)
					ui.Row(c).FillWidth().Padding(7, 8).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icon("search")).Size(15, 15).TextColor(theme.TextMuted)
						in := ui.TextInputBase(c, &a.settingsQuery).Grow(1).MinWidth(0).
							Placeholder("搜索设置…").Label("Search settings").FontSize(12)
						if a.settingsInitialFocus {
							in.Focus()
						}
						if c.Shortcut(ui.Cmd, ui.KeyF) {
							in.Focus()
						}
						if a.settingsQuery != "" && iconButton(c, k, "x", "Clear settings search", 18, 11).Clicked() {
							a.settingsQuery = ""
							in.Focus()
						}
					})
				})
				ui.Scroll(c).Grow(1).MinHeight(0).Padding(12).Gap(5).Children(func() {
					for i, section := range settingsSections {
						active := a.settingsSection == i && strings.TrimSpace(a.settingsQuery) == "" && !a.settingsModifiedOnly
						b := ui.ButtonBase(c.Key(section.label)).Label(section.label).FillWidth().Height(30).
							Padding(0, 9).Gap(9).Radius(7).AlignItems(ui.Center).Justify(ui.Start)
						if active {
							b.Background(selected)
						} else if b.Hovered() {
							b.Background(theme.SurfaceHover)
						}
						b.Children(func() {
							ui.Icon(c, icon(section.glyph)).Size(15, 15).TextColor(theme.TextMuted)
							ui.Text(c, section.title).FontSize(13).FontWeight(500).TextColor(theme.Text)
						})
						if b.Clicked() {
							a.settingsSection, a.settingsQuery, a.settingsModifiedOnly = i, "", false
						}
					}
				})
				ui.Row(c).FillWidth().Height(42).Padding(0, 18).AlignItems(ui.Center).Children(func() {
					ui.Checkbox(c, &a.settingsModifiedOnly, "仅显示已修改").Label("Only modified settings").FontSize(11)
				})
			})
			ui.Column(c.Key("settings-main")).Grow(1).MinWidth(0).FillHeight().Children(func() {
				title := settingsSections[a.settingsSection].title
				if strings.TrimSpace(a.settingsQuery) != "" {
					title = "搜索结果"
				} else if a.settingsModifiedOnly {
					title = "已修改的设置"
				}
				ui.Row(c).FillWidth().Height(84).Padding(0, contentPad).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Grow(1).MaxWidth(640).Children(func() {
						ui.Text(c, title).FontSize(19).FontWeight(600).TextColor(theme.Text)
					})
				})
				ui.Scroll(c.Key(fmt.Sprintf("settings-content-%d", a.settingsSection))).Grow(1).MinHeight(0).
					FillWidth().Padding(0, contentPad, 28, contentPad).Children(func() {
					ui.Column(c).FillWidth().MaxWidth(640).AlignSelf(ui.Center).Children(func() { a.settingsContent(c, &theme) })
				})
				ui.Row(c).FillWidth().Height(44).Padding(0, contentPad).Justify(ui.End).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "更改会自动保存").FontSize(11).TextColor(theme.TextMuted).Margin(0, 12)
					if ui.Button(c, "完成").Label("Done").Clicked() {
						a.settingsOpen = false
					}
				})
			})
			if iconButton(c, k, "x", "Close Settings", 26, 16).Absolute().Top(9).Right(12).Focusable().Tooltip("关闭设置  Esc").Clicked() {
				a.settingsOpen = false
			}
		})
	})
	a.settingsInitialFocus = false
	if !a.settingsOpen {
		a.focusCurrentTerminal()
		c.Invalidate()
	}
}

func (a *App) preferenceItems() []preferenceItem {
	items := []preferenceItem{
		{id: "appearance", title: "主题", label: "Color theme", detail: "选择浅色、深色，或跟随系统的外观设置。", group: "颜色", section: 0,
			modified: prefs.Appearance != "", reset: func() { a.setAppearance("") },
			control: func(c *ui.Context) {
				values, selected := []string{"", "light", "dark"}, 0
				for i, value := range values {
					if prefs.Appearance == value {
						selected = i
					}
				}
				if ui.Segmented(c, &selected, "跟随系统", "浅色", "深色").Label("Color theme").Changed() {
					a.setAppearance(values[selected])
				}
			}},
		{id: "desktop-layout", title: "会话布局", label: "Session layout", detail: "Tab 模式使用经典顶部标签；侧边栏模式将标签放在左侧，可收起以扩大终端区域。", group: "布局", section: 0,
			modified: prefs.DesktopLayout != "", reset: func() { a.setDesktopLayout("") },
			control: func(c *ui.Context) {
				selected := 0
				if prefs.DesktopLayout == "sidebar" {
					selected = 1
				}
				if ui.Segmented(c, &selected, "Tab 模式", "侧边栏模式").Label("Session layout").Changed() {
					layout := ""
					if selected == 1 {
						layout = "sidebar"
					}
					a.setDesktopLayout(layout)
					c.Invalidate()
				}
			}},
		{id: "git-status", title: "Git 分支与变更", label: "Git status", detail: "在窗格中显示当前分支和变更摘要，点击打开 Diff。关闭后停止后台 Git 状态查询。", group: "布局", section: 0,
			modified: prefs.HideGitStatus, reset: func() { a.setGitStatus(true) },
			control: func(c *ui.Context) {
				show := !prefs.HideGitStatus
				if settingsSwitch(c, &show, "Git status").Changed() {
					a.setGitStatus(show)
				}
			}},
		{id: "diagnostics", title: "Hook 与版本诊断", label: "Open diagnostics", detail: "检查当前会话的 Hook 命令、工作目录、缺失路径和运行版本。只读检查，不执行 Hook 或重启服务。", group: "诊断", section: 4,
			control: func(c *ui.Context) {
				if ui.Button(c, "打开诊断").Label("Open diagnostics").Clicked() {
					a.openDiagnostics()
				}
			}},
		{id: "font-size", title: "字体大小", label: "Terminal font size", detail: "调整所有终端窗格的文字大小。也可使用 ⌘+、⌘− 和 ⌘0。", group: "文字", section: 1,
			modified: prefs.FontSize != defaultFontSize, reset: func() { a.setFontSize(defaultFontSize) },
			control: func(c *ui.Context) {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("%.1f pt", prefs.FontSize)).FontSize(13).TextColor(c.Theme().Text)
					size := float64(prefs.FontSize)
					step := ui.Stepper(c, &size, 8, 32, 0.5).Label("Terminal font size")
					if step.Focused() {
						step.ScrollIntoView()
					}
					if step.Changed() {
						a.setFontSize(float32(size))
						c.Invalidate()
					}
				})
			}},
		{id: "copy-content", title: "复制内容", label: "Selection copy content", detail: "显示文字：复制选区中的可见文字，保留列表序号。原始终端文本：同时保留隐藏字符。适用于选中自动复制、⌘C 和右键复制。", group: "复制", section: 1,
			modified: prefs.CopyRawText, reset: func() { a.setCopyRawText(false) },
			control: func(c *ui.Context) {
				selected := 0
				if prefs.CopyRawText {
					selected = 1
				}
				if ui.Segmented(c, &selected, "显示文字", "原始终端文本").Label("Selection copy content").Changed() {
					a.setCopyRawText(selected == 1)
				}
			}},
		{id: "link-editor", title: "打开文件的编辑器", label: "File link editor", detail: "⌘ 点击文件路径时默认在 Retty 文件查看器中打开，支持行列号跳转；也可选择外部编辑器。", group: "链接", section: 1,
			modified: prefs.LinkEditor != "", reset: func() { a.setLinkEditor("") },
			control: func(c *ui.Context) {
				values, labels := []string{"", "vscode", "cursor", "system"}, []string{"Retty 文件查看器", "VS Code", "Cursor", "系统默认"}
				selected := labels[0]
				for i, value := range values {
					if prefs.LinkEditor == value {
						selected = labels[i]
					}
				}
				if ui.Select(c, &selected, labels).Label("File link editor").Changed() {
					for i, label := range labels {
						if selected == label {
							a.setLinkEditor(values[i])
						}
					}
				}
			}},
		{id: "dev-servers", title: "发现开发地址", label: "Discover development URLs", detail: "从终端最近输出识别 localhost 等开发地址，点击后在同一主机的浏览器窗格中打开。不会自动打开或探测端口。", group: "链接", section: 1,
			modified: prefs.HideDevServers, reset: func() { prefs.HideDevServers = false; saveSettings() },
			control: func(c *ui.Context) {
				show := !prefs.HideDevServers
				if settingsSwitch(c, &show, "Discover development URLs").Changed() {
					prefs.HideDevServers = !show
					saveSettings()
				}
			}},
		{id: "desktop-notifications", title: "桌面通知", label: "Desktop notifications", detail: "在此电脑显示系统通知，包括任务完成、等待输入和终端提醒。", group: "通知", section: 2,
			modified: prefs.HideDesktopNotifications,
			reset:    func() { a.setDesktopNotifications(true) },
			control: func(c *ui.Context) {
				show := !prefs.HideDesktopNotifications
				if settingsSwitch(c, &show, "Desktop notifications").Changed() {
					a.setDesktopNotifications(show)
				}
			}},
		{id: "phone-notifications", title: "手机通知", label: "Phone notifications", detail: "离开电脑后，将此电脑的任务提醒推送到手机。关闭后，退出桌面应用也不推送。", group: "通知", section: 2,
			modified: prefs.HidePhoneNotifications,
			reset:    func() { a.setPhoneNotifications(true) },
			control: func(c *ui.Context) {
				show := !prefs.HidePhoneNotifications
				if settingsSwitch(c, &show, "Phone notifications").Changed() {
					a.setPhoneNotifications(show)
				}
			}},
		{id: "agent-completion-notifications", title: "完成或失败时提醒", label: "Agent completion notifications", detail: "后台 Tab 或未聚焦窗格中的 Agent 完成任务或执行失败时发送桌面通知。点击通知可返回对应窗格。", group: "通知", section: 2,
			modified: prefs.HideAgentCompletionNotifications,
			disabled: prefs.HideDesktopNotifications,
			reset:    func() { a.setAgentCompletionNotifications(true) },
			control: func(c *ui.Context) {
				show := !prefs.HideAgentCompletionNotifications
				if settingsSwitch(c, &show, "Agent completion notifications").Changed() {
					a.setAgentCompletionNotifications(show)
				}
			}},
		{id: "agent-notifications", title: "等待输入时提醒", label: "Agent waiting notifications", detail: "Agent 需要授权、回答或输入时发送桌面通知。正在查看的窗格不提醒，点击通知可定位窗格。", group: "通知", section: 2,
			modified: prefs.HideAgentNotifications,
			disabled: prefs.HideDesktopNotifications,
			reset:    func() { a.setAgentNotifications(true) },
			control: func(c *ui.Context) {
				show := !prefs.HideAgentNotifications
				if settingsSwitch(c, &show, "Agent waiting notifications").Changed() {
					a.setAgentNotifications(show)
				}
			}},
	}
	for _, id := range agents.Integrated {
		name := programOf(id).Name
		s := a.agentHooks[id]
		status, action, verb := "尚未接入", "启用接入", "Install"
		if s.Installed {
			status, action, verb = "已安装接入", "移除接入", "Remove"
		} else if s.Present {
			status, action = "接入不完整", "修复接入"
		}
		detail := status + "。安装到用户配置，保留已有 hooks；在新启动的 Agent 会话中生效。"
		if id == "codex" {
			detail += " 正常启动 codex，并在 /hooks 中信任 Retty 新增项；已安装不代表已信任。"
		}
		if (id == "gemini" || id == "qwen") && a.hello.Version > 0 && a.hello.Version < 5 {
			detail += " 当前后台服务需更新；结束现有任务后，使用 Shell → Quit and End All Sessions，再打开新版 Retty。"
		}
		if s.Error != "" {
			detail = "无法读取配置：" + s.Error
		}
		items = append(items, preferenceItem{id: "agent-hook-" + id, title: name, label: name + " integration", detail: detail, group: "状态接入", section: 2,
			modified: s.Present, disabled: a.agentHookBusy != "",
			reset: func() { a.changeAgentHooks(id, false) },
			control: func(c *ui.Context) {
				if a.agentHookBusy == id {
					ui.Text(c, "处理中…").FontSize(12)
					return
				}
				if ui.Button(c, action).Label(verb + " " + name + " integration").Clicked() {
					a.changeAgentHooks(id, !s.Installed)
				}
			},
		})
	}
	return items
}

func (a *App) refreshAgentHooks() {
	a.agentHooks = map[string]agents.HookInstallation{}
	for _, id := range agents.Integrated {
		a.agentHooks[id] = agents.InspectHooks(id)
	}
}

func (a *App) changeAgentHooks(id string, install bool) {
	if a.agentHookBusy != "" {
		return
	}
	a.agentHookBusy, a.agentHookError = id, ""
	update := a.desktopDispatcher()
	go func() {
		err := agents.SetHooks(id, install)
		update(func() {
			a.agentHookBusy = ""
			if err != nil {
				a.agentHookError = err.Error()
			}
			a.refreshAgentHooks()
		})
	}()
}

func (a *App) setDesktopNotifications(show bool) {
	prefs.HideDesktopNotifications = !show
	if !show {
		a.closeNotices(func(string) bool { return true })
	}
	saveSettings()
	a.reportDesktopPushPresence()
}

func (a *App) setPhoneNotifications(show bool) {
	prefs.HidePhoneNotifications = !show
	saveSettings()
	// Refresh an existing worker immediately; do not start or replace one.
	dir := rex.Dir()
	go func() { _, _ = push.Query(dir) }()
}

func (a *App) setAgentNotifications(show bool) {
	prefs.HideAgentNotifications = !show
	if !show {
		a.closeNotices(func(kind string) bool { return kind == agents.Waiting })
	}
	saveSettings()
}

func (a *App) setAgentCompletionNotifications(show bool) {
	prefs.HideAgentCompletionNotifications = !show
	if !show {
		a.closeNotices(agents.Finished)
	}
	saveSettings()
}

// closeNotices closes the notices of the kinds the user turned off.
func (a *App) closeNotices(off func(kind string) bool) {
	for sid, n := range a.agentNotices {
		if off(n.kind) {
			a.closeAgentNotice(sid)
		}
	}
}

func settingsSwitch(c *ui.Context, value *bool, label string) ui.Element {
	sw := ui.Switch(c, value).Label(label)
	if sw.Focused() {
		sw.ScrollIntoView()
	}
	if sw.Changed() {
		c.Invalidate()
	}
	return sw
}

func (a *App) settingsContent(c *ui.Context, theme *ui.Theme) {
	query := strings.ToLower(strings.TrimSpace(a.settingsQuery))
	if a.settingsSection == 4 && query == "" && !a.settingsModifiedOnly {
		a.diagnosticsPage(c, theme)
		return
	}
	if a.settingsSection == 3 && query == "" && !a.settingsModifiedOnly {
		ui.Text(c, "Retty").FontSize(17).FontWeight(600).TextColor(theme.Text).Margin(12, 0)
		ui.Text(c, "Reconnect TTY · Relay TTY").FontSize(13).TextColor(theme.TextMuted)
		ui.Text(c, "换个屏幕，接着工作。电脑、手机与服务器上的持久终端会话。").FontSize(12).TextColor(theme.TextMuted).Margin(16, 0)
		ui.Text(c, "开源地址").FontSize(11).TextColor(theme.TextMuted).Margin(12, 0, 6, 0)
		ui.Link(c, "https://github.com/daodao97/retty", "https://github.com/daodao97/retty").FontSize(12).FillWidth()
		return
	}
	if a.agentHookError != "" && (a.settingsSection == 2 || query != "") {
		ui.Text(c, a.agentHookError).FontSize(12).TextColor(ui.Hex("#e56c6c")).Margin(8, 0)
	}
	group, count := "", 0
	for _, item := range a.preferenceItems() {
		if query == "" && !a.settingsModifiedOnly && item.section != a.settingsSection {
			continue
		}
		if a.settingsModifiedOnly && !item.modified {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(item.title+" "+item.detail+" "+item.label+" "+item.group+" "+settingsSections[item.section].title), query) {
			continue
		}
		key := fmt.Sprintf("%d-%s", item.section, item.group)
		if key != group {
			if count > 0 {
				ui.Box(c).FillWidth().Height(1).Background(theme.Border).Margin(24, 0)
			}
			name := item.group
			if query != "" || a.settingsModifiedOnly {
				name = settingsSections[item.section].title + " · " + name
			}
			ui.Text(c, name).FontSize(14).FontWeight(600).TextColor(theme.Text).Margin(12, 0, 14, 0)
			group = key
		}
		ui.Row(c.Key(item.id)).FillWidth().Padding(14, 0).Gap(20).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
				ui.Text(c, item.title).FontSize(13).FontWeight(500).TextColor(theme.Text)
				ui.Text(c, item.detail).FontSize(11.5).TextColor(theme.TextMuted)
				if item.modified {
					ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "已修改").FontSize(11).TextColor(theme.TextMuted)
						b := ui.ButtonBase(c).Label("Restore default "+item.label).Padding(2, 0).Radius(3).Disabled(item.disabled)
						b.Children(func() { ui.Text(c, "恢复默认值").FontSize(11).FontWeight(500).TextColor(theme.Text) })
						if b.Clicked() {
							item.reset()
							c.Invalidate()
						}
					})
				}
			})
			ui.Row(c).Shrink(0).Disabled(item.disabled).Children(func() { item.control(c) })
		})
		count++
	}
	if count == 0 {
		message := "未找到匹配的设置"
		if query == "" {
			message = "所有设置均使用默认值"
		}
		ui.Text(c, message).FontSize(13).TextColor(theme.TextMuted).Margin(24, 0)
	}
}
