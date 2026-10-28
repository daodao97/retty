//go:build darwin || linux

package rex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"retty/internal/agents"
	"retty/internal/approval"
)

func codexSocket() (string, error) {
	home, err := agents.ConfigDir("codex")
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "app-server-control", "app-server-control.sock"), nil
}

func dialCodex(ctx context.Context, socket string) (*websocket.Conn, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	c, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if c != nil {
		c.SetReadLimit(-1)
	}
	return c, err
}

// StartCodexBridge never restarts a daemon. The private endpoint is consumed
// by the launcher, while pane capabilities remain only in process memory.
func StartCodexBridge(launcher string, parent int, exe string) error {
	if parent <= 0 || osPaneContextMissing() {
		return errors.New("missing pane context")
	}
	socket, err := codexSocket()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	c, err := dialCodex(ctx, socket)
	cancel()
	if err != nil {
		// A normal CLI also starts the daemon when one does not exist.
		ctx, cancel = context.WithTimeout(context.Background(), 8*time.Second)
		start := exec.CommandContext(ctx, exe, "app-server", "daemon", "start")
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "RETTY_") {
				start.Env = append(start.Env, kv)
			}
		}
		err = start.Run()
		cancel()
		if err != nil {
			return err
		}
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		c, err = dialCodex(ctx, socket)
		cancel()
		if err != nil {
			return err
		}
	}
	c.CloseNow()
	dir, err := os.MkdirTemp("", "retty-codex-")
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	cmd := exec.Command(self, "-codex-proxy", dir, strconv.Itoa(parent), socket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return err
	}
	go cmd.Wait()
	ready := filepath.Join(dir, "ready")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return os.WriteFile(filepath.Join(launcher, "endpoint"), []byte("unix://"+filepath.Join(dir, "bridge.sock")), 0o600)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd.Process.Kill()
	os.RemoveAll(dir)
	return errors.New("Codex notification relay did not start")
}

func osPaneContextMissing() bool {
	return os.Getenv("RETTY_SESSION") == "" || os.Getenv("RETTY_AGENT_TOKEN") == "" || os.Getenv("RETTY_AGENT_SOCKET") == ""
}

// RunCodexProxy relays the CLI transport and arbitrates structured approval
// responses. Pending requests stay in memory; transcripts and streaming deltas
// are never retained. The shared Codex daemon is never restarted.
func RunCodexProxy(dir string, parent int, socket string) error {
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "bridge.sock"))
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !processAlive(parent) {
					cancel()
					ln.Close()
					return
				}
			}
		}
	}()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		back, err := dialCodex(ctx, socket)
		if err != nil {
			http.Error(w, "Codex daemon unavailable", http.StatusServiceUnavailable)
			return
		}
		defer back.CloseNow()
		front, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer front.CloseNow()
		front.SetReadLimit(-1)
		connection, end := context.WithCancel(ctx)
		defer end()
		observer := newCodexObserver(connection)
		go observer.reconcileMetadata(connection, socket)
		store := approval.NewStore()
		if closeApprovals, err := approval.Listen(connection, Dir(), os.Getenv("RETTY_SESSION"), store); err == nil {
			defer closeApprovals()
		}
		approvals := newCodexApprovals(os.Getenv("RETTY_SESSION"), store, func(raw []byte) error {
			writeCtx, cancel := context.WithTimeout(connection, 3*time.Second)
			defer cancel()
			return back.Write(writeCtx, websocket.MessageText, raw)
		})
		defer approvals.close()
		copy := func(from, to *websocket.Conn, client bool) {
			defer end()
			for {
				kind, raw, err := from.Read(connection)
				if err != nil {
					return
				}
				// Register requests before the daemon can respond; observe responses
				// before forwarding so subsequent CLI turns have an established owner.
				if kind == websocket.MessageText {
					observer.message(raw, client)
					if client {
						if approvals.send(raw) != nil {
							return
						}
						continue
					}
					observer.mu.Lock()
					owner := observer.thread.ID
					observer.mu.Unlock()
					approvals.receive(raw, owner)
				}
				if to.Write(connection, kind, raw) != nil {
					return
				}
			}
		}
		go copy(front, back, true)
		copy(back, front, false)
	})}
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
		return err
	}
	err = server.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

type codexEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

type codexThread struct {
	ID        string      `json:"id"`
	CWD       string      `json:"cwd"`
	Parent    string      `json:"parentThreadId"`
	Ephemeral bool        `json:"ephemeral"`
	Status    codexStatus `json:"status"`
}

