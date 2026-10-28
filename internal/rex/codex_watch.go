//go:build darwin || linux

package rex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/coder/websocket"
	"retty/internal/agents"
)

// WatchCodexThread attaches metadata-only observation to an already running
// CLI. It neither resumes the thread nor changes its instructions/approvals.
// The caller supplies the verified pane capability and exact thread ID.
func WatchCodexThread(thread string, parent int) error {
	if thread == "" || parent <= 0 || osPaneContextMissing() {
		return errors.New("missing Codex pane context")
	}
	socket, err := codexSocket()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := newCodexObserver(ctx)
	// An attached watcher must preserve an existing completion badge, and
	// may repair a running pane that missed its terminal event before attach.
	if state, ok := codexPaneState(os.Getenv("RETTY_AGENT_SOCKET"), os.Getenv("RETTY_SESSION"), thread); ok {
		observer.thread = codexThread{ID: thread, CWD: state.CWD}
		observer.active = state.State == agents.Running || state.State == agents.Waiting
	}
	for processAlive(parent) {
		if err := pollCodexThread(ctx, socket, thread, parent, observer); err != nil {
			time.Sleep(time.Second)
		}
	}
	return nil
}

func codexPaneState(socket, pane, thread string) (AgentState, bool) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return AgentState{}, false
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	client := NewClient(conn, nil)
	defer client.Close()
	sessions, err := client.List()
	if err != nil {
		return AgentState{}, false
	}
	for _, s := range sessions {
		if s.ID == pane && !s.Exited && !s.Idle && s.Agent.ID == "codex" && s.Agent.SessionID == thread {
			return s.Agent, true
		}
	}
	return AgentState{}, false
}

func pollCodexThread(ctx context.Context, socket, thread string, parent int, observer *codexObserver) error {
	client, err := newCodexMetadataClient(ctx, socket)
	if err != nil {
		return err
	}
	defer client.conn.CloseNow()
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for processAlive(parent) {
		metadata, turn, err := client.snapshot(ctx, thread)
		if err != nil {
			return err
		}
		observer.snapshot(metadata, turn)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

type codexMetadataClient struct {
	conn     *websocket.Conn
	sequence int
}

func newCodexMetadataClient(ctx context.Context, socket string) (*codexMetadataClient, error) {
	connect, cancel := context.WithTimeout(ctx, 3*time.Second)
	c, err := dialCodex(connect, socket)
	cancel()
	if err != nil {
		return nil, err
	}
	client := &codexMetadataClient{conn: c}
	if err := client.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "retty_status_observer", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		c.CloseNow()
		return nil, err
	}
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"method":"initialized"}`)); err != nil {
		c.CloseNow()
		return nil, err
	}
	return client, nil
}

func (c *codexMetadataClient) call(ctx context.Context, method string, params any, result any) error {
	c.sequence++
	request, _ := json.Marshal(map[string]any{"id": c.sequence, "method": method, "params": params})
	deadline, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	if err := c.conn.Write(deadline, websocket.MessageText, request); err != nil {
		return err
	}
	for {
		_, raw, err := c.conn.Read(deadline)
		if err != nil {
			return err
		}
		var response struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Method != "" || response.ID != c.sequence {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return errors.New("Codex metadata unavailable")
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

func (c *codexMetadataClient) snapshot(ctx context.Context, thread string) (codexThread, codexTurn, error) {
	var metadata struct {
		Thread codexThread `json:"thread"`
	}
	if err := c.call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, &metadata); err != nil {
		return codexThread{}, codexTurn{}, err
	}
	var turns struct {
		Data []codexTurn `json:"data"`
	}
	if err := c.call(ctx, "thread/turns/list", map[string]any{"threadId": thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded"}, &turns); err != nil {
		return codexThread{}, codexTurn{}, err
	}
	// Confirm status after reading turn history. An earlier idle record
	// must not finish a turn that started between these separate reads.
	if err := c.call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, &metadata); err != nil {
		return codexThread{}, codexTurn{}, err
	}
	if metadata.Thread.ID != thread || !metadata.Thread.persistentRoot() {
		return codexThread{}, codexTurn{}, errors.New("not the requested root thread")
	}
	var turn codexTurn
	if len(turns.Data) > 0 {
		turn = turns.Data[0]
	}
	return metadata.Thread, turn, nil
}

// Reconcile over a separate, read-only connection. Missing notifications must
// not strand a pane, and reconnecting must never resume or subscribe a thread.
func (o *codexObserver) reconcileMetadata(ctx context.Context, socket string) {
	for ctx.Err() == nil {
		_ = o.pollMetadata(ctx, socket)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (o *codexObserver) pollMetadata(ctx context.Context, socket string) error {
	client, err := newCodexMetadataClient(ctx, socket)
	if err != nil {
		return err
	}
	defer client.conn.CloseNow()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := o.reconcileSnapshot(ctx, client); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (o *codexObserver) reconcileSnapshot(ctx context.Context, client *codexMetadataClient) error {
	o.mu.Lock()
	id, revision := o.thread.ID, o.revision
	o.mu.Unlock()
	if id == "" {
		return nil
	}
	thread, turn, err := client.snapshot(ctx, id)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revision == revision && o.thread.ID == id {
		o.snapshotLocked(thread, turn)
	}
	return nil
}

func (o *codexObserver) snapshot(thread codexThread, turn codexTurn) {
	if !thread.persistentRoot() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshotLocked(thread, turn)
}

func (o *codexObserver) snapshotLocked(thread codexThread, turn codexTurn) {
	o.revision++
	if o.thread.ID == "" {
		o.thread = thread
		o.emit(agents.HookInput{Event: "SessionStart", SessionID: thread.ID, CWD: thread.CWD, Source: "resume"})
	}
	if thread.ID != o.thread.ID {
		return
	}
	o.thread.Status = thread.Status
	if thread.Status.Type == "active" && !o.active && turn.Status != "inProgress" && turn.ID != o.turn {
		// A new active turn may not have reached history yet. Clear the old
		// completion badge, without attributing the old turn to this new work.
		o.beginTurn("")
	}
	if thread.Status.Type == "active" && turn.ID != "" && turn.Status == "inProgress" && (!o.active || o.turn != turn.ID) {
		o.beginTurn(turn.ID)
	}
	o.waiting(thread.Status)
	// These two records are fetched separately. An active thread paired with
	// a completed turn can be an old result or a continuation still running.
	if o.active && o.turn == "" && thread.Status.Type == "idle" {
		o.turn = turn.ID
	}
	if o.active && turn.ID != "" && turn.ID == o.turn {
		switch turn.Status {
		case "completed", "failed", "interrupted":
			o.finished = turn.Status
		}
		o.finishIfIdle()
	}
}
