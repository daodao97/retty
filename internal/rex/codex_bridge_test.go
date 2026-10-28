//go:build darwin || linux

package rex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"retty/internal/agents"
)

func TestCodexBridgeOnlyTracksItsCLIThread(t *testing.T) {
	var tracker agentTracker
	var events []agents.HookInput
	o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
		events = append(events, h)
		tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
	}}
	message := func(client bool, value string) { t.Helper(); o.message([]byte(value), client) }
	// Thread notifications broadcast by the daemon cannot claim this pane.
	message(false, `{"method":"thread/started","params":{"thread":{"id":"foreign","cwd":"/same"}}}`)
	message(false, `{"method":"turn/started","params":{"threadId":"foreign","turn":{"id":"foreign-turn"}}}`)
	if len(events) != 0 {
		t.Fatal("foreign daemon thread claimed pane")
	}
	message(true, `{"id":1,"method":"thread/start","params":{"cwd":"/same"}}`)
	// Server requests use a separate ID namespace and may overlap client IDs.
	message(false, `{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"foreign"}}`)
	message(false, `{"id":1,"result":{"thread":{"id":"own","cwd":"/same","status":{"type":"idle"}}}}`)
	message(false, `{"method":"turn/started","params":{"threadId":"own","turn":{"id":"turn","status":"inProgress"}}}`)
	if tracker.state.SessionID != "own" || tracker.state.State != agents.Running {
		t.Fatalf("unbound turn: %+v", tracker.state)
	}
	tracker.apply(AgentEvent{Agent: "codex", At: time.Now(), Input: agents.HookInput{SessionID: "own", TurnID: "turn", Event: "Stop"}})
	if tracker.state.State != agents.Running || tracker.state.CompletionRevision != 0 {
		t.Fatal("native Stop hook finished a turn before the daemon")
	}
	message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`)
	message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`)
	if tracker.state.State != agents.Waiting || tracker.state.Reason != "permission" || tracker.state.WaitRevision != 1 {
		t.Fatalf("permission status: %+v", tracker.state)
	}
	message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active","activeFlags":["waitingOnUserInput"]}}}`)
	if tracker.state.Reason != "question" {
		t.Fatalf("question status: %+v", tracker.state)
	}
	message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active","activeFlags":[]}}}`)
	if tracker.state.State != agents.Running {
		t.Fatalf("unresolved wait: %+v", tracker.state)
	}
	message(false, `{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"old-turn","status":"completed"}}}`)
	message(false, `{"method":"turn/completed","params":{"threadId":"foreign","turn":{"id":"turn","status":"completed"}}}`)
	if tracker.state.State != agents.Running {
		t.Fatal("stale/foreign completion accepted")
	}
	message(false, `{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"turn","status":"completed"}}}`)
	message(false, `{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"turn","status":"completed"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatal("terminal turn event waited for an idle notification")
	}
	message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatalf("completion: %+v", tracker.state)
	}
	for _, finish := range []struct{ status, want string }{{"failed", agents.Failed}, {"interrupted", agents.Ready}} {
		message(false, `{"method":"turn/started","params":{"threadId":"own","turn":{"id":"next","status":"inProgress"}}}`)
		data, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "own", "turn": map[string]string{"id": "next", "status": finish.status}}})
		o.message(data, false)
		message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`)
		if tracker.state.State != finish.want {
			t.Fatalf("%s treated as %s", finish.status, tracker.state.State)
		}
	}
	// An error reply and a subagent response cannot replace the root owner.
	message(true, `{"id":"resume","method":"thread/resume","params":{"threadId":"missing"}}`)
	message(false, `{"id":"resume","error":{"code":-1,"message":"not found"}}`)
	message(true, `{"id":2,"method":"thread/start","params":{}}`)
	message(false, `{"id":2,"result":{"thread":{"id":"child","parentThreadId":"own","cwd":"/same"}}}`)
	if o.thread.ID != "own" {
		t.Fatal("failed resume/subagent replaced root")
	}
}