func (t codexThread) persistentRoot() bool {
	return t.ID != "" && t.Parent == "" && !t.Ephemeral
}

type codexStatus struct {
	Type  string   `json:"type"`
	Flags []string `json:"activeFlags"`
}

type codexTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type codexObserver struct {
	mu             sync.Mutex
	revision       uint64 // guards metadata reads against newer transport events
	pending        map[string]codexThreadRequest
	pendingTurns   map[string]codexTurnRequest
	ignoredThreads []string // recent temporary/subagent threads on this CLI connection
	thread         codexThread
	turn           string
	active         bool
	wait           string
	finished       string // final turn status awaiting an idle thread confirmation
	summary        string // bounded final answer for this turn only
	emit           func(agents.HookInput)
}

type codexTurnRequest struct {
	ThreadID string `json:"threadId"`
	CWD      string `json:"cwd"`
}

type codexThreadRequest struct {
	Method    string
	Ephemeral bool `json:"ephemeral"`
}

func newCodexObserver(ctx context.Context) *codexObserver {
	events := make(chan AgentEvent, 256)
	configDir := agentConfigDir("codex")
	sid, token, socket := os.Getenv("RETTY_SESSION"), os.Getenv("RETTY_AGENT_TOKEN"), os.Getenv("RETTY_AGENT_SOCKET")
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-events:
				_ = ReportAgent(socket, sid, token, event)
			}
		}
	}()
	return &codexObserver{pending: map[string]codexThreadRequest{}, emit: func(h agents.HookInput) {
		h.ConfigDir = configDir
		select {
		case events <- AgentEvent{Agent: "codex", Input: h, At: time.Now()}:
		default: // Notification reporting cannot hold up terminal operations.
		}
	}}
}

func (o *codexObserver) event(name, tool string) {
	if o.thread.ID == "" {
		return
	}
	h := agents.HookInput{Event: name, SessionID: o.thread.ID, CWD: o.thread.CWD, TurnID: o.turn, Tool: tool, Source: agents.CodexLifecycleSource}
	if name == "UserPromptSubmit" || name == "SessionStart" {
		o.summary = ""
	}
	if name == "Stop" || name == "StopFailure" {
		h.Summary = o.summary
	}
	o.emit(h)
}

