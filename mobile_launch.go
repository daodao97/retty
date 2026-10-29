package main

import (
	_ "embed"
	"sync"

	"github.com/egoist/mygo/ui"
)

// The application owns its startup view, just like any other MyGo UI view.
// The system's pre-Go placeholder is managed by the framework.
//
//go:embed resources/icon.png
var mobileLaunchLogoPNG []byte

var mobileLaunchLogo = sync.OnceValue(func() *ui.Bitmap {
	logo, err := ui.DecodeBitmap(mobileLaunchLogoPNG)
	if err != nil {
		panic("invalid embedded Retty logo: " + err.Error())
	}
	return logo
})

func mobileLaunchView(c *ui.Context) {
	ui.Column(c).Fill().Center().Label("Retty 启动画面").Children(func() {
		ui.Column(c).AlignItems(ui.Center).Gap(spaceXL).Children(func() {
			ui.Image(c, mobileLaunchLogo()).Size(120, 120).Fit(ui.Contain).
				Role(ui.RoleImage).Label("Retty Logo")
			ui.Text(c, "Retty").FontSize(mobileLargeTitle).Bold().TextColor(c.Theme().Text)
		})
	})
}

// Local readiness belongs to Retty; MyGo owns presentation and transition.
func (m *mobileApp) startupLoaded() {
	if m.startup != nil {
		m.startup.Ready = true
		m.invalidate()
	}
}
