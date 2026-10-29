package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

// The connection drawer overlays the terminal, so opening it never resizes a PTY.
func (a *App) desktopConnectionDialog(c *ui.Context, k *colors) {
	if !a.desktops.open {
		return
	}
	original := c.Theme()
	theme := pageTheme(original)
	c.SetTheme(&theme)
	defer func() {
		c.SetTheme(original)
		c.Root().Background(terminalBackground(c))
	}()
	width, height := c.Size()
	ui.DialogBase(c.Key("desktop-connection-drawer"), &a.desktops.open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		panel.Absolute().Right(0).Top(compactTitleH).Width(min(384, width)).Height(max(height-compactTitleH, 0)).
			Column().Background(theme.Background).BorderWidth(0, 0, 0, 1).BorderColor(k.headerBorder).Clip().Label("连接侧边栏")
		a.desktopConnectionHeader(c)
		switch {
		case a.desktops.showLocal:
			a.desktopLocalConnection(c)
		case a.desktops.selected != nil:
			a.desktopHostSessions(c, a.desktops.selected)
		default:
			a.desktopConnectionHome(c)
		}
	})
	if !a.desktops.open {
		a.finishDesktopDialog()
	}
}

func (a *App) desktopConnectionHeader(c *ui.Context) {
	ui.Row(c).FillWidth().Height(48).Padding(0, spaceL).Gap(spaceS).AlignItems(ui.Center).Background(colorsOf(c).track).
		BorderWidth(0, 0, 1, 0).BorderColor(colorsOf(c).headerBorder).Children(func() {
		title := "连接"
		if a.desktops.showLocal || a.desktops.selected != nil {
			if desktopConnectionIconAction(c, "返回", "chevron-left").Clicked() {
				a.desktops.showLocal, a.desktops.selected, a.desktops.err = false, nil, ""
			}
			if a.desktops.showLocal {
				title = "连接此电脑"
			} else if h := a.desktops.selected; h != nil {
				title = h.name()
			}
		} else {
			ui.Icon(c, icon("plug-connected")).Size(17, 17).TextColor(c.Theme().Accent).Margin(0, spaceXS)
		}
		ui.Text(c, title).FontSize(textTitle).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").PassThrough()
		if h := a.desktops.selected; h != nil && !a.desktops.showLocal {
			if desktopConnectionIconAction(c, "新建远端会话", "plus").Disabled(!h.connected() || h.creating).Clicked() {
				a.createDesktopSession(h, "", nil, false)
			}
			if h.creating {
				ui.Spinner(c).Size(14, 14)
			}
		} else if !a.desktops.showLocal {
			if desktopConnectionIconAction(c, "显示本机连接二维码", "qr-code").Tooltip("本机二维码").Clicked() {
				a.openPhonePair()
			}
		}
		if desktopConnectionIconAction(c, "关闭桌面连接", "x").Clicked() {
			a.finishDesktopDialog()
		}
	})
}

func (a *App) pasteDesktopConnection(c *ui.Context) {
	a.desktops.input = strings.TrimSpace(c.ReadClipboard())
	if a.desktops.input == "" {
		a.desktops.err = "剪贴板中没有连接码。"
		return
	}
	a.desktops.err = ""
	a.submitDesktopConnection()
}

func (a *App) desktopConnectionHome(c *ui.Context) {
	ui.Scroll(c.Key("desktop-connection-home")).Grow(1).MinHeight(0).FillWidth().Padding(spaceXL).Gap(spaceL).Children(func() {
		desktopConnectionGroup(c).Padding(spaceL).Gap(spaceL).Children(func() {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(36, 36).Radius(radiusM).Background(colorsOf(c).panelSel).Center().Children(func() {
					ui.Icon(c, icon("plug-connected")).Size(20, 20).TextColor(c.Theme().Accent)
				})
				desktopConnectionText(c, mobileListTextOptions{Title: "连接另一台电脑", Subtitle: "也可以连接运行 Retty 的服务器"})
			})
			if ui.PrimaryButton(c, "").Label("粘贴桌面连接码").Role(ui.RoleButton).Height(34).FillWidth().Children(func() {
				ui.Icon(c, icon("clipboard-paste")).Size(15, 15)
				ui.Text(c, "粘贴连接码").FontSize(textBody)
			}).Clicked() {
				a.pasteDesktopConnection(c)
			}
		})
		if a.desktops.err != "" {
			connectionStatusCard(c, "无法连接", a.desktops.err, false, nil)
		}
		a.desktopHostRows(c)
		a.desktopRecentSessionRows(c)
		a.desktopIncomingRows(c)
	})
	ui.Row(c).FillWidth().Height(64).Shrink(0).Padding(0, spaceXL).Gap(spaceM).AlignItems(ui.Center).
		Background(colorsOf(c).track).BorderWidth(1, 0, 0, 0).BorderColor(colorsOf(c).headerBorder).Children(func() {
		mobileListIcon(c, "monitor", colorsOf(c).iconMuted)
		name := a.hello.Host.Name
		if name == "" {
			name = "本机"
		}
		desktopConnectionText(c, mobileListTextOptions{Title: name, Subtitle: "此电脑 · 端到端加密连接"})
		if desktopConnectionIconAction(c, "新建本地会话", "plus").Clicked() {
			a.later(c, func() { a.newLocalTab(a.hello.Host.Home); a.finishDesktopDialog() })
		}
	})
}

