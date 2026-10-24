package remote

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"retty/internal/approval"
	"retty/internal/rex"
	"sync/atomic"
	"testing"
)

func TestStructuredApprovalBridgeChecksLivePaneAndSingleUse(t *testing.T) {
	for _, source := range []string{"relay", rex.ProgramStatusSource, "missing hooks"} {
		t.Run(source, func(t *testing.T) { testApprovalBridge(t, source) })
	}
}
func testApprovalBridge(t *testing.T, source string) {
	dir, err := os.MkdirTemp("/tmp", "retty-approval-bridge-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "server.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var exited atomic.Bool
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req rex.Request
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				if req.Op != "list" {
					t.Error("approval modified daemon", req.Op)
					return
				}
				agent := rex.AgentState{ID: "codex", SessionID: "thread", State: "waiting", Reason: "permission"}
				if source == rex.ProgramStatusSource {
					agent.Source = source
					agent.SessionID = "osc7501:"
				}
				if source == "missing hooks" {
					agent = rex.AgentState{}
				}
				data, _ := json.Marshal([]rex.SessionInfo{{ID: "pane", Program: "codex", PID: 777, Cols: 91, Rows: 27, Exited: exited.Load(), Agent: agent}})
				json.NewEncoder(conn).Encode(rex.Response{ID: req.ID, Data: data})
			}()
		}
	}()
	b := &Bridge{approvalDir: dir, imageContext: context.Background()}
	control, peer := net.Pipe()
	defer peer.Close()
	client := rex.NewClient(control, func(context.Context) (net.Conn, error) {
		c, s := net.Pipe()
		go func() { defer s.Close(); b.serveConnection(s, socket) }()
		return c, nil
	})
	defer client.Close()
	store := approval.NewStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeEndpoint, err := approval.Listen(ctx, dir, "pane", store)
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	var count atomic.Int32
	id, err := store.Add(approval.Request{SID: "pane", ThreadID: "thread", Agent: "codex", Title: "RPC request", Options: []approval.Option{{ID: "once", Label: "Allow"}, {ID: "deny", Label: "Deny"}}}, func(choice string) error {
		if choice != "deny" {
			t.Error("wrong decision", choice)
		}
		count.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	queryThread := "thread"
	if source != "relay" {
		queryThread = ""
	}
	got, err := ListApprovals(ctx, client, "pane", queryThread)
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatal(got, err)
	}
	if !approval.ViewerActive(dir, "pane") {
		t.Fatal("phone viewer lease absent")
	}
	if foreign, err := ListApprovals(ctx, client, "pane", "foreign"); err == nil && len(foreign) != 0 {
		t.Fatal("foreign conversation read")
	}
	wrong := got[0]
	wrong.ThreadID = "foreign"
	if err := AnswerApproval(ctx, client, wrong, "deny"); err == nil {
		t.Fatal("foreign thread answered")
	}
	wrong = got[0]
	wrong.Agent = "claude"
	if err := AnswerApproval(ctx, client, wrong, "deny"); err == nil {
		t.Fatal("foreign agent answered")
	}
	if _, err := ListApprovals(ctx, client, "foreign-pane", ""); err == nil {
		t.Fatal("foreign pane read")
	}
	if err = AnswerApproval(ctx, client, got[0], "arbitrary response JSON"); err == nil {
		t.Fatal("unknown decision accepted")
	}
	exited.Store(true)
	if err = AnswerApproval(ctx, client, got[0], "deny"); err == nil {
		t.Fatal("ended pane authorized")
	}
	exited.Store(false)
	if err = AnswerApproval(ctx, client, got[0], "deny"); err != nil {
		t.Fatal(err)
	}
	if err = AnswerApproval(ctx, client, got[0], "deny"); err == nil || count.Load() != 1 {
		t.Fatal("request replayed", err, count.Load())
	}
}
