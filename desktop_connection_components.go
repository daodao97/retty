package main

import "github.com/egoist/mygo/ui"

func desktopConnectionIconAction(c *ui.Context, label, glyph string) ui.Element {
	k := colorsOf(c)
	b := ui.ButtonBase(c).Label(label).Role(ui.RoleButton).Size(28, 28).Radius(5).Tooltip(label)
	if b.Pressed() {
		b.Background(k.pressed)
	} else if b.Hovered() {
		b.Background(k.hover)
	}
	b.Children(func() { ui.Icon(c, icon(glyph)).Size(15, 15).TextColor(k.iconMuted) })
	return b
}

func desktopConnectionGroup(c *ui.Context) ui.Element {
	return mobileListGroup(c).Radius(8).Border(1, colorsOf(c).panelBorder)
}

func desktopConnectionRow(c *ui.Context, key, label string, height float32) ui.Element {
	row := mobileListRow(c, key, label, height).Gap(9)
	if row.Hovered() && !row.Pressed() {
		row.Background(colorsOf(c).hover)
	}
	return row
}

func desktopConnectionText(c *ui.Context, opts mobileListTextOptions) {
	opts.TitleSize, opts.SubtitleSize = 13, 11
	mobileListText(c, opts)
}

func desktopConnectionSection(c *ui.Context, title string, action func()) {
	ui.Row(c).FillWidth().Height(34).Padding(0, 2).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(colorsOf(c).textFaint).Grow(1)
		if action != nil {
			action()
		}
	})
}

func desktopConnectionDivider(c *ui.Context) {
	ui.Box(c).FillWidth().Height(1).Margin(0, 0, 0, 48).Background(colorsOf(c).hover)
}
