package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/vncunlock"
)

// screenAllowed mirrors prefs.ScreenSharing for the bridge, which asks off
// the UI thread.
var screenAllowed atomic.Bool

// consoleLocked asks the system whether the console is locked.
func consoleLocked() bool {
	out, err := exec.Command("ioreg", "-n", "Root", "-d1").Output()
	return err == nil && strings.Contains(string(out), `"IOConsoleLocked" = Yes`)
}

// unlockScreen types password at the lock screen through the Mac's own
// Screen Sharing, as the user it runs as. It types only at a locked screen:
// otherwise the password would land in whatever window has focus.
func unlockScreen(ctx context.Context, password string) error {
	if !consoleLocked() {
		return errors.New("电脑没有锁屏")
	}
	account, err := user.Current()
	if err != nil {
		return errors.New("无法读取当前账户")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:5900")
	if err != nil {
		return errors.New("此电脑未开启系统「屏幕共享」，请在系统设置 → 通用 → 共享中开启")
	}
	switch err = vncunlock.Unlock(ctx, conn, account.Username, password, vncunlock.DefaultPace); {
	case errors.Is(err, vncunlock.ErrDenied):
		return errors.New("密码不正确，或此账户不允许屏幕共享登录")
	case errors.Is(err, vncunlock.ErrUnsupported):
		return errors.New("系统屏幕共享未提供账户登录")
	case err != nil:
		return errors.New("解锁失败，请重试")
	}
	for range 6 {
		if !consoleLocked() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("已输入密码，但电脑仍处于锁屏，请确认密码后重试")
}

// screenAccess is what macOS lets the screen helper do.
type screenAccess struct {
	Screen bool `json:"screen"`
	Input  bool `json:"input"`
}

// screenHelperPath finds retty-screen among the app's resources: at their
// top in a built app, in resources/darwin under go run. It is looked up
// once, as settings ask on every frame.
var screenHelperPath = sync.OnceValue(func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return ""
	}
	for _, p := range []string{filepath.Join(dir, "retty-screen"), filepath.Join(dir, "darwin", "retty-screen")} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
})

// screenHost is the bridge's view of screen sharing, or nil where the
// helper is missing.
func (a *App) screenHost() *remote.ScreenHost {
	helper := screenHelperPath()
	if helper == "" {
		return nil
	}
	screenAllowed.Store(prefs.ScreenSharing)
	update := a.desktopDispatcher()
	return &remote.ScreenHost{
		Helper:    helper,
		Allowed:   screenAllowed.Load,
		OnViewers: func(n int) { update(func() { a.screenViewers = n }) },
		Locked:    consoleLocked,
		Unlock:    unlockScreen,
	}
}

func (a *App) setScreenSharing(on bool) {
	prefs.ScreenSharing = on
	screenAllowed.Store(on)
	saveSettings()
	if !on {
		a.stopScreenSharing()
		return
	}
	// Turning it on asks macOS for both permissions, so the user grants
	// them now, at the desk, rather than finding them missing from a phone.
	a.refreshScreenAccess(true)
}

func (a *App) stopScreenSharing() {
	if a.phone != nil && a.phone.bridge != nil {
		go a.phone.bridge.StopScreen()
	}
}

// refreshScreenAccess reads the helper's permissions off the UI thread;
// request asks macOS for the missing ones.
func (a *App) refreshScreenAccess(request bool) {
	helper := screenHelperPath()
	if helper == "" || a.screenBusy {
		return
	}
	a.screenBusy = true
	update := a.desktopDispatcher()
	go func() {
		command := "check"
		if request {
			command = "request"
		}
		var access *screenAccess
		if out, err := exec.Command(helper, command).Output(); err == nil {
			access = &screenAccess{}
			if json.Unmarshal(out, access) != nil {
				access = nil
			}
		}
		update(func() { a.screenAccess, a.screenBusy = access, false })
	}()
}

// screenAccessLabel tells what the screen helper may do, for the settings.
func (a *App) screenAccessLabel() string {
	switch access := a.screenAccess; {
	case screenHelperPath() == "":
		return "此版本未包含远程桌面组件。"
	case access == nil:
		return "正在检查权限…"
	case access.Screen && access.Input:
		return "已允许屏幕录制和辅助功能，手机可以查看并操作。"
	case access.Screen:
		return "已允许屏幕录制；缺少辅助功能权限，手机只能查看，不能操作。"
	default:
		return "缺少屏幕录制权限。请在系统设置 → 隐私与安全性中允许 Retty，然后重新打开 Retty。"
	}
}

// screenSharingBar floats over the window while a device watches the
// screen, so it is never seen without the user knowing, and ends it in one
// click.
func (a *App) screenSharingBar(c *ui.Context, k *colors) {
	ui.Overlay(c, func() {
		ui.Row(c).Absolute().Bottom(18).Left(0).Right(0).Justify(ui.Center).PassThrough().Children(func() {
			ui.Row(c.Key("screen-sharing-bar")).Label("远程桌面提示").Gap(spaceM).Padding(spaceS, spaceS, spaceS, spaceL).Radius(radiusL).
				Background(k.panel).Border(1, k.panelBorder).Shadow(0, 8, 24, 0, k.shadowFocused).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 8).Radius(4).Background(k.danger)
				ui.Text(c, "其他设备正在查看并可操作此电脑的屏幕").FontSize(textSmall).TextColor(k.text)
				if boardButton(c, k, "screen-sharing-stop", "断开", buttonDanger).Clicked() {
					a.stopScreenSharing()
				}
			})
		})
	})
}