func (a *App) desktopHostRows(c *ui.Context) {
	ui.Column(c).FillWidth().Children(func() {
		desktopConnectionSection(c, "最近连接", func() {
			if len(a.desktops.hosts) == 0 {
				return
			}
			label, glyph := "清除桌面连接记录", "trash-2"
			if a.desktops.historySelection != nil {
				label, glyph = "确认清除桌面连接记录", "check"
			}
			if desktopConnectionIconAction(c, label, glyph).Clicked() {
				a.desktops.qualityHost = nil
				if a.desktops.historySelection == nil {
					a.desktops.historySelection = map[string]bool{}
				} else {
					selection := a.desktops.historySelection
					a.later(c, func() { a.removeDesktopHistory(selection) })
				}
			}
		})
		desktopConnectionGroup(c).Children(func() {
			if len(a.desktops.hosts) == 0 {
				mobileListEmpty(c, "连接过的桌面会显示在这里")
			}
			for i, h := range slices.Clone(a.desktops.hosts) {
				if i > 0 {
					desktopConnectionDivider(c)
				}
				status, color := "未连接", c.Theme().TextMuted
				if h.connected() {
					status, color = "已连接", colorsOf(c).busy.Mix(color, .25)
				} else if h.busy {
					status = "连接中"
				} else if h.err != "" {
					status = "已断开"
				}
				selecting, checked := a.desktops.historySelection != nil, a.desktops.historySelection[h.key]
				label := "查看电脑 " + h.name()
				if selecting {
					label = "选择电脑 " + h.name()
				}
				row := desktopConnectionRow(c, "desktop-"+h.key, label, 52).Value(status)
				if selecting {
					row.Role(ui.RoleCheckBox).Checked(checked)
				}
				row.Children(func() {
					if selecting {
						mobileHistoryCheckbox(c, checked)
					} else {
						platform := desktopPlatformProgram(h.recent.OS)
						mobileListIcon(c, platform.Glyph, colorsOf(c).iconMuted)
					}
					desktopConnectionText(c, mobileListTextOptions{Title: h.name()})
					ui.Row(c).Gap(spaceXS).Shrink(0).AlignItems(ui.Center).Children(func() {
						if h.busy {
							mobileReconnectIcon(c, 12, c.Theme().Accent)
						} else {
							ui.Box(c).Size(6, 6).Radius(3).Background(color)
						}
						ui.Text(c, status).FontSize(textSmall).TextColor(c.Theme().TextMuted)
					})
					if !selecting {
						a.desktopQualityTip(c, h)
						mobileListChevron(c)
					}
				})
				if row.Clicked() {
					if selecting {
						a.desktops.historySelection[h.key] = !checked
					} else {
						a.desktops.selected, a.desktops.err = h, ""
						if !h.connected() && !h.busy {
							a.connectDesktop(h)
						}
					}
				}
			}
		})
	})
}

func (a *App) desktopRecentSessionRows(c *ui.Context) {
	if len(a.desktops.recentSessions) == 0 {
		return
	}
	ui.Column(c).FillWidth().Children(func() {
		desktopConnectionSection(c, "最近会话", nil)
		desktopConnectionGroup(c).Children(func() {
			rowIndex := 0
			for _, entry := range slices.Clone(a.desktops.recentSessions) {
				var host *desktopHost
				for _, h := range a.desktops.hosts {
					if h.key == entry.Desktop {
						host = h
						break
					}
				}
				if host == nil {
					continue
				}
				if rowIndex > 0 {
					desktopConnectionDivider(c)
				}
				rowIndex++
				row := desktopConnectionRow(c, "recent-"+entry.Desktop+entry.Session, "打开最近远端会话 "+entry.Session, 64)
				row.Children(func() {
					prog := programOf(entry.Program)
					mobileListIcon(c, prog.Glyph, programIconColor(c, prog))
					desktopConnectionText(c, mobileListTextOptions{Title: entry.Title, Subtitle: host.name()})
					mobileListChevron(c)
				})
				if row.Clicked() {
					h := host
					a.later(c, func() {
						a.desktops.selected, a.desktops.err = h, ""
						if h.connected() {
							a.openDesktopRecentSession(h, entry.Session)
						} else {
							h.pendingSession = entry.Session
							if !h.busy {
								a.connectDesktop(h)
							}
						}
					})
				}
			}
		})
	})
}

