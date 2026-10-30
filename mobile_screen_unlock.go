package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
)

// screenUnlockRemote asks the Mac's bridge to type a password at its lock
// screen; tests replace it.
var screenUnlockRemote = remote.UnlockScreen

// screenUnlock is the lock screen's password form. The password is kept
// only in this phone's Keychain, one entry per Mac, and read only when it
// is sent; the Mac uses it for the one sign-in and keeps nothing.
type screenUnlock struct {
	// key is the Keychain entry of the Mac the form is for.
	key string
	// saved tells that the entry holds a password, as far as it is known.
	saved    bool
	password string
	remember bool
	// reveal shows the typed password, to check it.
	reveal bool
	// editing shows the field although a password is saved, to change it.
	editing bool
	busy    bool
	error   string
}

// screenUnlockKey names a Mac's Keychain entry by a hash of its identity,
// which may be the pairing link, a capability that must not be a name.
func screenUnlockKey(desktop string) string {
	sum := sha256.Sum256([]byte(desktop))
	return "screen-unlock-" + hex.EncodeToString(sum[:16])
}

// prepareScreenUnlock readies the form for the connected Mac when its lock
// screen shows, and learns whether a password is saved for it.
func (m *mobileApp) prepareScreenUnlock() {
	u := &m.screen.unlock
	key := screenUnlockKey(m.desktopKey())
	if u.key == key {
		return
	}
	*u = screenUnlock{key: key, remember: true}
	if m.store == nil || m.storage == nil {
		return
	}
	m.storage <- func() {
		_, err := m.store.Get(key)
		saved := err == nil
		mygo.RunOnMain(func() {
			if m.screen.unlock.key == key {
				m.screen.unlock.saved = saved
				m.invalidate()
			}
		})
	}
}

// startScreenUnlock sends the typed password, or else the saved one.
func (m *mobileApp) startScreenUnlock() {
	u := &m.screen.unlock
	if u.busy {
		return
	}
	typed := u.password
	if typed == "" && (!u.saved || u.editing) {
		u.error = "请输入电脑的登录密码"
		return
	}
	u.busy, u.error, u.password = true, "", ""
	key, remember, epoch, client := u.key, u.remember, m.screen.epoch, m.client
	send := func(password string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := screenUnlockRemote(ctx, client, password)
			mygo.RunOnMain(func() { m.finishScreenUnlock(epoch, key, typed, remember, err) })
		}()
	}
	if typed != "" {
		send(typed)
		return
	}
	if m.store == nil || m.storage == nil {
		u.busy, u.error = false, "无法读取保存的密码"
		return
	}
	m.storage <- func() {
		saved, err := m.store.Get(key)
		if err != nil {
			mygo.RunOnMain(func() {
				if m.screen.unlock.key == key {
					m.screen.unlock.busy, m.screen.unlock.saved = false, false
					m.screen.unlock.error = "无法读取保存的密码，请重新输入"
					m.invalidate()
				}
			})
			return
		}
		send(string(saved))
	}
}

func (m *mobileApp) finishScreenUnlock(epoch uint64, key, typed string, remember bool, err error) {
	u := &m.screen.unlock
	if m.screen.epoch != epoch || u.key != key {
		return
	}
	u.busy = false
	defer m.invalidate()
	if err != nil {
		u.error = err.Error()
		// A saved password that failed is offered for changing.
		if typed == "" {
			u.editing = true
		}
		return
	}
	u.editing = false
	switch {
	case typed == "":
	case remember:
		u.saved = true
		m.save(key, []byte(typed), func() { m.screen.unlock.error = "已解锁，但无法保存密码"; m.invalidate() })
	case u.saved && m.store != nil && m.storage != nil:
		u.saved = false
		m.storage <- func() { m.store.Delete(key) }
	}
}

// screenUnlockCard floats over a locked Mac's picture, or where it would
// be, and unlocks it with the user's login password.
func (m *mobileApp) screenUnlockCard(c *ui.Context, k *colors) {
	u := &m.screen.unlock
	ui.Column(c.Key("screen-locked")).Label("电脑已锁屏").Absolute().Left(spaceL).Right(spaceL).Top(2*spaceS+screenCloseSize).Padding(spaceL).Gap(spaceM).
		Radius(radiusL).Background(k.panel).Border(1, k.panelBorder).Children(func() {
		ui.Text(c, "电脑已锁屏").FontSize(mobileSubhead).TextColor(k.text)
		if u.saved && !u.editing {
			ui.Text(c, "使用保存在此手机上的密码解锁。").FontSize(textSmall).TextColor(k.textMuted).FillWidth().Wrap()
		} else {
			// The keyboard types the password as it is: iOS would otherwise
			// capitalize its first letter, correct it, or compose it in an input
			// method, all unseen behind the bullets.
			field := ui.TextInput(c.Key("screen-unlock-password"), &u.password).Label("电脑登录密码").Placeholder("电脑登录密码").
				FillWidth().Height(40).FontSize(textBody).
				InputOptions(ui.InputOptions{Keyboard: ui.KeyboardASCII, Content: ui.ContentPassword, Correction: ui.CorrectionOff, Capitalization: ui.CapitalizeNone})
			if !u.reveal {
				field.Password()
			}
			ui.Row(c).FillWidth().Gap(spaceXS).Children(func() {
				if screenToggle(c, "记住密码", "在此手机上记住密码", u.remember) {
					u.remember = !u.remember
				}
				if screenToggle(c, "显示密码", "显示输入的密码", u.reveal) {
					u.reveal = !u.reveal
				}
			})
		}
		ui.Row(c).FillWidth().Gap(spaceS).Children(func() {
			label := "解锁"
			if u.busy {
				label = "正在解锁…"
			}
			unlock := ui.ButtonBase(c.Key("screen-unlock")).Label("解锁").Role(ui.RoleButton).Height(40).Grow(1).Basis(0).MinWidth(0).
				Radius(radiusM).Center().Background(k.accent)
			unlock.Children(func() { ui.Text(c, label).FontSize(textBody).TextColor(k.onAccent) })
			if unlock.Clicked() {
				m.startScreenUnlock()
			}
			if u.saved && !u.editing && ui.Button(c, "更换密码").Label("更换保存的密码").Height(40).Clicked() {
				u.editing, u.error = true, ""
			}
		})
		if u.error != "" {
			ui.Text(c, u.error).FontSize(textSmall).TextColor(k.danger).FillWidth().Wrap()
		}
		ui.Text(c, "密码只保存在此手机的钥匙串中，通过电脑的系统屏幕共享输入，电脑不保存。").FontSize(textSmall).TextColor(k.textFaint).FillWidth().Wrap()
	})
}
