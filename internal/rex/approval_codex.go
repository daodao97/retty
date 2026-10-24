//go:build darwin || linux

package rex

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"retty/internal/approval"
)

type codexApproval struct{ local, thread, turn, item string }
type codexApprovals struct {
	mu      sync.Mutex
	store   *approval.Store
	sid     string
	pending map[string]codexApproval
	settled map[string]bool
	files   map[string]string
	write   func([]byte) error
}

func newCodexApprovals(sid string, store *approval.Store, write func([]byte) error) *codexApprovals {
	return &codexApprovals{store: store, sid: sid, pending: map[string]codexApproval{}, settled: map[string]bool{}, files: map[string]string{}, write: write}
}

// send serializes CLI and phone responses through the same pending-request
// lock. A late CLI response to an approval already answered on the phone is
// swallowed, while ordinary client RPCs keep their separate ID namespace.
func (a *codexApprovals) send(raw []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Method == "" && len(m.ID) > 0 {
		id := string(m.ID)
		if a.settled[id] {
			return nil
		}
		if p, ok := a.pending[id]; ok {
			a.store.Remove(p.local)
			delete(a.pending, id)
			a.settle(id)
		}
	}
	return a.write(raw)
}
func (a *codexApprovals) settle(id string) {
	if len(a.settled) >= 512 {
		clear(a.settled)
	}
	a.settled[id] = true
}
func (a *codexApprovals) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, p := range a.pending {
		a.store.Remove(p.local)
		delete(a.pending, id)
	}
}