func TestCodexBridgeKeepsSameDirectoryPanesIndependent(t *testing.T) {
	for _, id := range []string{"first", "second"} {
		var seen []agents.HookInput
		o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) { seen = append(seen, h) }}
		o.message([]byte(`{"id":1,"method":"thread/start"}`), true)
		bound, _ := json.Marshal(map[string]any{"id": 1, "result": map[string]any{"thread": map[string]string{"id": id, "cwd": "/same"}}})
		o.message(bound, false)
		for _, thread := range []string{"first", "second"} {
			raw, _ := json.Marshal(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": "turn"}}})
			o.message(raw, false)
		}
		if len(seen) != 2 || seen[1].SessionID != id {
			t.Fatal("same cwd crossed pane ownership")
		}
	}
}

func TestCodexBridgeBindsContinuedThreadFromSuccessfulTurnRequest(t *testing.T) {
	for _, method := range []string{"turn/start", "turn/steer"} {
		t.Run(method, func(t *testing.T) {
			var tracker agentTracker
			o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
				tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
			}}
			o.message([]byte(`{"id":1,"method":"thread/start"}`), true)
			o.message([]byte(`{"id":1,"result":{"thread":{"id":"startup","cwd":"/project"}}}`), false)
			request, _ := json.Marshal(map[string]any{"id": 2, "method": method, "params": map[string]string{"threadId": "continued"}})
			o.message(request, true)
			// Broadcasts can arrive before the successful reply and cannot bind
			// a pane by themselves. An overlapping server request is unrelated.
			o.message([]byte(`{"method":"turn/started","params":{"threadId":"continued","turn":{"id":"live"}}}`), false)
			o.message([]byte(`{"id":2,"method":"item/tool/requestUserInput","params":{"threadId":"foreign"}}`), false)
			if tracker.state.SessionID != "startup" {
				t.Fatal("broadcast/server request claimed the pane")
			}
			response := `{"id":2,"result":{"turn":{"id":"live","status":"inProgress"}}}`
			if method == "turn/steer" {
				response = `{"id":2,"result":{"turnId":"live"}}`
			}
			o.message([]byte(response), false)
			if tracker.state.SessionID != "continued" || tracker.state.State != agents.Running || o.thread.CWD != "/project" {
				t.Fatal("continued CLI thread retained its startup identity", tracker.state)
			}
			o.message([]byte(`{"method":"turn/completed","params":{"threadId":"startup","turn":{"id":"old","status":"completed"}}}`), false)
			o.message([]byte(`{"method":"turn/completed","params":{"threadId":"continued","turn":{"id":"live","status":"completed"}}}`), false)
			if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
				t.Fatal("continued thread did not finish without an idle notification")
			}
			o.message([]byte(`{"method":"thread/status/changed","params":{"threadId":"continued","status":{"type":"idle"}}}`), false)
			if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
				t.Fatal("continued thread did not produce a completion", tracker.state)
			}
			failed, _ := json.Marshal(map[string]any{"id": 3, "method": method, "params": map[string]string{"threadId": "failed"}})
			o.message(failed, true)
			o.message([]byte(`{"id":3,"error":{"code":-1,"message":"rejected"}}`), false)
			if tracker.state.SessionID != "continued" || tracker.state.State != agents.Completed || len(o.pendingTurns) != 0 {
				t.Fatal("failed turn request replaced the pane owner")
			}
		})
	}
}

