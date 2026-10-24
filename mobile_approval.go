package main

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/approval"
	"retty/internal/remote"
	"retty/internal/rex"
)

type mobileApproval struct {
	identity                  string
	epoch                     uint64
	requests                  []approval.Request
	shown                     map[string]bool
	loading, showing, sending bool
	next                      time.Time
	show                      func(mygo.MessageOptions) (mygo.MessageResult, error)
	post                      func(func())
	list                      func(context.Context) ([]approval.Request, error)
	answer                    func(context.Context, approval.Request, string) error
}

func (m *mobileApp) approvalAgent() string {
	if !m.resultTerminalVisible() || !m.connectionUsable() || m.busy || m.needsRecovery() || m.selected.Exited || m.selected.Idle || m.imagePasteBusy || m.voice.recording != nil || m.navigation != nil && m.navigation.Path() != "/sessions/terminal" {
		return ""
	}
	agent, _ := rex.ApprovalIdentity(m.selected)
	return agent
}
func approvalThread(info rex.SessionInfo) string {
	_, thread := rex.ApprovalIdentity(info)
	return thread
}

func (s *mobileApproval) dispatch(fn func()) {
	if s.post != nil {
		s.post(fn)
	} else {
		mygo.RunOnMain(fn)
	}
}

func (m *mobileApp) syncApproval(c *ui.Context) {
	if m.approval == nil {
		if runtime.GOOS != "ios" {
			return
		}
		m.approval = &mobileApproval{}
	}
	s := m.approval
	identity := ""
	if m.approvalAgent() != "" {
		identity = fmt.Sprintf("%s/%d/%s/%s", m.preferenceKey(m.selected.ID), m.generation, m.approvalAgent(), approvalThread(m.selected))
	}
	if s.identity != identity {
		s.identity = identity
		s.epoch++
		s.requests = nil
		s.shown = map[string]bool{}
		s.loading = false
		s.next = time.Time{}
	}
	if identity == "" {
		return
	}
	if s.shown == nil {
		s.shown = map[string]bool{}
	}
	if !s.loading && !c.Now().Before(s.next) {
		s.loading = true
		epoch, client, generation := s.epoch, m.client, m.generation
		sid, thread := m.selected.ID, approvalThread(m.selected)
		list := s.list
		if list == nil {
			list = func(ctx context.Context) ([]approval.Request, error) {
				return remote.ListApprovals(ctx, client, sid, thread)
			}
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			requests, err := list(ctx)
			s.dispatch(func() {
				if s.epoch != epoch || m.client != client || m.generation != generation {
					return
				}
				s.loading = false
				s.next = time.Now().Add(time.Second)
				s.requests = nil
				if err == nil {
					for _, r := range requests {
						if r.SID == sid && r.ThreadID != "" && (thread == "" || r.ThreadID == thread) && r.Agent == m.approvalAgent() && r.ID != "" && len(r.Options) >= 2 && len(r.Options) <= 9 && time.Now().Before(r.Expires) {
							s.requests = append(s.requests, r)
						}
					}
				} else {
					s.next = time.Now().Add(5 * time.Second)
				}
				m.invalidate()
			})
		}()
	}
	c.After(time.Second)
	if !s.showing && !s.sending {
		for _, r := range s.requests {
			if !s.shown[r.ID] {
				m.showApproval(c, r)
				break
			}
		}
	}
}

func (m *mobileApp) showApproval(c *ui.Context, r approval.Request) {
	s := m.approval
	if s == nil || s.showing || s.sending || m.approvalAgent() == "" || runtime.GOOS != "ios" && s.show == nil {
		return
	}
	buttons := make([]string, 0, len(r.Options)+1)
	for _, o := range r.Options {
		buttons = append(buttons, o.Label)
	}
	buttons = append(buttons, "返回终端")
	opts := mygo.MessageOptions{Parent: m.win, Style: mygo.MessageActionSheet, Type: mygo.MessageQuestion, Title: r.Title, Message: r.Detail, Buttons: buttons, DefaultButton: len(buttons) - 1, CancelButton: len(buttons) - 1}
	show := s.show
	if show == nil {
		show = mygo.Dialog.Message
	}
	s.showing = true
	s.shown[r.ID] = true
	epoch, client, generation, term := s.epoch, m.client, m.generation, m.term
	m.dismissKeyboard(c.Services())
	go func() {
		result, err := show(opts)
		s.dispatch(func() {
			s.showing = false
			if s.epoch != epoch || m.client != client || m.generation != generation || m.term != term || m.approvalAgent() == "" {
				m.invalidate()
				return
			}
			if err != nil {
				m.error = "无法打开授权面板，请在终端中操作。"
			} else if result.Button >= 0 && result.Button < len(r.Options) {
				present := false
				for _, current := range s.requests {
					present = present || current.ID == r.ID
				}
				if !present {
					m.error = approval.ErrStale.Error()
				} else {
					s.sending = true
					answer := s.answer
					if answer == nil {
						answer = func(ctx context.Context, r approval.Request, choice string) error {
							return remote.AnswerApproval(ctx, client, r, choice)
						}
					}
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						err := answer(ctx, r, r.Options[result.Button].ID)
						s.dispatch(func() {
							s.sending = false
							if s.epoch == epoch && m.client == client && m.generation == generation {
								if err != nil {
									m.error = "授权未提交：" + err.Error()
								}
								s.next = time.Time{}
								m.invalidate()
							}
						})
					}()
				}
			}
			m.invalidate()
		})
	}()
}
func (m *mobileApp) approvalAction(c *ui.Context) {
	s := m.approval
	if s == nil || len(s.requests) == 0 || m.approvalAgent() == "" {
		return
	}
	if mobileIconAction(c, "处理授权", "check").Disabled(s.showing || s.sending).Clicked() {
		m.showApproval(c, s.requests[0])
	}
}
