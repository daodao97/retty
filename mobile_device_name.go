package main

import "github.com/egoist/mygo/ui"

func (m *mobileApp) connectedDevice() desktopRecent {
	if device, ok := m.recentDesktop(m.desktopKey()); ok {
		if m.hello.Host.Name != "" {
			device.Name = m.hello.Host.Name
		}
		return device
	}
	return desktopRecent{ID: m.hello.Host.ID, Link: m.link, Name: m.hello.Host.Name, OS: desktopPlatform(m.hello.Host)}
}

func (m *mobileApp) setDeviceName(name string) {
	name = cleanName(name)
	device := m.connectedDevice()
	if device.key() == "" {
		return
	}
	device.Alias = name
	if m.deviceNameTouched == nil {
		m.deviceNameTouched = map[string]bool{}
	}
	for _, key := range []string{device.ID, device.Link} {
		if key != "" {
			m.deviceNameTouched[key] = true
		}
	}
	for i, entry := range m.history {
		if entry.Link == device.Link || sameDesktop(entry, device) {
			m.history[i].Alias = name
		}
	}
	m.history = mergeDesktopHistory([]desktopRecent{device}, m.history)
	m.persistDesktopHistory()
	m.invalidate()
}

func (m *mobileApp) deviceNameDialog(c *ui.Context) {
	if !m.deviceNameOpen {
		return
	}
	dialog := ui.DialogBase(c.Key("mobile-device-name"), &m.deviceNameOpen, func(back, panel ui.Element) {
		back.Background(colorsOf(c).sheetScrim).Column().Justify(ui.End).AlignItems(ui.Center).Padding(spaceL)
		panel.Label("设备名称面板").FillWidth().MaxWidth(480).Padding(spaceXL).Radius(radiusSheet).Background(c.Theme().Surface).Column().Gap(spaceL)
		ui.Row(c).FillWidth().Height(44).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "设备名称").FontSize(mobileHeadline).Bold().Grow(1)
			if ui.ButtonBase(c).Label("取消设备重命名").Role(ui.RoleButton).Size(44, 44).Radius(22).Background(c.Theme().Background).Children(func() {
				ui.Icon(c, icon("x")).Size(16, 16).TextColor(c.Theme().TextMuted)
			}).Clicked() {
				m.deviceNameOpen = false
			}
		})
		ui.Column(c).FillWidth().Padding(spaceM, spaceL).Radius(radiusL).Background(c.Theme().Background).Children(func() {
			ui.Text(c, "留空使用设备原名").FontSize(textSmall).TextColor(c.Theme().TextMuted)
			ui.TextInputBase(c, &m.deviceNameDraft).Label("连接设备名称").Placeholder(m.hello.Host.Name).FontSize(mobileCallout).FillWidth().Height(40).
				InputOptions(ui.InputOptions{Return: ui.ReturnDone, Correction: ui.CorrectionOff, Capitalization: ui.CapitalizeNone})
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(spaceL).Children(func() {
			ui.Text(c, "仅在这台手机上显示").FontSize(textSmall).TextColor(c.Theme().TextMuted).Grow(1)
			if ui.ButtonBase(c).Label("保存设备名称").Role(ui.RoleButton).Size(72, 44).Radius(radiusL).Background(c.Theme().Accent).Children(func() {
				ui.Icon(c, icon("check")).Size(19, 19).TextColor(c.Theme().AccentText)
			}).Clicked() {
				m.setDeviceName(m.deviceNameDraft)
				m.deviceNameOpen = false
			}
		})
	})
	if dialog.Dismissed() || !m.deviceNameOpen {
		m.sessionSettingsOpen = true
		c.Blur()
	}
}