// receive inspects structured requests only for the CLI's established root
// conversation. Foreign broadcasts and subagents cannot acquire this pane.
func (a *codexApprovals) receive(raw []byte, owner string) {
	var m codexEnvelope
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	var p struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		RequestID json.RawMessage `json:"requestId"`
		ItemID    string          `json:"itemId"`
		Status    codexStatus     `json:"status"`
		Item      struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Changes []struct {
				Path string `json:"path"`
				Diff string `json:"diff"`
			} `json:"changes"`
		} `json:"item"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.ThreadID == owner && m.Method == "item/started" && p.Item.Type == "fileChange" && len(raw) <= 64<<10 && len(a.files) < 32 {
		var details []string
		for _, change := range p.Item.Changes {
			details = append(details, "文件："+change.Path+"\n"+change.Diff)
		}
		a.files[p.ThreadID+"/"+p.Item.ID] = strings.Join(details, "\n\n")
	}
	if m.Method == "item/completed" {
		delete(a.files, p.ThreadID+"/"+p.Item.ID)
	}
	for key := range a.files {
		if m.Method == "turn/completed" && strings.HasPrefix(key, p.ThreadID+"/") || owner != "" && !strings.HasPrefix(key, owner+"/") {
			delete(a.files, key)
		}
	}
	for id, pending := range a.pending {
		resolved := m.Method == "serverRequest/resolved" && pending.thread == p.ThreadID && id == string(p.RequestID)
		finished := m.Method == "turn/completed" && pending.thread == p.ThreadID && pending.turn == p.Turn.ID
		idle := m.Method == "thread/status/changed" && p.ThreadID == pending.thread && p.Status.Type == "idle"
		completedItem := m.Method == "item/completed" && p.ThreadID == pending.thread && p.Item.ID != "" && p.Item.ID == pending.item
		if resolved || finished || idle || completedItem || owner != "" && pending.thread != owner {
			a.store.Remove(pending.local)
			delete(a.pending, id)
			a.settle(id)
		}
	}
	if len(m.ID) == 0 || string(m.ID) == "null" || owner == "" || p.ThreadID != owner {
		return
	}
	r, decisions, err := codexApprovalRequest(m.Method, m.Params)
	if err != nil {
		return
	}
	if m.Method == "item/fileChange/requestApproval" {
		preview := a.files[p.ThreadID+"/"+p.ItemID]
		if preview == "" {
			return
		} // Never ask the user to approve unseen file edits.
		r.Detail = strings.TrimSpace(r.Detail + "\n\n" + preview)
	}
	r.SID = a.sid
	id := string(m.ID)
	if _, ok := a.pending[id]; ok {
		return
	}
	delete(a.settled, id)
	local := ""
	local, err = a.store.Add(r, func(choice string) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		pending, ok := a.pending[id]
		if !ok || pending.local != local {
			return approval.ErrStale
		}
		decision, ok := decisions[choice]
		if !ok {
			return errors.New("invalid Codex decision")
		}
		delete(a.pending, id)
		a.settle(id)
		response, _ := json.Marshal(struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}{m.ID, decision})
		return a.write(response)
	})
	if err == nil {
		a.pending[id] = codexApproval{local, p.ThreadID, p.TurnID, p.ItemID}
	}
}

func codexApprovalRequest(method string, raw json.RawMessage) (approval.Request, map[string]json.RawMessage, error) {
	var p struct {
		ThreadID    string            `json:"threadId"`
		TurnID      string            `json:"turnId"`
		Available   []json.RawMessage `json:"availableDecisions"`
		Permissions json.RawMessage   `json:"permissions"`
		Proposed    []string          `json:"proposedExecpolicyAmendment"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ThreadID == "" || p.TurnID == "" || len(raw) > 64<<10 {
		return approval.Request{}, nil, errors.New("invalid approval payload")
	}
	r := approval.Request{Agent: "codex", ThreadID: p.ThreadID, TurnID: p.TurnID}
	r.Detail = codexApprovalDetail(raw)
	decisions := map[string]json.RawMessage{}
	add := func(id, label string, result any) {
		b, _ := json.Marshal(result)
		r.Options = append(r.Options, approval.Option{ID: id, Label: label})
		decisions[id] = b
	}
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		r.Title = "Codex 请求执行命令"
		if method == "item/fileChange/requestApproval" {
			r.Title = "Codex 请求修改文件"
		}
		available := p.Available
		if available == nil {
			for _, d := range []string{"accept", "acceptForSession", "decline", "cancel"} {
				b, _ := json.Marshal(d)
				available = append(available, b)
			}
			if len(p.Proposed) > 0 {
				b, _ := json.Marshal(map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": p.Proposed}})
				available = append(available, b)
			}
		}
		labels := map[string]string{"accept": "允许本次", "acceptForSession": "允许本会话", "decline": "拒绝", "cancel": "取消当前操作"}
		for _, d := range available {
			var key string
			if json.Unmarshal(d, &key) == nil {
				if label, ok := labels[key]; ok {
					add(key, label, map[string]any{"decision": key})
				}
				continue
			}
			var amendment map[string]json.RawMessage
			if json.Unmarshal(d, &amendment) == nil && len(amendment) == 1 {
				if _, ok := amendment["acceptWithExecpolicyAmendment"]; ok {
					add("acceptWithExecpolicyAmendment", "允许并保存请求中列出的命令规则", map[string]any{"decision": json.RawMessage(d)})
				}
			}
		}
	case "item/permissions/requestApproval":
		r.Title = "Codex 请求文件或网络权限"
		var permissions map[string]json.RawMessage
		if json.Unmarshal(p.Permissions, &permissions) != nil || len(permissions) == 0 {
			return r, nil, errors.New("missing requested permissions")
		}
		add("turn", "允许本轮所列权限", map[string]any{"permissions": permissions, "scope": "turn"})
		add("session", "允许本会话所列权限", map[string]any{"permissions": permissions, "scope": "session"})
		add("deny", "拒绝", map[string]any{"permissions": map[string]any{}, "scope": "turn"})
	default:
		return r, nil, errors.New("unsupported Codex approval")
	}
	if len(r.Options) < 2 || len(r.Options) > 9 {
		return r, nil, errors.New("unsupported approval decisions")
	}
	return r, decisions, nil
}

func codexApprovalDetail(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var out []string
	for _, field := range []struct{ key, label string }{{"command", "命令"}, {"cwd", "工作目录"}, {"reason", "原因"}, {"networkApprovalContext", "网络访问"}, {"grantRoot", "请求写入目录"}, {"permissions", "请求权限"}, {"additionalPermissions", "额外权限"}, {"proposedExecpolicyAmendment", "建议保存的命令规则"}, {"proposedNetworkPolicyAmendments", "建议保存的网络规则"}} {
		value := fields[field.key]
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			var v any
			_ = json.Unmarshal(value, &v)
			pretty, _ := json.MarshalIndent(v, "", "  ")
			text = string(pretty)
		}
		if text != "" {
			out = append(out, field.label+"：\n"+text)
		}
	}
	return strings.Join(out, "\n\n")
}
