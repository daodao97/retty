package main

import (
	"context"
	"math"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/skip2/go-qrcode"
	"retty/internal/push"
	"retty/internal/remote"
	"retty/internal/rex"
)

type phonePair struct {
	busy          bool
	revoking      bool
	generation    int
	cancel        context.CancelFunc
	bridge        *remote.Bridge
	link, message string
	qr            [][]bool
}

func (a *App) openPhonePair() {
	a.showDesktopConnections(nil)
	a.desktops.showLocal = true
	a.startPhonePair()
}

func (a *App) restorePhonePair() {
	if remote.IdentityExists(rex.Dir()) {
		a.startPhonePair()
	}
}

func (a *App) startPhonePair() {
	if a.desktopUpdater == nil {
		a.desktopUpdater = newDesktopUpdateService(func() {
			mygo.RunOnMain(mygo.App.Relaunch)
		})
	}
	if a.phone == nil {
		a.phone = &phonePair{}
	}
	p := a.phone
	if p.bridge != nil || p.busy || p.revoking {
		return
	}
	p.busy, p.message = true, "正在开启连接…"
	p.generation++
	generation := p.generation
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	p.cancel = cancel
	go func() {
		// Notification registration shares the authenticated control tunnel and
		// runs off the forwarding goroutine. Repeated receipts are idempotent.
		registrations := make(chan push.Registration, 32)
		presence := make(chan rex.DesktopActivity, 32)
		stopPush := make(chan struct{})
		go func() {
			for {
				select {
				case <-stopPush:
					return
				case r := <-registrations:
					if r.Validate() == nil {
						push.Register(rex.Dir(), r)
					}
				case activity := <-presence:
					_ = push.ReportDesktop(rex.Dir(), activity)
				}
			}
		}()
		b, err := remote.Start(ctx, rex.SocketPath(), remote.Options{StateDir: rex.Dir(), OnDevice: func(info rex.DeviceInfo) {
			if info.Activity != nil {
				select {
				case presence <- *info.Activity:
				default:
				}
			}
			if info.Push != nil {
				select {
				case registrations <- push.Registration(*info.Push):
				default:
				}
			}
		}, OnNotice: func(activity rex.DesktopActivity, n push.Notice) (push.Route, error) {
			return claimDesktopNotice(rex.Dir(), activity, n)
		}, OnPeer: func(d remote.ConnectedDevice) {
			a.post(func() {
				if p.generation == generation && !a.quitting {
					a.desktops.incoming = mergeIncomingDevices([]remote.ConnectedDevice{d}, a.desktops.incoming)
					a.saveDesktopHistory()
				}
			})
		}, OnPasteImage: pasteRemoteImage, OnUpdate: a.desktopUpdater.request, Screen: a.screenHost()})
		if err != nil {
			close(stopPush)
		} else {
			b.OnClose(func() { close(stopPush) })
		}
		cancel()
		mygo.RunOnMain(func() {
			if p.generation != generation {
				if b != nil {
					go b.Close()
				}
				return
			}
			p.busy, p.cancel = false, nil
			if err != nil {
				p.message = "无法开启连接，请检查网络后重试"
			} else {
				p.bridge, p.link, p.message = b, b.Link(), ""
				code, err := qrcode.New(p.link, qrcode.Medium)
				if err == nil {
					p.qr = code.Bitmap()
				}
			}
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}

func (a *App) stopPhonePair() {
	p := a.phone
	if p == nil {
		return
	}
	p.generation++
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.bridge != nil {
		b := p.bridge
		p.bridge = nil
		go b.Close()
	}
	p.link, p.qr, p.message, p.busy = "", nil, "", false
}

// Explicit stop revokes saved credentials. App/window shutdown only closes the
// bridge, so the next launch can serve exactly the same capability.
func (a *App) revokePhonePair() {
	a.stopPhonePair()
	p := a.phone
	if p == nil || p.revoking {
		return
	}
	p.revoking = true
	dir := rex.Dir()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		err := remote.ForgetIdentity(ctx, dir)
		mygo.RunOnMain(func() {
			p.revoking = false
			if err != nil {
				p.message = "无法停止连接，请重试"
				a.showDesktopConnections(nil)
				a.desktops.showLocal = true
			}
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}

func paintQR(p *ui.Painter, r ui.Rect, code [][]bool) {
	if len(code) == 0 {
		return
	}
	p.Fill(r, ui.Hex("#ffffff"), 0)
	size := float32(math.Floor(float64(min(r.W, r.H)*p.Scale()/float32(len(code))))) / p.Scale()
	x, y := r.X+(r.W-size*float32(len(code)))/2, r.Y+(r.H-size*float32(len(code)))/2
	for row, cells := range code {
		for col, on := range cells {
			if on {
				p.Fill(ui.Rect{X: x + float32(col)*size, Y: y + float32(row)*size, W: size, H: size}, ui.Hex("#000000"), 0)
			}
		}
	}
}
