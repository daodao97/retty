// Package rex is Retty's session server and its client.
//
// The server owns the pseudo-terminals: shells keep running when the app
// quits, and the next launch attaches to them again, with what they printed
// replayed. The app talks to it over a Unix socket: a control connection of
// JSON lines (requests and their responses), and one connection per
// attached session that carries the terminal's bytes both ways.
package rex

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"retty/internal/runtimeinfo"
	"time"
)

// ProtocolVersion identifies the server schema. Additive JSON fields can
// remain compatible with older servers; incompatible changes raise the minimum.
const ProtocolVersion = 6

// Size leases are negotiated with additive attach/response fields. Legacy
// peers keep their version 6 behavior without requiring a service restart.
const sizeLockLease = 30 * time.Second
const sizeLockRenewInterval = 5 * time.Second

// Version 6 adds the mobile size lock: Attach.Owner, "lockedResize",
// "unlockSize" and "resync" requests, and SessionInfo.SizeLock. Older clients keep resizing as before; a locked
// session ignores their resize requests until it is unlocked.
// Version 5 adds CompletionRevision to the version 4 protocol. Requests,
// streaming, layout and agent events are unchanged; clients can fall back
// to observed state/input changes for version 4 completion notifications.
const MinProtocolVersion = 4

func CompatibleProtocol(version int) bool {
	return version >= MinProtocolVersion && version <= ProtocolVersion
}

// Request is a line of the control connection, from the client.
type Request struct {
	ID         int64           `json:"id"`
	Op         string          `json:"op"`
	SID        string          `json:"sid,omitempty"`
	Cols       int             `json:"cols,omitempty"`
	Rows       int             `json:"rows,omitempty"`
	Create     *CreateOptions  `json:"create,omitempty"`
	Layout     json.RawMessage `json:"layout,omitempty"`
	AgentEvent *AgentEvent     `json:"agentEvent,omitempty"`
	Token      string          `json:"token,omitempty"`
	Device     *DeviceInfo     `json:"device,omitempty"`
	// Owner identifies the device of a size lock request.
	Owner string `json:"owner,omitempty"`
	// Retry explicitly retries a previously failed Agent restore.
	Retry bool `json:"retry,omitempty"`
	// Notice identifies the exact lifecycle event acknowledged by a viewer.
	Notice string `json:"notice,omitempty"`
	// ETag, on "list", names the list the client already has.
	ETag string `json:"etag,omitempty"`
	// HistoryRows bounds an independent history snapshot request.
	HistoryRows int `json:"history_rows,omitempty"`
	// Input is explicit automation input, independent of attachment and size.
	Input  *InputRequest  `json:"input,omitempty"`
	Status *ProgramStatus `json:"programStatus,omitempty"`
}

// InputRequest writes once. An optional output revision rejects stale reads.
type InputRequest struct {
	Data     []byte  `json:"data"`
	IfOutput *uint64 `json:"ifOutput,omitempty"`
}

// Response answers the Request with the same ID.
type Response struct {
	ID    int64           `json:"id"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	// ETag names a "list" answer. When it equals the request's, Data is
	// left out: remote clients poll every few seconds over a relay, and
	// sessions are mostly unchanged. Older servers ignore the request's
	// ETag and always send the list, and older clients never send one, so
	// this needs no new protocol version, as with size leases.
	ETag string `json:"etag,omitempty"`
}

// CreateOptions start a session.
type CreateOptions struct {
	resumed bool // server-owned; cannot be requested through JSON
	// Command runs instead of the user's login shell.
	Command []string `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Rows    int      `json:"rows,omitempty"`
}

// SessionInfo describes a session and what runs in it now.
type SessionInfo struct {
	HookExecutable string    `json:"hookExecutable,omitempty"`
	GitTurn        string    `json:"gitTurn,omitempty"`
	ID             string    `json:"id"`
	PID            int       `json:"pid"`
	Shell          string    `json:"shell"`
	Created        time.Time `json:"created"`
	// Program is the name of the program in the foreground, and Args its
	// arguments; Idle tells that it is the shell itself, at its prompt.
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
	Idle    bool     `json:"idle"`
	// Dir is the working directory of the program in the foreground.
	Dir string `json:"dir"`
	// Title is the last title a program set (OSC 0, OSC 2).
	Title      string    `json:"title,omitempty"`
	LastOutput time.Time `json:"lastOutput"`
	LastInput  time.Time `json:"lastInput"`
	Output     uint64    `json:"output"`
	Bells      int       `json:"bells"`
	Exited     bool      `json:"exited"`
	ExitCode   int       `json:"exitCode"`
	Attached   int       `json:"attached"`
	Cols       int       `json:"cols"`
	Rows       int       `json:"rows"`
	// SizeLock names the device holding the terminal size ("" when the
	// desktop's windows set it, as before version 6).
	SizeLock string `json:"sizeLock,omitempty"`
	// SizeLockDevice is the name of that device, for the windows.
	SizeLockDevice string     `json:"sizeLockDevice,omitempty"`
	Agent          AgentState `json:"agent,omitempty"`
	// ProgramStatuses preserves all OSC 7501 records across viewer reconnects.
	// Agent contains their effective status for existing UI and push clients.
	ProgramStatuses []ProgramStatus `json:"programStatuses,omitempty"`
	Resumed         bool            `json:"resumed,omitempty"`
	AttentionSeen   string          `json:"attentionSeen,omitempty"`
}