func (o *codexObserver) message(raw []byte, client bool) {
	var message codexEnvelope
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.revision++
	if client {
		switch message.Method {
		case "thread/start", "thread/resume", "thread/fork":
			if len(message.ID) > 0 && len(o.pending) < 64 {
				var request codexThreadRequest
				if len(message.Params) > 0 && json.Unmarshal(message.Params, &request) != nil {
					return
				}
				request.Method = message.Method
				o.pending[string(message.ID)] = request
			}
		case "turn/start", "turn/steer":
			var request codexTurnRequest
			if len(message.ID) > 0 && json.Unmarshal(message.Params, &request) == nil && request.ThreadID != "" && len(o.pendingTurns) < 64 {
				if o.pendingTurns == nil {
					o.pendingTurns = map[string]codexTurnRequest{}
				}
				o.pendingTurns[string(message.ID)] = request
			}
		}
		return
	}
	// A CLI can continue a loaded conversation without first resuming it.
	// Only its successful turn request can establish that ownership; broadcast
	// daemon notifications and failed requests must never claim another pane.
	if request, ok := o.pendingTurns[string(message.ID)]; message.Method == "" && len(message.ID) > 0 && ok {
		delete(o.pendingTurns, string(message.ID))
		if slices.Contains(o.ignoredThreads, request.ThreadID) {
			return
		}
		var result struct {
			Turn   codexTurn `json:"turn"`
			TurnID string    `json:"turnId"`
		}
		if json.Unmarshal(message.Result, &result) != nil {
			return
		}
		turn := result.Turn.ID
		if turn == "" {
			turn = result.TurnID
		}
		if turn == "" {
			return
		}
		if o.thread.ID != request.ThreadID {
			cwd := request.CWD
			if cwd == "" {
				cwd = o.thread.CWD
			}
			o.thread = codexThread{ID: request.ThreadID, CWD: cwd}
			o.turn, o.active, o.wait, o.finished = "", false, "", ""
			o.emit(agents.HookInput{Event: "SessionStart", SessionID: request.ThreadID, CWD: cwd, Source: "resume"})
		}
		if !o.active || o.turn != turn {
			o.thread.Status = codexStatus{Type: "active"}
			o.beginTurn(turn)
		}
		return
	}
	if request, ok := o.pending[string(message.ID)]; message.Method == "" && len(message.ID) > 0 && ok {
		delete(o.pending, string(message.ID))
		var result struct {
			Thread codexThread `json:"thread"`
		}
		if json.Unmarshal(message.Result, &result) == nil && result.Thread.ID != "" {
			if request.Ephemeral || !result.Thread.persistentRoot() {
				// The TUI generates summaries and other structured output on
				// temporary threads through this same connection. They cannot
				// replace the pane owner, even via a successful turn/start.
				if !slices.Contains(o.ignoredThreads, result.Thread.ID) {
					if len(o.ignoredThreads) >= 256 {
						o.ignoredThreads = o.ignoredThreads[1:]
					}
					o.ignoredThreads = append(o.ignoredThreads, result.Thread.ID)
				}
				return
			}
			o.thread, o.turn, o.active, o.wait = result.Thread, "", false, ""
			o.summary = ""
			o.finished = ""
			h := agents.HookInput{Event: "SessionStart", SessionID: o.thread.ID, CWD: o.thread.CWD, Source: "startup"}
			if request.Method == "thread/resume" {
				h.Source = "resume"
			}
			o.emit(h)
			if result.Thread.Status.Type == "active" {
				o.beginTurn("")
				o.waiting(result.Thread.Status)
			}
		}
		return
	}
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Phase string `json:"phase"`
		} `json:"item"`
		Turn   codexTurn   `json:"turn"`
		Status codexStatus `json:"status"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" || params.ThreadID != o.thread.ID {
		return
	}
	switch message.Method {
	case "item/completed":
		if o.active && params.TurnID != "" && params.TurnID == o.turn && params.Item.Type == "agentMessage" && (params.Item.Phase == "" || params.Item.Phase == "final_answer") {
			o.summary = agents.SummaryText(params.Item.Text)
		}
	case "turn/started":
		if params.Turn.ID == "" || (o.active && o.turn == params.Turn.ID) {
			return
		}
		o.thread.Status = codexStatus{Type: "active"}
		o.beginTurn(params.Turn.ID)
	case "turn/completed":
		if params.Turn.ID == "" || (o.turn != "" && params.Turn.ID != o.turn) || !o.active {
			return
		}
		if params.Turn.Status != "completed" && params.Turn.Status != "interrupted" && params.Turn.Status != "failed" {
			return
		}
		o.turn = params.Turn.ID
		// The app-server's terminal turn event is authoritative. Waiting for
		// a second idle notification can strand the pane in running forever.
		o.finishTurn(params.Turn.Status)
	case "thread/status/changed":
		o.thread.Status = params.Status
		// Only a turn start (or a fresh metadata read) starts work. A delayed
		// active notification must not reopen an already completed turn.
		o.waiting(params.Status)
		o.finishIfIdle()
	}
}

// beginTurn starts tracking a new turn, possibly not yet identified, and
// reports it as the start of new work.
func (o *codexObserver) beginTurn(turn string) {
	o.turn, o.active, o.wait, o.finished = turn, true, "", ""
	o.event("UserPromptSubmit", "")
}

func (o *codexObserver) finishIfIdle() {
	if !o.active || o.finished == "" || o.thread.Status.Type != "idle" {
		return
	}
	o.finishTurn(o.finished)
}

func (o *codexObserver) finishTurn(status string) {
	event := ""
	switch status {
	case "completed":
		event = "Stop"
	case "interrupted":
		event = "Interrupt"
	case "failed":
		event = "StopFailure"
	}
	if event != "" {
		o.active, o.wait, o.finished = false, "", ""
		o.event(event, "")
	}
}

func (o *codexObserver) waiting(status codexStatus) {
	if !o.active || status.Type != "active" {
		return
	}
	wait := ""
	for _, flag := range status.Flags {
		if flag == "waitingOnApproval" {
			wait = "permission"
			break
		}
		if flag == "waitingOnUserInput" {
			wait = "question"
		}
	}
	if wait == o.wait {
		return
	}
	// Clear the bridge's previous waiting key before changing its reason.
	if o.wait != "" {
		o.event("PostToolUse", "codex_bridge")
		o.event("PostToolUse", "request_user_input")
	}
	o.wait = wait
	if wait == "permission" {
		o.event("PermissionRequest", "codex_bridge")
	}
	if wait == "question" {
		o.event("PreToolUse", "request_user_input")
	}
}

// ParseBridgePID is used only by the early CLI entry point.
func ParseBridgePID(value string) (int, error) {
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid Codex launcher PID")
	}
	return pid, nil
}