func (a *App) desktopHostSessions(c *ui.Context, h *desktopHost) {
	ui.Scroll(c.Key("desktop-sessions-" + h.key)).Grow(1).MinHeight(0).FillWidth().Padding(spaceXL).Gap(spaceXL).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "会话").FontSize(textBody).TextColor(c.Theme().TextMuted).Grow(1)
			ui.Text(c, fmt.Sprint(len(h.sessions))).FontSize(textBody).TextColor(c.Theme().TextMuted)
			desktopConnectionIconAction(c, "管理电脑 "+h.name(), "settings-2").Menu(func(m *ui.Menu) {
				if m.Item("断开连接").Disabled(!h.connected() && !h.busy).Chosen() {
					a.laterFrom(a.services, func() { a.disconnectDesktop(h) })
				}
				if m.Item("移除连接记录").Chosen() {
					a.laterFrom(a.services, func() { a.removeDesktopHistory(map[string]bool{h.key: true}) })
				}
			})
		})
		if h.busy || !h.connected() {
			title, body := "正在连接", "连接恢复后即可打开会话，远端任务会继续运行。"
			if !h.busy {
				title, body = "连接已断开", "点击重试连接，继续远端会话。"
			}
			if h.err != "" {
				body = h.err
			}
			connectionStatusCard(c, title, body, h.busy, func() {
				ui.Row(c).FillWidth().Gap(spaceXS).Children(func() {
					if desktopConnectionIconAction(c, "重连电脑 "+h.name(), "rotate-ccw").Disabled(h.busy).Clicked() {
						a.connectDesktop(h)
					}
					if h.busy && desktopConnectionIconAction(c, "取消连接电脑 "+h.name(), "x").Clicked() {
						a.later(c, func() { a.disconnectDesktop(h) })
					}
				})
			})
		}
		if a.desktops.err != "" {
			ui.Text(c, a.desktops.err).FontSize(textBody).TextColor(c.Theme().Danger)
		}
		desktopConnectionGroup(c).Children(func() {
			if len(h.sessions) == 0 {
				mobileListEmpty(c, "还没有会话，点击右上角 + 新建")
			}
			for i, in := range slices.Clone(h.sessions) {
				if i > 0 {
					desktopConnectionDivider(c)
				}
				p := &Pane{content: &terminalPane{info: in, startDir: in.Dir}}
				name, _ := p.label()
				row := desktopConnectionRow(c, "remote-session-"+h.key+in.ID, "打开远端会话 "+in.ID, 64).Disabled(!h.connected() || h.creating)
				row.Children(func() {
					prog := paneProgram(p)
					mobileListIcon(c, prog.Glyph, programIconColor(c, prog))
					desktopConnectionText(c, mobileListTextOptions{Title: name, Subtitle: remoteShortDir(in.Dir, h.hello.Host.Home)})
					mobileListChevron(c)
				})
				if row.Clicked() {
					a.later(c, func() { a.openDesktopSession(h, in, true) })
				}
			}
		})
	})
}

func (a *App) desktopLocalConnection(c *ui.Context) {
	ui.Scroll(c.Key("desktop-local-connection")).Grow(1).MinHeight(0).FillWidth().Padding(spaceXL).Gap(spaceXL).Children(func() {
		ui.Text(c, "手机扫码，或在另一台电脑粘贴连接码。").FontSize(textBody).TextColor(c.Theme().TextMuted)
		p := a.phone
		if p == nil {
			return
		}
		desktopConnectionGroup(c).Padding(spaceXL).Gap(spaceL).AlignItems(ui.Center).Children(func() {
			if len(p.qr) > 0 {
				// A QR code reads on white in either window.
				ui.Box(c.Key("phone-qr")).Label("手机连接二维码").Size(240, 240).Background(ui.Hex("#ffffff")).Draw(func(painter *ui.Painter, r ui.Rect) { paintQR(painter, r, p.qr) })
				ui.Row(c).FillWidth().Justify(ui.Center).Gap(spaceL).Children(func() {
					if desktopConnectionIconAction(c, "复制连接码", "copy").Tooltip("复制连接码").Clicked() {
						c.WriteClipboard(p.link)
					}
					if desktopConnectionIconAction(c, "停止连接", "unplug").Tooltip("停止连接").Clicked() {
						a.revokePhonePair()
					}
				})
			} else if p.busy || p.revoking {
				ui.Spinner(c).Size(20, 20)
				ui.Text(c, "正在准备连接…").FontSize(textBody).TextColor(c.Theme().TextMuted)
				if p.busy && !p.revoking && desktopConnectionIconAction(c, "取消开启连接", "x").Clicked() {
					a.stopPhonePair()
				}
			} else {
				if p.message != "" {
					ui.Text(c, p.message).FontSize(textBody).TextColor(c.Theme().TextMuted)
				}
				if ui.PrimaryButton(c, "开启连接").Label("开启本机连接").Height(44).FillWidth().Clicked() {
					a.startPhonePair()
				}
			}
		})
		a.desktopIncomingRows(c)
	})
}

