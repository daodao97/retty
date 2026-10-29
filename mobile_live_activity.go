package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/rex"
)

type mobileLiveActivity struct {
	enabled, touched, tools, busy, registering                     bool
	id, desktop, session, token, activity, last, registered, error string
	epoch                                                          uint64
}
type mobileLivePreference struct {
	Enabled              bool
	ID, Desktop, Session string
}

func (m *mobileApp) setupLiveActivity() {
	mygo.OnLiveActivityToken(func(info mygo.LiveActivityInfo) {
		if info.ID != m.live.id {
			return
		}
		m.live.token, m.live.activity = info.PushToken, info.Activity
		m.live.registered = ""
		if info.PushToken == "" {
			m.unregisterLiveActivity()
			m.live.id = ""
			m.saveLivePreference()
		} else {
			m.registerLiveActivity()
		}
	})
	if m.store == nil || m.storage == nil {
		return
	}
	m.storage <- func() {
		var p mobileLivePreference
		if data, err := m.store.Get("live-activity-v1"); err == nil {
			json.Unmarshal(data, &p)
		}
		mygo.RunOnMain(func() {
			if !m.live.touched {
				m.live.enabled = p.Enabled
				m.live.id = p.ID
				m.live.desktop = p.Desktop
				m.live.session = p.Session
				m.invalidate()
			}
		})
	}
}
func (m *mobileApp) saveLivePreference() {
	data, _ := json.Marshal(mobileLivePreference{m.live.enabled, m.live.id, m.live.desktop, m.live.session})
	m.save("live-activity-v1", data, func() { m.live.error = "无法保存锁屏状态设置"; m.invalidate() })
}
func (m *mobileApp) setLiveActivityEnabled(enabled bool) {
	m.live.touched = true
	m.live.enabled = enabled
	m.live.error = ""
	if !enabled {
		m.stopLiveActivity()
	} else if m.term != nil {
		m.trackLiveActivity(m.selected)
	}
	m.saveLivePreference()
	m.invalidate()
}
func (m *mobileApp) trackLiveActivity(s rex.SessionInfo) {
	if !m.connectionUsable() {
		m.live.error = "请先连接主机"
		return
	}
	if s.ID == "" || s.Exited || s.Agent.State == "" {
		m.live.error = "此会话还没有可显示的 Agent 状态"
		return
	}
	sum := sha256.Sum256([]byte(m.hello.Host.ID + ":" + s.ID))
	id := m.live.id
	if id == "" || m.live.desktop != m.hello.Host.ID || m.live.session != s.ID {
		m.stopLiveActivity()
		id = fmt.Sprintf("retty-%s-%x", hex.EncodeToString(sum[:16]), time.Now().UnixNano())
	}
	m.live.id = id
	m.live.desktop = m.hello.Host.ID
	m.live.session = s.ID
	m.live.enabled, m.live.touched = true, true
	m.live.error = ""
	m.live.last, m.live.registered = "", ""
	m.saveLivePreference()
	m.syncLiveActivity()
}
func (m *mobileApp) stopLiveActivity() {
	m.unregisterLiveActivity()
	m.live.epoch++
	id := m.live.id
	m.live.id, m.live.last, m.live.token, m.live.activity, m.live.registered = "", "", "", "", ""
	m.live.busy, m.live.registering = false, false
	if id != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			mygo.EndLiveActivity(ctx, id)
		}()
	}
}
func (m *mobileApp) unregisterLiveActivity() {
	if m.client == nil || m.live.activity == "" || m.pushDeviceID == "" {
		return
	}
	client := m.client
	req := remote.ActivityRegistration{DeviceID: m.pushDeviceID, ActivityID: m.live.activity, SessionID: m.live.session, Disabled: true}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		remote.RegisterActivity(ctx, client, req)
	}()
}
func (m *mobileApp) syncLiveActivity() {
	if !m.live.enabled || m.live.id == "" || m.live.busy || m.background || !m.screenActive || !m.connectionUsable() || m.hello.Host.ID != m.live.desktop {
		return
	}
	var session rex.SessionInfo
	for _, s := range m.sessions {
		if s.ID == m.live.session {
			session = s
			break
		}
	}
	if session.ID == "" {
		return
	}
	state := remote.ActivityContent(m.hello.Host, session)
	stable, _ := json.Marshal(state)
	if m.live.last == string(stable) {
		m.registerLiveActivity()
		return
	}
	m.live.busy = true
	id, epoch := m.live.id, m.live.epoch
	state.UpdatedAt = time.Now().Unix()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		info, err := mygo.UpdateLiveActivity(ctx, id, mygo.LiveActivityState(state))
		mygo.RunOnMain(func() {
			if epoch != m.live.epoch {
				if err == nil && id != m.live.id {
					go mygo.EndLiveActivity(context.Background(), id)
				}
				return
			}
			m.live.busy = false
			// Remember a failed revision too; retry explicitly instead of every frame.
			m.live.last = string(stable)
			if err != nil {
				m.live.error = "无法开启锁屏实时状态，请检查系统权限后重试"
				if errors.Is(err, mygo.ErrUnsupported) {
					m.live.error = "锁屏实时状态需要 iOS 16.2 或更新系统"
				}
			} else {
				if info.PushToken != "" {
					m.live.token = info.PushToken
				}
				m.live.activity = info.Activity
				m.registerLiveActivity()
			}
			m.invalidate()
		})
	}()
}
func (m *mobileApp) registerLiveActivity() {
	if !m.live.enabled || m.live.token == "" || m.live.activity == "" || m.pushDeviceID == "" || m.live.registering || !m.connectionUsable() || m.hello.Host.ID != m.live.desktop {
		return
	}
	key := fmt.Sprintf("%s:%s:%d", m.live.token, m.link, m.generation)
	if key == m.live.registered {
		return
	}
	m.live.registering = true
	req := remote.ActivityRegistration{DeviceID: m.pushDeviceID, Token: m.live.token, ActivityID: m.live.activity, SessionID: m.live.session}
	client, epoch := m.client, m.live.epoch
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := remote.RegisterActivity(ctx, client, req)
		mygo.RunOnMain(func() {
			if epoch != m.live.epoch {
				return
			}
			m.live.registering = false
			m.live.registered = key
			if err != nil {
				m.live.error = err.Error()
			} else {
				m.live.error = ""
			}
			m.invalidate()
		})
	}()
}
func (m *mobileApp) openMobileURL(raw string) {
	if desktop, sid, ok := remote.ParseActivityURL(raw); ok {
		m.openNotifiedSession(desktop, sid)
		return
	}
	m.connect(raw)
}
func (m *mobileApp) liveActivityDialog(c *ui.Context) {
	ui.DialogBase(c, &m.live.tools, func(back, panel ui.Element) {
		back.Background(colorsOf(c).sheetScrim).Column().Justify(ui.End).AlignItems(ui.Center).Padding(spaceL)
		panel.Label("会话工具面板").FillWidth().MaxWidth(480).MaxHeight(c.Root().Bounds().H - 40).Padding(spaceXL).Radius(radiusSheet).Background(c.Theme().Surface).Column().Gap(spaceL).Clip()
		ui.Row(c).FillWidth().Shrink(0).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "会话工具").FontSize(mobileTitle).Bold().Grow(1)
			if mobileIconAction(c, "关闭会话工具", "x").Clicked() {
				m.live.tools = false
			}
		})
		ui.Scroll(c.Key("mobile-session-tools")).FillWidth().MinHeight(0).Shrink(1).Gap(spaceL).Children(func() {
			if ui.Button(c, "开发页面预览").FillWidth().Height(48).Disabled(!m.connectionUsable()).Clicked() {
				m.live.tools = false
				m.openPreview()
			}
			if ui.Button(c, "订阅用量汇总").FillWidth().Height(48).Clicked() {
				m.live.tools = false
				m.openUsage()
			}
			if ui.Button(c, "在锁屏显示此会话").FillWidth().Height(48).Disabled(!m.connectionUsable()).Clicked() {
				m.trackLiveActivity(m.selected)
			}
			enabled := m.live.enabled
			if mobileSettingSwitch(c, &enabled, "锁屏实时状态", "activity", "锁屏实时状态", "通过系统实时活动显示运行、等待和完成状态").Changed() {
				m.setLiveActivityEnabled(enabled)
			}
			if m.live.id != "" {
				ui.Text(c, "正在跟踪一个会话 · 点按锁屏卡片返回").FontSize(textSmall).TextColor(c.Theme().TextMuted).FillWidth()
			}
			if m.live.error != "" {
				ui.Text(c, m.live.error).FontSize(textSmall).TextColor(c.Theme().Danger).FillWidth()
			}
			if m.live.error != "" && ui.Button(c, "重试锁屏状态").FillWidth().Height(40).Clicked() {
				m.live.last = ""
				m.live.registered = ""
				m.live.error = ""
				m.syncLiveActivity()
			}
		})
	})
}
