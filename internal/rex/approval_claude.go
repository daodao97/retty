package rex

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"retty/internal/approval"
)

// A synchronous PermissionRequest hook owns the structured decision. With no
// foreground phone lease, errors, timeout or disconnect it returns no decision
// and Claude continues its normal permission UI. Nothing is auto-approved.
func runClaudeApproval(data []byte, out io.Writer) {
	sid := os.Getenv("RETTY_SESSION")
	if sid == "" || !approval.ViewerActive(Dir(), sid) {
		return
	}
	r, decisions, ok := claudeApprovalRequest(data)
	if !ok {
		return
	}
	r.SID = sid
	r.Expires = time.Now().Add(90 * time.Second)
	store := approval.NewStore()
	ctx, cancel := context.WithDeadline(context.Background(), r.Expires)
	defer cancel()
	closeEndpoint, err := approval.Listen(ctx, Dir(), sid, store)
	if err != nil {
		return
	}
	defer closeEndpoint()
	answer := make(chan json.RawMessage, 1)
	id, err := store.Add(r, func(choice string) error { answer <- decisions[choice]; return nil })
	if err != nil {
		return
	}
	defer store.Remove(id)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case result := <-answer:
			if len(result) > 0 {
				_, _ = out.Write(append(result, '\n'))
			}
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !approval.ViewerActive(Dir(), sid) {
				return
			}
		}
	}
}

func claudeApprovalRequest(data []byte) (approval.Request, map[string]json.RawMessage, bool) {
	var h struct {
		Event       string                       `json:"hook_event_name"`
		Thread      string                       `json:"session_id"`
		AgentID     string                       `json:"agent_id"`
		Tool        string                       `json:"tool_name"`
		Input       json.RawMessage              `json:"tool_input"`
		Suggestions []map[string]json.RawMessage `json:"permission_suggestions"`
	}
	if len(data) > 48<<10 || json.Unmarshal(data, &h) != nil || h.Event != "PermissionRequest" || h.AgentID != "" || h.Thread == "" || h.Tool == "" || len(h.Input) == 0 {
		return approval.Request{}, nil, false
	}
	detail, _ := json.MarshalIndent(struct {
		Tool        string          `json:"tool"`
		Input       json.RawMessage `json:"input"`
		Suggestions any             `json:"permissionSuggestions,omitempty"`
	}{h.Tool, h.Input, h.Suggestions}, "", "  ")
	r := approval.Request{Agent: "claude", ThreadID: h.Thread, Title: "Claude Code 请求使用 " + h.Tool, Detail: string(detail)}
	decisions := map[string]json.RawMessage{}
	add := func(id, label string, d any) {
		r.Options = append(r.Options, approval.Option{ID: id, Label: label})
		b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": d}})
		decisions[id] = b
	}
	add("once", "允许本次", map[string]any{"behavior": "allow"})
	// Suggestions describe semantics, not a copy of the CLI's button list.
	// Only expose narrow additive grants with a visibly session-only lifetime.
	for i, suggestion := range h.Suggestions {
		if i >= 5 {
			break
		}
		var kind, behavior string
		_ = json.Unmarshal(suggestion["type"], &kind)
		_ = json.Unmarshal(suggestion["behavior"], &behavior)
		if kind != "addRules" && kind != "addDirectories" || kind == "addRules" && behavior != "allow" {
			continue
		}
		update := map[string]json.RawMessage{}
		for k, v := range suggestion {
			update[k] = v
		}
		update["destination"] = json.RawMessage(`"session"`)
		id := string(rune('1' + i))
		add("session-"+id, "允许并应用建议 "+id+"（仅本会话）", map[string]any{"behavior": "allow", "updatedPermissions": []any{update}})
	}
	add("deny", "拒绝", map[string]any{"behavior": "deny", "message": "用户在 Retty 手机端拒绝了此请求"})
	r.Options = append(r.Options, approval.Option{ID: "terminal", Label: "在电脑终端处理"})
	decisions["terminal"] = nil
	return r, decisions, true
}
