package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

func connectionStatusCard(c *ui.Context, title, body string, animating bool, actions func()) {
	mobileCard(c.Key("connection-feedback")).Label("连接状态").Padding(spaceL).Gap(spaceXS).Children(func() {
		ui.Row(c).FillWidth().Gap(spaceM).Children(func() {
			if animating {
				mobileReconnectIcon(c, 14, c.Theme().Accent)
			}
			ui.Text(c, title).FontSize(mobileSubhead).Bold().Grow(1)
		})
		ui.Text(c, body).FontSize(textBody).LineHeight(1.4).TextColor(c.Theme().TextMuted)
		if actions != nil {
			actions()
		}
	})
}

// Keep automatic recovery quiet; the full set of actions lives in the details.
func connectionStatusSummary(c *ui.Context, title, body string, animating bool) ui.Element {
	var action ui.Element
	mobileCard(c.Key("connection-feedback")).Label("连接状态").Children(func() {
		action = ui.ButtonBase(c).Label("连接恢复操作").Value(title+" · "+body).Role(ui.RoleButton).
			FillWidth().Height(64).Padding(0, spaceL).Gap(spaceM)
		if action.Pressed() {
			action.Background(c.Theme().SurfacePressed)
		}
		action.Children(func() {
			ui.Box(c).Size(32, 32).Shrink(0).Radius(radiusL).Center().Background(c.Theme().Accent.Mix(c.Theme().Surface, .92)).Children(func() {
				if animating {
					mobileReconnectIcon(c, 16, c.Theme().Accent)
				} else {
					ui.Icon(c, icon("unplug")).Size(16, 16).TextColor(colorsOf(c).attention)
				}
			})
			mobileListText(c, mobileListTextOptions{Title: title, Subtitle: body, TitleSize: 14, SubtitleSize: 12})
			ui.Icon(c, icon("ellipsis")).Size(17, 17).Shrink(0).TextColor(c.Theme().TextMuted)
		})
	})
	return action
}

func mobileReconnectIcon(c *ui.Context, size float32, color ui.Color) {
	e := ui.Icon(c, icon("rotate-ccw")).Label("重连进度").Role(ui.RoleProgress).Size(size, size).Shrink(0).TextColor(color).PassThrough()
	e.Rotate(-360 * e.Loop("reconnect", 1100*time.Millisecond, ui.Linear))
}

func mobileCard(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().Radius(radiusL).Background(c.Theme().Surface)
}

func mobileListGroup(c *ui.Context) ui.Element {
	return mobileCard(c).Clip()
}

func mobileListSectionHeader(c *ui.Context, title string, action func()) {
	ui.Row(c).FillWidth().Height(44).Padding(0, spaceXS).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(textBody).TextColor(c.Theme().TextMuted).Grow(1)
		if action != nil {
			action()
		}
	})
}

func mobileIconAction(c *ui.Context, label, glyph string) ui.Element {
	return ui.ButtonBase(c).Label(label).Role(ui.RoleButton).Size(44, 44).Children(func() {
		ui.Icon(c, icon(glyph)).Size(17, 17).TextColor(c.Theme().Accent)
	})
}

func mobileTextAction(c *ui.Context, label, text string) ui.Element {
	return ui.ButtonBase(c).Label(label).Role(ui.RoleButton).MinWidth(44).Height(44).Padding(0, spaceM).Children(func() {
		ui.Text(c, text).FontSize(mobileSubhead).TextColor(c.Theme().Accent)
	})
}

func mobileListRow(c *ui.Context, key, label string, height float32) ui.Element {
	row := ui.ButtonBase(c.Key(key)).Label(label).Role(ui.RoleButton).FillWidth().Height(height).Padding(0, spaceL).Gap(spaceL)
	if row.Pressed() {
		row.Background(c.Theme().SurfacePressed)
	}
	return row
}

type mobileListTextOptions struct {
	Title, Subtitle                   string
	TitleSize, SubtitleSize           float32
	TitleAccessory, SubtitleAccessory func()
}

func mobileListText(c *ui.Context, opts mobileListTextOptions) {
	if opts.TitleSize == 0 {
		opts.TitleSize = 15
	}
	if opts.SubtitleSize == 0 {
		opts.SubtitleSize = 12
	}
	ui.Column(c).Grow(1).MinWidth(0).Gap(spaceXS).Children(func() {
		ui.Row(c).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, opts.Title).FontSize(opts.TitleSize).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if opts.TitleAccessory != nil {
				opts.TitleAccessory()
			}
		})
		if opts.Subtitle != "" || opts.SubtitleAccessory != nil {
			ui.Row(c).FillWidth().Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, opts.Subtitle).FontSize(opts.SubtitleSize).TextColor(c.Theme().TextMuted).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
				if opts.SubtitleAccessory != nil {
					opts.SubtitleAccessory()
				}
			})
		}
	})
}

func mobileListIcon(c *ui.Context, name string, color ui.Color) ui.Element {
	return ui.Box(c).Size(32, 32).Shrink(0).Center().Children(func() {
		programIcon(c, name).Size(21, 21).TextColor(color)
	})
}

func mobileListChevron(c *ui.Context) {
	ui.Icon(c, icon("chevron-right")).Size(16, 16).TextColor(colorsOf(c).iconMuted)
}

func mobileListDivider(c *ui.Context) {
	ui.Box(c).FillWidth().Height(1).Margin(0, 0, 0, 56).Background(colorsOf(c).hover)
}

func mobileListEmpty(c *ui.Context, message string) {
	ui.Text(c, message).FontSize(mobileSubhead).TextColor(c.Theme().TextMuted).Padding(spaceXL)
}