func (a *App) desktopIncomingRows(c *ui.Context) {
	var active = map[string]bool{}
	incoming := mergeIncomingDevices(nil, a.desktops.incoming)
	var bridge *remote.Bridge
	if a.phone != nil {
		bridge = a.phone.bridge
	}
	if bridge != nil {
		devices := mergeIncomingDevices(nil, bridge.Devices())
		incoming = mergeIncomingDevices(devices, incoming)
		for _, d := range devices {
			active[d.ID] = true
			// A retained older control may still be live after a newer peer
			// disconnects. Use that peer for status and disconnect actions.
			for i, saved := range incoming {
				if d.DeviceID != "" && d.DeviceID == saved.DeviceID {
					incoming[i] = d
				}
			}
		}
	}
	ui.Column(c).FillWidth().Children(func() {
		desktopConnectionSection(c, "最近接入此电脑", func() {
			ui.Text(c, fmt.Sprint(len(active))+" 在线").FontSize(textCaption).TextColor(colorsOf(c).textFaint)
		})
		desktopConnectionGroup(c).Children(func() {
			if len(incoming) == 0 {
				ui.Text(c, "手机或其他电脑连接后，会保留在这里").FontSize(textCaption).TextColor(colorsOf(c).textFaint).Padding(spaceL)
			}
			for i, d := range incoming {
				if i > 0 {
					desktopConnectionDivider(c)
				}
				ui.Row(c.Key("incoming-"+d.ID)).FillWidth().Height(60).Padding(0, spaceL).Gap(spaceM).AlignItems(ui.Center).Children(func() {
					glyph := "monitor"
					if strings.Contains(strings.ToLower(d.OS), "ios") || strings.Contains(strings.ToLower(d.OS), "android") {
						glyph = "smartphone"
					}
					connected := bridge != nil && bridge.DeviceConnected(d.ID)
					disconnected := bridge != nil && bridge.DeviceDisconnected(d.ID)
					iconColor := colorsOf(c).iconMuted
					if active[d.ID] || connected {
						iconColor = colorsOf(c).busy
					}
					mobileListIcon(c, glyph, iconColor)
					status, col := "离线", colorsOf(c).textFaint
					if active[d.ID] {
						status, col = "已连接", colorsOf(c).busy
					} else if disconnected {
						status = "已断开"
					} else if connected {
						status = "后台"
					}
					subtitle := d.OS
					if !active[d.ID] && !d.Connected.IsZero() {
						subtitle = "上次连接 " + d.Connected.Local().Format("01-02 15:04")
					}
					desktopConnectionText(c, mobileListTextOptions{Title: d.Name, Subtitle: subtitle})
					ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(5, 5).Radius(3).Background(col)
						ui.Text(c, status).FontSize(textCaption).TextColor(col)
					})
					if disconnected {
						if desktopConnectionIconAction(c, "允许设备 "+d.Name+" 重新连接", "plug-connected").Clicked() {
							bridge.AllowDevice(d.ID)
						}
					} else if connected {
						if desktopConnectionIconAction(c, "断开设备 "+d.Name, "unplug").Clicked() {
							go bridge.DisconnectDevice(d.ID)
						}
					}
				})
			}
		})
	})
	c.After(time.Second)
}

func remoteShortDir(dir, home string) string {
	if home != "" {
		if dir == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(dir, home+"/"); ok {
			return "~/" + rest
		}
	}
	return dir
}

func (a *App) desktopOfflineBar(c *ui.Context, k *colors, h *desktopHost) {
	ui.Row(c).FillWidth().Height(32).Padding(0, spaceM).Gap(spaceM).AlignItems(ui.Center).Background(k.panel).Children(func() {
		ui.Icon(c, icon("monitor")).Size(13, 13).TextColor(k.iconMuted)
		ui.Text(c, h.name()+" · 连接已断开，远端会话继续运行").FontSize(textCaption).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").TextColor(k.textMuted)
		if h.busy {
			ui.Spinner(c).Size(14, 14)
		} else if ui.Button(c, "重连").Label("重连远端电脑").Clicked() {
			if h.connected() {
				h.err = "会话连接已中断。"
			}
			a.connectDesktop(h)
		}
	})
}