func TestCodexBridgeIgnoresTemporaryStructuredTasks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request string
		thread  string
	}{
		{"response metadata", `{}`, `{"id":"temporary","ephemeral":true}`},
		{"request metadata", `{"ephemeral":true}`, `{"id":"temporary"}`},
		{"subagent", `{}`, `{"id":"temporary","parentThreadId":"own"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tracker agentTracker
			var events []agents.HookInput
			o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
				events = append(events, h)
				tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
			}}
			message := func(client bool, raw string) { t.Helper(); o.message([]byte(raw), client) }
			message(true, `{"id":1,"method":"thread/start"}`)
			message(false, `{"id":1,"result":{"thread":{"id":"own","cwd":"/project"}}}`)
			message(false, `{"method":"turn/started","params":{"threadId":"own","turn":{"id":"live"}}}`)
			before := tracker.state
			count := len(events)
			message(true, `{"id":"temporary-structured","method":"thread/start","params":`+tc.request+`}`)
			message(false, `{"id":"temporary-structured","result":{"thread":`+tc.thread+`}}`)
			for _, method := range []string{"turn/start", "turn/steer"} {
				message(true, `{"id":"temporary-turn","method":"`+method+`","params":{"threadId":"temporary"}}`)
				message(false, `{"id":"temporary-turn","result":{"turn":{"id":"summary-turn"},"turnId":"summary-turn"}}`)
			}
			message(false, `{"method":"turn/started","params":{"threadId":"temporary","turn":{"id":"summary-turn"}}}`)
			message(false, `{"method":"item/completed","params":{"threadId":"temporary","turnId":"summary-turn","item":{"type":"agentMessage","text":"{\"summary\":\"旧对话的摘要。\"}"}}}`)
			message(false, `{"method":"turn/completed","params":{"threadId":"temporary","turn":{"id":"summary-turn","status":"completed"}}}`)
			message(false, `{"method":"thread/status/changed","params":{"threadId":"temporary","status":{"type":"idle"}}}`)
			if !tracker.state.Equal(before) || len(events) != count || o.thread.ID != "own" || len(o.pendingTurns) != 0 || len(o.pending) != 0 {
				t.Fatalf("temporary task changed pane state: %+v", tracker.state)
			}
			message(false, `{"method":"item/completed","params":{"threadId":"own","turnId":"live","item":{"type":"agentMessage","text":"真实任务已完成。"}}}`)
			message(false, `{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"live","status":"completed"}}}`)
			message(false, `{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`)
			if tracker.state.SessionID != "own" || tracker.state.CompletionRevision != 1 || tracker.state.Summary != "真实任务已完成。" {
				t.Fatalf("real completion lost after temporary task: %+v", tracker.state)
			}
		})
	}
}

func TestCodexMetadataWatchIgnoresTemporaryThreads(t *testing.T) {
	for _, thread := range []codexThread{
		{ID: "temporary", Ephemeral: true},
		{ID: "child", Parent: "own"},
	} {
		o := &codexObserver{emit: func(h agents.HookInput) { t.Fatalf("non-root snapshot emitted %+v", h) }}
		thread.Status = codexStatus{Type: "active"}
		o.snapshot(thread, codexTurn{ID: "summary-turn", Status: "inProgress"})
		thread.Status = codexStatus{Type: "idle"}
		o.snapshot(thread, codexTurn{ID: "summary-turn", Status: "completed"})
		if o.thread.ID != "" {
			t.Fatal("temporary snapshot bound pane")
		}
	}
}

func TestCodexMetadataWatchIgnoresHistoricalCompletion(t *testing.T) {
	var tracker agentTracker
	o := &codexObserver{emit: func(h agents.HookInput) { tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()}) }}
	thread := codexThread{ID: "existing", CWD: "/same", Status: codexStatus{Type: "idle"}}
	o.snapshot(thread, codexTurn{ID: "old", Status: "completed"})
	if tracker.state.State != agents.Ready || tracker.state.CompletionRevision != 0 {
		t.Fatal("history produced a fresh completion")
	}
	thread.Status = codexStatus{Type: "active", Flags: []string{"waitingOnApproval"}}
	o.snapshot(thread, codexTurn{ID: "new", Status: "inProgress"})
	if tracker.state.State != agents.Waiting || tracker.state.Reason != "permission" {
		t.Fatal("active existing task not observed")
	}
	// Metadata and turn history are separate reads. A completed old snapshot
	// must not finish the still-active thread, including a blocked Stop hook.
	o.snapshot(thread, codexTurn{ID: "new", Status: "completed"})
	if tracker.state.State != agents.Waiting || tracker.state.CompletionRevision != 0 {
		t.Fatal("completed history overrode active thread metadata")
	}
	thread.Status = codexStatus{Type: "idle"}
	o.snapshot(thread, codexTurn{ID: "new", Status: "completed"})
	o.snapshot(thread, codexTurn{ID: "new", Status: "completed"})
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatal("polled completion missing or duplicated")
	}
	// A delayed active status with the already completed turn cannot reopen it.
	thread.Status = codexStatus{Type: "active"}
	o.snapshot(thread, codexTurn{ID: "new", Status: "completed"})
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatal("delayed active metadata reopened a completed turn")
	}
	o.snapshot(thread, codexTurn{ID: "next", Status: "inProgress"})
	thread.Status = codexStatus{Type: "idle"}
	o.snapshot(thread, codexTurn{ID: "next", Status: "completed"})
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 2 {
		t.Fatal("new observed turn did not complete independently")
	}
}

func TestCodexBridgeUnknownCompletionKeepsObserving(t *testing.T) {
	var tracker agentTracker
	o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
		tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
	}}
	o.message([]byte(`{"id":1,"method":"thread/start"}`), true)
	o.message([]byte(`{"id":1,"result":{"thread":{"id":"own"}}}`), false)
	o.message([]byte(`{"method":"turn/started","params":{"threadId":"own","turn":{"id":"turn"}}}`), false)
	o.message([]byte(`{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"turn","status":"inProgress"}}}`), false)
	o.message([]byte(`{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"turn","status":"completed"}}}`), false)
	o.message([]byte(`{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`), false)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatal("unknown completion status disabled final lifecycle observation")
	}
}

func TestCodexBridgeContinuationHasIndependentTerminalEvents(t *testing.T) {
	var tracker agentTracker
	o := &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
		tracker.apply(AgentEvent{Agent: "codex", Input: h, At: time.Now()})
	}}
	message := func(value string) { o.message([]byte(value), false) }
	o.message([]byte(`{"id":1,"method":"thread/start"}`), true)
	message(`{"id":1,"result":{"thread":{"id":"own","status":{"type":"idle"}}}}`)
	message(`{"method":"turn/started","params":{"threadId":"own","turn":{"id":"first"}}}`)
	message(`{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"first","status":"completed"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 1 {
		t.Fatal("first turn did not settle immediately", tracker.state)
	}
	message(`{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active"}}}`)
	if tracker.state.State != agents.Completed {
		t.Fatal("delayed active status reopened the finished turn", tracker.state)
	}
	message(`{"method":"thread/status/changed","params":{"threadId":"foreign","status":{"type":"idle"}}}`)
	message(`{"method":"turn/started","params":{"threadId":"own","turn":{"id":"continuation"}}}`)
	message(`{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"first","status":"completed"}}}`)
	// Idle may arrive before the final completion message. It cannot release
	// the pending result from the previous turn.
	message(`{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`)
	if tracker.state.State != agents.Running || tracker.state.CompletionRevision != 1 {
		t.Fatal("intermediate/stale completion escaped during continuation", tracker.state)
	}
	message(`{"method":"turn/completed","params":{"threadId":"own","turn":{"id":"continuation","status":"completed"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 2 {
		t.Fatal("continuation waited for a separate idle event", tracker.state)
	}
	message(`{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"idle"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 2 {
		t.Fatal("final idle completion missing or repeated", tracker.state)
	}
	message(`{"method":"thread/status/changed","params":{"threadId":"own","status":{"type":"active"}}}`)
	if tracker.state.State != agents.Completed || tracker.state.CompletionRevision != 2 {
		t.Fatal("late thread metadata displaced the completed turn", tracker.state)
	}
}

func TestCodexWatchConfirmsIdleAfterReadingTurnHistory(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "retty-watch-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		reads := 0
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var request codexEnvelope
			if json.Unmarshal(raw, &request) != nil || len(request.ID) == 0 {
				continue
			}
			var result any = map[string]any{}
			switch request.Method {
			case "thread/read":
				reads++
				status := "active"
				if reads == 1 || reads >= 5 {
					status = "idle"
				}
				result = map[string]any{"thread": codexThread{ID: "own", Status: codexStatus{Type: status}}}
			case "thread/turns/list":
				turn := codexTurn{ID: "old", Status: "completed"}
				if reads == 3 {
					turn = codexTurn{ID: "new", Status: "inProgress"}
				} else if reads >= 5 {
					turn.ID = "new"
				}
				result = map[string]any{"data": []codexTurn{turn}}
			}
			response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result, "error": nil})
			if conn.Write(r.Context(), websocket.MessageText, response) != nil {
				return
			}
		}
	})}
	go server.Serve(ln)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []agents.HookInput
	o := &codexObserver{thread: codexThread{ID: "own"}, turn: "old", active: true, emit: func(h agents.HookInput) {
		events = append(events, h)
		if h.Event == "Stop" {
			cancel()
		}
	}}
	err = pollCodexThread(ctx, socket, "own", os.Getpid(), o)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("watch did not reach the confirmed final completion", err)
	}
	if len(events) != 2 || events[0].Event != "UserPromptSubmit" || events[0].TurnID != "new" || events[1].Event != "Stop" || events[1].TurnID != "new" {
		t.Fatal("old idle metadata announced completion before new work", events)
	}
}
