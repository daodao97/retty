//go:build darwin || linux

package rex

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"retty/internal/agents"
)

func TestCodexProxyMetadataRepairsLostEventsWithoutOverwritingNewWork(t *testing.T) {
	for _, race := range []string{"none", "next turn", "different thread"} {
		t.Run(race, func(t *testing.T) {
			var tracker agentTracker
			o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
				tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
			}}
			o.message([]byte(`{"id":1,"method":"thread/start"}`), true)
			o.message([]byte(`{"id":1,"result":{"thread":{"id":"own","cwd":"/fixture"}}}`), false)
			o.message([]byte(`{"method":"turn/started","params":{"threadId":"own","turn":{"id":"lost"}}}`), false)
			dir, err := os.MkdirTemp("/tmp", "retty-metadata-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "daemon.sock")
			ln, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			reads := 0
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				for {
					_, raw, err := c.Read(r.Context())
					if err != nil {
						return
					}
					var request codexEnvelope
					if json.Unmarshal(raw, &request) != nil {
						t.Error("invalid metadata request")
						return
					}
					if request.Method == "initialized" {
						continue
					}
					var result any = map[string]any{}
					switch request.Method {
					case "initialize":
					case "thread/read", "thread/turns/list":
						var params struct {
							ThreadID     string `json:"threadId"`
							IncludeTurns bool   `json:"includeTurns"`
							ItemsView    string `json:"itemsView"`
						}
						json.Unmarshal(request.Params, &params)
						if params.ThreadID != "own" || params.IncludeTurns || request.Method == "thread/turns/list" && params.ItemsView != "notLoaded" {
							t.Error("metadata read loaded content or targeted a foreign thread")
							return
						}
						if request.Method == "thread/read" {
							reads++
							if reads == 2 {
								switch race {
								case "next turn":
									o.message([]byte(`{"method":"turn/started","params":{"threadId":"own","turn":{"id":"next"}}}`), false)
								case "different thread":
									o.message([]byte(`{"id":2,"method":"thread/resume"}`), true)
									o.message([]byte(`{"id":2,"result":{"thread":{"id":"new-owner","cwd":"/fixture","status":{"type":"active"}}}}`), false)
								}
							}
							result = map[string]any{"thread": codexThread{ID: "own", Status: codexStatus{Type: "idle"}}}
						} else {
							result = map[string]any{"data": []codexTurn{{ID: "lost", Status: "completed"}}}
						}
					default:
						t.Errorf("mutating or unexpected request %q", request.Method)
						return
					}
					response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
					// JSON-RPC peers have separate ID namespaces. Ignore an
					// unsolicited server request with the same response ID.
					unsolicited, _ := json.Marshal(map[string]any{"id": request.ID, "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "foreign"}})
					if c.Write(r.Context(), websocket.MessageText, unsolicited) != nil {
						return
					}
					if c.Write(r.Context(), websocket.MessageText, response) != nil {
						return
					}
				}
			})}
			go server.Serve(ln)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := newCodexMetadataClient(ctx, socket)
			if err != nil {
				t.Fatal(err)
			}
			defer client.conn.CloseNow()
			if err := o.reconcileSnapshot(ctx, client); err != nil {
				t.Fatal(err)
			}
			switch race {
			case "none":
				if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
					t.Fatal("missed completion not repaired", tracker.state)
				}
			case "next turn":
				if tracker.state.State != agents.Running || tracker.turnID != "next" || tracker.state.CompletionRevision != 0 {
					t.Fatal("old snapshot completed new work", tracker.state)
				}
			case "different thread":
				if tracker.state.SessionID != "new-owner" || tracker.state.State != agents.Running || tracker.state.CompletionRevision != 0 {
					t.Fatal("old snapshot replaced new thread", tracker.state)
				}
			}
		})
	}
}

func TestCodexMetadataFinishesWhenBothTurnNotificationsWereMissed(t *testing.T) {
	var events []agents.HookInput
	o := &codexObserver{emit: func(h agents.HookInput) { events = append(events, h) }}
	o.snapshot(codexThread{ID: "own", Status: codexStatus{Type: "active"}}, codexTurn{ID: "previous", Status: "completed"})
	o.snapshot(codexThread{ID: "own", Status: codexStatus{Type: "idle"}}, codexTurn{ID: "unseen", Status: "completed"})
	if len(events) != 3 || events[2].Event != "Stop" || events[2].TurnID != "unseen" {
		t.Fatal("metadata-only task did not finish", events)
	}
}

func TestCodexWatchSeedsOnlyItsExistingPaneAndThread(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "retty-watch-pane-")
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var req Request
			raw, _ := bufio.NewReader(c).ReadBytes('\n')
			if json.Unmarshal(raw, &req) != nil || req.Op != "list" {
				t.Error("watch attachment used a non-read request")
				c.Close()
				continue
			}
			data, _ := json.Marshal([]SessionInfo{
				{ID: "own", Agent: AgentState{ID: "codex", SessionID: "thread", State: agents.Completed, CompletionRevision: 7}},
				{ID: "exited", Exited: true, Agent: AgentState{ID: "codex", SessionID: "thread", State: agents.Running}},
			})
			json.NewEncoder(c).Encode(Response{ID: req.ID, Data: data})
			c.Close()
		}
	}()
	if state, ok := codexPaneState(socket, "own", "thread"); !ok || state.State != agents.Completed || state.CompletionRevision != 7 {
		t.Fatal("attachment lost the existing result", state, ok)
	}
	for _, args := range [][2]string{{"own", "other-thread"}, {"other-pane", "thread"}, {"exited", "thread"}} {
		if state, ok := codexPaneState(socket, args[0], args[1]); ok {
			t.Fatal("watch seeded an unowned or exited pane", state)
		}
	}
	ln.Close()
	<-done
}
