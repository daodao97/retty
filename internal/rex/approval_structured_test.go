//go:build darwin || linux

package rex

import (
	"bytes"
	"context"
	"encoding/json"
	"retty/internal/approval"
	"strings"
	"sync"
	"testing"
	"time"
)

const codexApprovalWire = `{"id":17,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread","turnId":"turn","itemId":"item","command":"echo fixture","availableDecisions":["accept","decline","cancel"]}}`

func TestStructuredCodexApprovalCompetingResponses(t *testing.T) {
	for _, winner := range []string{"phone", "desktop", "concurrent", "resolved", "finished", "other thread"} {
		t.Run(winner, func(t *testing.T) {
			s := approval.NewStore()
			var sent [][]byte
			a := newCodexApprovals("pane", s, func(b []byte) error { sent = append(sent, append([]byte(nil), b...)); return nil })
			defer a.close()
			a.receive([]byte(codexApprovalWire), "foreign")
			if len(s.List("pane", "thread")) != 0 {
				t.Fatal("foreign request leaked")
			}
			a.receive([]byte(codexApprovalWire), "thread")
			r := s.List("pane", "thread")
			if len(r) != 1 || len(r[0].Options) != 3 {
				t.Fatal(r)
			}
			desktop := []byte(`{"id":17,"result":{"decision":"accept"}}`)
			switch winner {
			case "phone":
				if err := s.Answer("pane", "thread", r[0].ID, "decline"); err != nil {
					t.Fatal(err)
				}
				a.send(desktop)
			case "desktop":
				a.send(desktop)
				if s.Answer("pane", "thread", r[0].ID, "decline") == nil {
					t.Fatal("late phone accepted")
				}
			case "concurrent":
				var wg sync.WaitGroup
				wg.Go(func() { _ = s.Answer("pane", "thread", r[0].ID, "decline") })
				wg.Go(func() { _ = a.send(desktop) })
				wg.Wait()
			case "resolved":
				a.receive([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":17}}`), "thread")
			case "finished":
				a.receive([]byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`), "thread")
			case "other thread":
				a.receive([]byte(`{"method":"thread/status/changed","params":{"threadId":"new"}}`), "new")
			}
			if len(s.List("pane", "thread")) != 0 {
				t.Fatal("request remained pending")
			}
			expected := 1
			if winner == "resolved" || winner == "finished" || winner == "other thread" {
				expected = 0
				if s.Answer("pane", "thread", r[0].ID, "accept") == nil {
					t.Fatal("stale response accepted")
				}
			}
			if len(sent) != expected {
				t.Fatal("duplicate or missing RPC response", len(sent))
			}
			// Client requests have a separate namespace, even with the same numeric ID.
			a.send([]byte(`{"id":17,"method":"thread/read","params":{"threadId":"thread"}}`))
			if len(sent) != expected+1 {
				t.Fatal("client RPC swallowed")
			}
		})
	}
}
func TestStructuredCodexApprovalRespectsChoicesAndPermissions(t *testing.T) {
	var m codexEnvelope
	json.Unmarshal([]byte(codexApprovalWire), &m)
	r, d, err := codexApprovalRequest(m.Method, m.Params)
	if err != nil || len(r.Options) != 3 || d["acceptForSession"] != nil || !strings.Contains(r.Detail, "echo fixture") {
		t.Fatal(r, d, err)
	}
	r, d, err = codexApprovalRequest("item/permissions/requestApproval", json.RawMessage(`{"threadId":"thread","turnId":"turn","permissions":{"network":{"enabled":true}}}`))
	if err != nil || len(r.Options) != 3 || string(d["deny"]) != `{"permissions":{},"scope":"turn"}` {
		t.Fatal(r, d, err)
	}
	if _, _, err = codexApprovalRequest("item/tool/requestUserInput", m.Params); err == nil {
		t.Fatal("question disguised as permission")
	}
}

const claudeApprovalWire = `{"session_id":"thread","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"echo fixture"},"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"echo fixture"}],"behavior":"allow","destination":"userSettings"}]}`

func TestStructuredClaudeHookReturnsJSONAndFallsBackWithoutViewer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("RETTY_SESSION", "pane")
	var out bytes.Buffer
	started := time.Now()
	runClaudeApproval([]byte(claudeApprovalWire), &out)
	if out.Len() != 0 || time.Since(started) > time.Second {
		t.Fatal("desktop hook blocked without phone")
	}
	for _, choice := range []string{"once", "session-1", "deny", "terminal"} {
		t.Run(choice, func(t *testing.T) {
			if err := approval.TouchViewer(dir, "pane"); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			out.Reset()
			go func() { defer close(done); runClaudeApproval([]byte(claudeApprovalWire), &out) }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var r []approval.Request
			for ctx.Err() == nil {
				r, _ = approval.List(ctx, dir, "pane", "thread")
				if len(r) > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if len(r) != 1 {
				t.Fatal("hook request unavailable")
			}
			if err := approval.Answer(ctx, dir, "pane", "thread", r[0].ID, choice); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("hook did not return")
			}
			if choice == "terminal" {
				if out.Len() != 0 {
					t.Fatal("fallback made a decision")
				}
				return
			}
			var result map[string]any
			if json.Unmarshal(out.Bytes(), &result) != nil {
				t.Fatal("hook returned invalid JSON", out.String())
			}
			if choice == "session-1" && (!strings.Contains(out.String(), `"destination":"session"`) || strings.Contains(out.String(), `"destination":"userSettings"`)) {
				t.Fatal("session grant persisted", out.String())
			}
			if choice == "deny" && !strings.Contains(out.String(), `"behavior":"deny"`) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestStructuredCodexFileApprovalRequiresPreviewAndRevokesCompletedItem(t *testing.T) {
	store := approval.NewStore()
	a := newCodexApprovals("pane", store, func([]byte) error { return nil })
	defer a.close()
	request := []byte(`{"id":19,"method":"item/fileChange/requestApproval","params":{"threadId":"thread","turnId":"turn","itemId":"file","reason":"Edit fixture"}}`)
	a.receive(request, "thread")
	if len(store.List("pane", "thread")) != 0 {
		t.Fatal("file approval without changes")
	}
	a.receive([]byte(`{"method":"item/started","params":{"threadId":"thread","turnId":"turn","item":{"id":"file","type":"fileChange","changes":[{"path":"fixture.txt","diff":"-old\n+new"}]}}}`), "thread")
	a.receive(request, "thread")
	pending := store.List("pane", "thread")
	if len(pending) != 1 || !strings.Contains(pending[0].Detail, "fixture.txt") || !strings.Contains(pending[0].Detail, "+new") {
		t.Fatal("file preview missing", pending)
	}
	a.receive([]byte(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"id":"file","type":"fileChange"}}}`), "thread")
	if len(store.List("pane", "thread")) != 0 || store.Answer("pane", "thread", pending[0].ID, "accept") == nil {
		t.Fatal("completed item could be authorized")
	}
}

func TestStructuredCodexApprovalProposedCommandRule(t *testing.T) {
	raw := json.RawMessage(`{"threadId":"thread","turnId":"turn","command":"docker compose -f compose.yaml restart","proposedExecpolicyAmendment":["docker","compose","-f","compose.yaml","restart"]}`)
	r, decisions, err := codexApprovalRequest("item/commandExecution/requestApproval", raw)
	if err != nil || !strings.Contains(r.Detail, "compose.yaml") {
		t.Fatal(r, err)
	}
	want := `{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["docker","compose","-f","compose.yaml","restart"]}}}`
	if string(decisions["acceptWithExecpolicyAmendment"]) != want {
		t.Fatal(string(decisions["acceptWithExecpolicyAmendment"]))
	}
	// An explicit decision list is authoritative, including absence of saved rules.
	raw = json.RawMessage(`{"threadId":"thread","turnId":"turn","availableDecisions":["accept","decline"],"proposedExecpolicyAmendment":["docker"]}`)
	_, decisions, err = codexApprovalRequest("item/commandExecution/requestApproval", raw)
	if err != nil || decisions["acceptWithExecpolicyAmendment"] != nil {
		t.Fatal(decisions, err)
	}
}
