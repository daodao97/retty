package main

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/selfupdate"
)

var cmdUpdateRemote = command{ID: "host.update", Title: "更新远端 Retty…", Run: func(a *App) { a.openRemoteUpdate() }}

type remoteUpdateDialog struct {
	open, busy, tracking bool
	host                 *desktopHost
	status               selfupdate.Status
	err                  string
	started              time.Time
	lastPoll             time.Time
}

func (a *App) openRemoteUpdate() {
	t := a.tab()
	if t == nil || t.Host == nil || !t.Host.connected() {
		a.err = "请先打开已连接的远端会话"
		return
	}
	d := &remoteUpdateDialog{open: true, host: t.Host}
	a.remoteUpdate = d
	a.requestRemoteUpdate(d, selfupdate.Request{Action: "check"})
}

func (a *App) requestRemoteUpdate(d *remoteUpdateDialog, req selfupdate.Request) {
	if d.busy {
		return
	}
	if !d.host.connected() {
		if req.Action == "apply" {
			d.tracking = false
			d.err = "远端已断开，请重新连接后重试。"
		}
		return
	}
	d.busy = true
	client := d.host.client
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		status, err := remote.Update(ctx, client, req)
		a.post(func() {
			if a.remoteUpdate != d || !d.open {
				return
			}
			d.busy = false
			if err != nil {
				var networkError net.Error
				if d.tracking && (errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)) {
					return
				} // reconnection polls the replacement gateway
				d.tracking = false
				d.err = err.Error()
				return
			}
			d.status = status
			d.err = ""
			if req.Action == "check" && status.Busy() {
				d.tracking = true
				d.started = time.Now()
			}
			if req.Action == "apply" {
				d.tracking = true
				d.started = time.Now()
			}
			if status.Phase == "updated" || status.Phase == "failed" {
				d.tracking = false
			}
		})
	}()
}

func (a *App) remoteUpdateView(c *ui.Context, k *colors) {
	d := a.remoteUpdate
	if d == nil || !d.open {
		return
	}
	ui.DialogBase(c.Key("remote-update"), &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop)
		width, _ := c.Size()
		panel.Column().Width(440).MaxWidth(width - 32).Padding(20).Gap(14).Radius(12).Background(k.panel).Children(func() {
			ui.Text(c, "更新远端 Retty").FontSize(16).FontWeight(600)
			ui.Text(c, d.host.hello.Host.Name).FontSize(13).TextColor(k.textMuted)
			message := "正在检查版本…"
			if d.status.Current != "" {
				message = "当前 " + d.status.Current
				if d.status.Latest != "" {
					message += " · 最新 " + d.status.Latest
				}
			}
			ui.Text(c, message).FontSize(13)
			if d.err != "" {
				ui.Text(c, d.err).FontSize(13)
			} else if d.tracking {
				phase := "正在更新…"
				switch d.status.Phase {
				case "downloading":
					phase = "正在下载并校验…"
				case "installing":
					phase = "正在验证并安装…"
				case "restarting":
					phase = "正在恢复连接…"
				}
				if !d.host.connected() {
					phase = "正在等待远端重新连接…"
				}
				ui.Text(c, phase).FontSize(13)
			} else if d.status.Phase == "updated" {
				ui.Text(c, "更新完成，现有终端任务继续运行。").FontSize(13)
			} else if d.status.Phase == "failed" {
				ui.Text(c, "更新失败："+d.status.Error).FontSize(13)
			} else if !d.busy && !d.status.Available && d.status.Current != "" {
				ui.Text(c, "当前已是最新正式版本。").FontSize(13)
			}
			hint := "更新连接网关时会短暂断线。会话服务和正在运行的任务保持不变。"
			if d.status.Kind == "desktop" {
				hint = "更新后会重开远端 Retty 窗口。后台会话服务和正在运行的任务保持不变。"
			}
			ui.Text(c, hint).FontSize(12).TextColor(k.textMuted)
			ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
				if ui.Button(c, "关闭").Clicked() {
					d.open = false
				}
				if d.status.Available && !d.busy && !d.tracking && d.err == "" && d.status.Phase != "failed" {
					if ui.Button(c, "更新到 "+d.status.Latest).Clicked() {
						d.tracking = true
						d.started = time.Now()
						a.requestRemoteUpdate(d, selfupdate.Request{Action: "apply", Version: d.status.Latest})
					}
				}
			})
		})
	})
	if d.tracking {
		if time.Since(d.started) > 12*time.Minute {
			d.tracking = false
			d.err = "等待更新结果超时，请重新连接后检查远端版本。"
		} else if time.Since(d.lastPoll) >= time.Second && !d.busy {
			d.lastPoll = time.Now()
			a.requestRemoteUpdate(d, selfupdate.Request{Action: "status"})
		}
		c.After(time.Second)
	}
}
