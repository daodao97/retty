package main

import (
	"context"
	"errors"
	"net/url"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/browsercontrol"
	"retty/internal/remote"
)

type mobilePreview struct {
	open, busy     bool
	address, error string
	cancel         context.CancelFunc
	epoch          uint64
}

func mobilePreviewURL(raw string) (*url.URL, int, error) {
	normalized, err := browsercontrol.NormalizeURL(raw)
	if err != nil {
		return nil, 0, errors.New("请输入有效的开发地址，例如 http://localhost:3000")
	}
	port, ok := browsercontrol.LoopbackPort(normalized)
	if !ok {
		return nil, 0, errors.New("请输入主机的 localhost 或 127.0.0.1 开发地址")
	}
	u, err := url.Parse(normalized)
	return u, port, err
}

func (m *mobileApp) openPreview() {
	m.preview.open = true
	if m.preview.address == "" {
		m.preview.address = "http://localhost:3000"
	}
	m.preview.error = ""
	m.invalidate()
}
func (m *mobileApp) closePreview() {
	m.preview.epoch++
	if m.preview.cancel != nil {
		m.preview.cancel()
		m.preview.cancel = nil
	}
	m.preview.busy = false
}
func (m *mobileApp) startPreview() {
	u, port, err := mobilePreviewURL(m.preview.address)
	if err != nil {
		m.preview.error = err.Error()
		return
	}
	if !m.connectionUsable() {
		m.preview.error = "请先连接主机"
		return
	}
	m.closePreview()
	ctx, cancel := context.WithCancel(context.Background())
	m.preview.cancel, m.preview.busy, m.preview.error = cancel, true, ""
	if m.term != nil {
		m.term.SetInputEnabled(false)
	}
	epoch, client := m.preview.epoch, m.client
	go func() {
		defer cancel()
		forward, err := remote.ForwardPort(ctx, client, port)
		if err == nil {
			defer forward.Close()
			u.Host = forward.Address()
			err = mygo.OpenInAppBrowser(ctx, u.String())
		}
		mygo.RunOnMain(func() {
			if epoch != m.preview.epoch {
				return
			}
			m.preview.busy, m.preview.cancel = false, nil
			if err != nil && ctx.Err() == nil {
				m.preview.error = "无法打开预览，请确认主机开发服务已启动且地址正确"
				if errors.Is(err, mygo.ErrUnsupported) {
					m.preview.error = "当前系统不支持应用内预览"
				}
			} else {
				m.preview.open = false
			}
			m.invalidate()
		})
	}()
}

func (m *mobileApp) previewDialog(c *ui.Context) {
	ui.DialogBase(c, &m.preview.open, func(back, panel ui.Element) {
		back.Background(colorsOf(c).sheetScrim).Column().Justify(ui.End).AlignItems(ui.Center).Padding(spaceL)
		panel.Label("开发页面预览").FillWidth().MaxWidth(480).MaxHeight(c.Root().Bounds().H - 24).Padding(spaceXL).Radius(radiusSheet).Background(c.Theme().Surface).Column().Gap(spaceXL).Clip()
		ui.Text(c, "开发页面预览").FontSize(mobileTitle).Bold().Shrink(0)
		ui.Scroll(c.Key("mobile-preview-address")).FillWidth().MinHeight(0).Shrink(1).Gap(spaceL).Children(func() {
			ui.Text(c, "输入这台主机上的开发地址，通过现有加密连接在手机预览。").FontSize(textBody).LineHeight(1.5).TextColor(c.Theme().TextMuted).FillWidth()
			ui.TextInputBase(c, &m.preview.address).Label("开发页面地址").Placeholder("http://localhost:3000").FillWidth().Height(44).FontSize(mobileBody).InputOptions(ui.InputOptions{Keyboard: ui.KeyboardASCII, Correction: ui.CorrectionOff, Capitalization: ui.CapitalizeNone})
			if m.preview.error != "" {
				ui.Text(c, m.preview.error).FontSize(textBody).TextColor(c.Theme().Danger).FillWidth()
			}
		})
		ui.Row(c).FillWidth().Shrink(0).Gap(spaceL).Children(func() {
			if ui.Button(c, "关闭").Grow(1).Height(44).Clicked() {
				m.closePreview()
				m.preview.open = false
				c.Blur()
			}
			label := "打开预览"
			if m.preview.busy {
				label = "正在预览…"
			}
			if ui.PrimaryButton(c, label).Grow(1).Height(44).Disabled(m.preview.busy).Clicked() {
				c.Blur()
				m.startPreview()
			}
		})
	})
	if !m.preview.open && m.preview.busy {
		m.closePreview()
	}
}
