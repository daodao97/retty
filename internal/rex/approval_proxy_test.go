//go:build darwin || linux

package rex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"retty/internal/approval"
)

// Exercise the actual relay against a disposable app-server transport. No
// lifecycle hooks, real Codex daemon, model calls or terminal input are used.
func TestStructuredCodexProxyWithoutHooks(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "retty-proxy-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex-home"))
	t.Setenv("RETTY_SESSION", "pane")
	t.Setenv("RETTY_AGENT_TOKEN", "fixture")
	t.Setenv("RETTY_AGENT_SOCKET", filepath.Join(dir, "absent.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	socket := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan []byte, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		_, first, err := c.Read(ctx)
		if err != nil {
			return
		}
		var request codexEnvelope
		if json.Unmarshal(first, &request) != nil {
			return
		}
		if request.Method == "initialize" {
			// The relay's metadata connection is independent of the CLI and
			// must never consume or answer the CLI's approval request.
			for {
				if len(request.ID) > 0 {
					var result any = map[string]any{}
					switch request.Method {
					case "initialize":
					case "thread/read":
						result = map[string]any{"thread": codexThread{ID: "thread", Status: codexStatus{Type: "idle"}}}
					case "thread/turns/list":
						result = map[string]any{"data": []codexTurn{}}
					default:
						t.Errorf("unexpected metadata request %q", request.Method)
						return
					}
					raw, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
					if c.Write(ctx, websocket.MessageText, raw) != nil {
						return
					}
				}
				_, raw, err := c.Read(ctx)
				if err != nil {
					return
				}
				request = codexEnvelope{}
				if json.Unmarshal(raw, &request) != nil {
					return
				}
			}
		}
		if err = c.Write(ctx, websocket.MessageText, []byte(`{"id":1,"result":{"thread":{"id":"thread","cwd":"/tmp","status":{"type":"idle"}}}}`)); err != nil {
			return
		}
		if err = c.Write(ctx, websocket.MessageText, []byte(codexApprovalWire)); err != nil {
			return
		}
		_, reply, err := c.Read(ctx)
		if err == nil {
			responses <- reply
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	parent := exec.Command("sleep", "60")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	proxyDir := filepath.Join(dir, "proxy")
	if err := os.Mkdir(proxyDir, 0700); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- RunCodexProxy(proxyDir, parent.Process.Pid, socket) }()
	defer func() {
		parent.Process.Kill()
		parent.Wait()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("owned relay did not stop")
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(proxyDir, "ready")); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cli, err := dialCodex(ctx, filepath.Join(proxyDir, "bridge.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.CloseNow()
	if err := cli.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"thread/resume","params":{"threadId":"thread"}}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := cli.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	requests, err := approval.List(ctx, dir, "pane", "")
	if err != nil || len(requests) != 1 || requests[0].ThreadID != "thread" {
		t.Fatal(requests, err)
	}
	if err := approval.Answer(ctx, dir, "pane", "thread", requests[0].ID, "decline"); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-responses:
		var reply struct {
			ID     int `json:"id"`
			Result struct {
				Decision string `json:"decision"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &reply) != nil || reply.ID != 17 || reply.Result.Decision != "decline" {
			t.Fatal(string(raw))
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := approval.Answer(ctx, dir, "pane", "thread", requests[0].ID, "accept"); err == nil {
		t.Fatal("replayed pending RPC")
	}
}