// RestoreResult is empty for a pane without a recoverable Agent. Errors retain
// the original pane ID and conversation on disk until an explicit retry/close.
type RestoreResult struct {
	Session *SessionInfo `json:"session,omitempty"`
	Agent   string       `json:"agent,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// Hello is the server's answer to "hello".
type Hello struct {
	Build   runtimeinfo.Info `json:"build,omitzero"`
	Version int              `json:"version"`
	PID     int              `json:"pid"`
	Started time.Time        `json:"started"`
	Host    HostInfo         `json:"host"`
	// Exe is the server's executable, and ExeTime when it was modified
	// last as the server started: a development build compares them with
	// its own, to replace a server of an older build.
	Exe            string    `json:"exe,omitempty"`
	ExeTime        time.Time `json:"exeTime,omitzero"`
	AgentRecovery  bool      `json:"agentRecovery,omitempty"`
	Attention      bool      `json:"attention,omitempty"`
	ScreenSync     bool      `json:"screen_sync,omitempty"`
	ScreenGraphics bool      `json:"screen_graphics,omitempty"`
	TurnDiff       bool      `json:"turn_diff,omitempty"`
	SessionControl bool      `json:"session_control,omitempty"`
}

// HostInfo describes the machine the server runs on.
type HostInfo struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Chip   string `json:"chip"`
	Memory uint64 `json:"memory"`
	OS     string `json:"os"`
	User   string `json:"user"`
	Home   string `json:"home"`
}

// DeviceInfo identifies the connected mobile client for the desktop UI.
type DeviceInfo struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name"`
	OS       string            `json:"os,omitempty"`
	Push     *PushRegistration `json:"push,omitempty"`
	Activity *DesktopActivity  `json:"activity,omitempty"`
}

// DesktopActivity is a short-lived notification presence lease. Session
// servers can ignore it; the authenticated desktop bridge forwards it to push.
type DesktopActivity struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	Active         bool   `json:"active"`
	RoutingVersion int    `json:"routingVersion,omitempty"`
	Present        bool   `json:"present,omitempty"`
	ViewedDesktop  string `json:"viewedDesktop,omitempty"`
	ViewedSession  string `json:"viewedSession,omitempty"`
	HideWaiting    bool   `json:"hideWaiting,omitempty"`
	HideCompletion bool   `json:"hideCompletion,omitempty"`
}

// PushRegistration travels only through an authenticated mobile control connection.
// Tokens are private and must never be rendered, logged or returned in Hello.
type PushRegistration struct {
	ID       string   `json:"id"`
	Token    string   `json:"token,omitempty"`
	Disabled bool     `json:"disabled,omitempty"`
	Receipts []string `json:"receipts,omitempty"`
}

// Attach is the first line of a connection attaching to a session.
type Attach struct {
	Op   string `json:"op"` // "attach"
	SID  string `json:"sid"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	// ScreenFrames asks for output framed with the authoritative grid size.
	// Older servers ignore this field and continue to send raw ANSI.
	ScreenFrames bool `json:"screen_frames,omitempty"`
	// ScreenSync negotiates acknowledged viewport states instead of output
	// bytes, including framed input/acks. Ignored by older servers.
	ScreenSync bool `json:"screen_sync,omitempty"`
	// ScreenGraphics negotiates bounded Kitty pixels in restored snapshots.
	ScreenGraphics bool `json:"screen_graphics,omitempty"`
	// InputOnly suppresses live screen updates on a separate ownership stream.
	InputOnly bool `json:"input_only,omitempty"`
	// Owner, with version 6, takes the session's size lock for a device
	// named Device, at Cols×Rows, before the snapshot is made.
	Owner  string `json:"owner,omitempty"`
	Device string `json:"device,omitempty"`
	// SizeLease requests an expiring lock renewed by "renewSize" requests.
	// A client renews only when the attach response acknowledges this field.
	SizeLease bool `json:"size_lease,omitempty"`
}

// Dir returns the directory of the server's socket, log and state.
func Dir() string {
	if d := os.Getenv("RETTY_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "Retty")
}

// SocketPath returns the path of the server's socket: in Dir, unless that
// is too long a path for a socket, then in the temporary directory.
func SocketPath() string {
	return SocketPathForDir(Dir())
}

// SocketPathForDir resolves a namespace without changing process environment.
func SocketPathForDir(dir string) string {
	p := filepath.Join(dir, "server.sock")
	if len(p) < 100 {
		return p
	}
	h := fnv.New32a()
	h.Write([]byte(p))
	return filepath.Join(os.TempDir(), fmt.Sprintf("retty-%d-%x.sock", os.Getuid(), h.Sum32()))
}
