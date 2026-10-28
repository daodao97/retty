package remote

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/push/apns"
	"retty/internal/rex"
)

// ActivityRegistration uses an ActivityKit token, never the alert device token.
// Registrations live only for this gateway's lifetime and expire after 8 hours.
type ActivityRegistration struct {
	Op         string `json:"op"`
	DeviceID   string `json:"deviceID"`
	Token      string `json:"token,omitempty"`
	ActivityID string `json:"activityID"`
	SessionID  string `json:"sessionID"`
	Disabled   bool   `json:"disabled,omitempty"`
}
type ActivityState struct {
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle"`
	Status    string   `json:"status"`
	URL       string   `json:"url"`
	Progress  *float64 `json:"progress"`
	UpdatedAt int64    `json:"updatedAt"`
}

func ActivityContent(host rex.HostInfo, s rex.SessionInfo) ActivityState {
	state := ActivityState{Title: "Retty · " + host.Name, Subtitle: "Agent 运行中", Status: s.Agent.State, URL: "retty://session?" + url.Values{"desktop": {host.ID}, "session": {s.ID}}.Encode()}
	switch s.Agent.State {
	case "ready":
		state.Subtitle = "等待开始任务"
	case "":
		state.Status, state.Subtitle = "unknown", "状态暂不可用"
		if s.Idle {
			state.Status, state.Subtitle = "ended", "会话已回到终端"
		}
	case "waiting":
		state.Subtitle = "等待你的处理"
	case "completed", "complete", "done", "idle":
		state.Subtitle = "任务已完成"
	case "error", "failed":
		state.Subtitle = "任务遇到问题"
	}
	if s.Exited {
		state.Status = "ended"
		state.Subtitle = "会话已结束"
	}
	if s.Agent.Progress != nil {
		v := float64(max(0, min(100, *s.Agent.Progress))) / 100
		state.Progress = &v
	}
	// Lock-screen content excludes commands, prompts, paths and Agent replies.
	state.Title = string([]rune(state.Title)[:min(64, len([]rune(state.Title)))])
	return state
}
func activityEnded(state ActivityState) bool {
	switch state.Status {
	case "ended", "completed", "complete", "done", "idle", "error", "failed":
		return true
	}
	return false
}
func (r ActivityRegistration) validate() error {
	device, e := hex.DecodeString(r.DeviceID)
	if e != nil || len(device) != 16 || r.ActivityID == "" || len(r.ActivityID) > 128 || r.SessionID == "" || len(r.SessionID) > 128 {
		return errors.New("无效实时状态请求")
	}
	if !r.Disabled {
		token, e := hex.DecodeString(r.Token)
		if e != nil || len(token) < 16 || len(token) > 256 {
			return errors.New("无效实时状态令牌")
		}
	}
	return nil
}
func RegisterActivity(ctx context.Context, client *rex.Client, req ActivityRegistration) error {
	req.Op = "live-activity"
	if err := req.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	var res rex.Response
	if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&res) != nil {
		return errors.New("无法注册锁屏状态，请更新主机网关后重试")
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

type activityPublisher struct {
	wake    chan struct{}
	ctx     context.Context
	socket  string
	manager *apns.ActivityManager
	read    func(context.Context) (rex.Hello, []rex.SessionInfo, error)
}

func newActivityPublisher(ctx context.Context, socket string, provider apns.ActivitySender) (*activityPublisher, error) {
	manager, err := apns.NewActivityManager(provider, apns.ActivityManagerOptions{
		MaxActivities: 32, Lifetime: 8 * time.Hour, RefreshInterval: time.Minute,
		StaleAfter: 2 * time.Minute, DismissAfter: 10 * time.Minute, SendTimeout: 8 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return &activityPublisher{ctx: ctx, socket: socket, manager: manager, wake: make(chan struct{}, 1)}, nil
}

func (b *Bridge) serveLiveActivity(conn net.Conn, socket string, line []byte) {
	conn.SetDeadline(time.Now().Add(12 * time.Second))
	var req ActivityRegistration
	var err error
	if len(line) > 2048 || json.Unmarshal(line, &req) != nil {
		err = errors.New("无效实时状态请求")
	} else {
		err = req.validate()
	}
	if err == nil {
		b.mu.Lock()
		p := b.activity
		if p == nil && !req.Disabled {
			provider, e := apns.OpenProvider(b.approvalDir)
			if e != nil {
				err = errors.New("主机尚未配置锁屏推送，请先配置 APNs")
			} else {
				p, err = newActivityPublisher(b.imageContext, socket, provider)
				if err == nil {
					b.activity = p
					go p.run()
				}
			}
		}
		b.mu.Unlock()
		if p != nil {
			err = p.register(req)
		}
	}
	res := rex.Response{}
	if err != nil {
		res.Error = err.Error()
	}
	json.NewEncoder(conn).Encode(res)
}
func (p *activityPublisher) register(req ActivityRegistration) error {
	key := req.DeviceID + ":" + req.ActivityID
	if req.Disabled {
		p.manager.Remove(key)
		return nil
	}
	if err := p.manager.Register(apns.ActivityRegistration{ID: key, DeviceToken: req.Token, ResourceID: req.SessionID}); err != nil {
		if errors.Is(err, apns.ErrActivityLimit) {
			return errors.New("锁屏状态设备数量已达上限")
		}
		return err
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return nil
}
func (p *activityPublisher) sessions(ctx context.Context) (rex.Hello, []rex.SessionInfo, error) {
	if p.read != nil {
		return p.read(ctx)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", p.socket)
	if err != nil {
		return rex.Hello{}, nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	client := rex.NewClient(conn, nil)
	h, err := client.Hello()
	if err != nil {
		return h, nil, err
	}
	sessions, err := client.List()
	return h, sessions, err
}
func (p *activityPublisher) run() {
	for {
		active := len(p.manager.Targets()) > 0
		var timer *time.Timer
		var tick <-chan time.Time
		if active {
			timer = time.NewTimer(3 * time.Second)
			tick = timer.C
		}
		select {
		case <-p.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-p.wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
		p.publish()
	}
}
func (p *activityPublisher) publish() {
	targets := p.manager.Targets()
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 8*time.Second)
	defer cancel()
	h, sessions, err := p.sessions(ctx)
	if err != nil {
		return
	}
	byID := make(map[string]rex.SessionInfo, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		s, ok := byID[target.ResourceID]
		if !ok {
			s = rex.SessionInfo{ID: target.ResourceID, Exited: true}
		}
		state := ActivityContent(h.Host, s)
		stable, _ := json.Marshal(state)
		state.UpdatedAt = time.Now().Unix()
		content, _ := json.Marshal(state)
		_ = p.manager.Publish(ctx, target, apns.ActivityUpdate{
			ContentState: content, DeduplicationKey: string(stable), End: activityEnded(state),
		})
	}
}

// Activity deep links carry stable identifiers only, never a pairing capability.
func ParseActivityURL(raw string) (string, string, bool) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "retty" || u.Host != "session" || u.User != nil || len(raw) > 1024 {
		return "", "", false
	}
	d, s := u.Query().Get("desktop"), u.Query().Get("session")
	return d, s, d != "" && s != "" && !strings.ContainsAny(d+s, "\r\n\x00") && len(d) <= 256 && len(s) <= 128
}
