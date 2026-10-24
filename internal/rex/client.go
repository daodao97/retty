package rex

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Client is a control connection to the server.
type Client struct {
	conn net.Conn
	dial func(context.Context) (net.Conn, error)

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan Response
	err     error
	closed  chan struct{}

	// The last list the server sent, which it then need not repeat.
	listETag string
	listed   []SessionInfo
}

// Connect connects to the server, starting one in the background when
// none runs.
func Connect() (*Client, error) {
	socket := SocketPath()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		if err := Spawn(); err != nil {
			return nil, fmt.Errorf("rex: starting the server: %w", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			time.Sleep(30 * time.Millisecond)
			conn, err = net.Dial("unix", socket)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("rex: connecting to the server: %w", err)
			}
		}
	}
	// Pin every later stream/extension to this server, even if a fixture or
	// caller changes the process-wide data-directory environment afterward.
	return NewClient(conn, func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "unix", socket) }), nil
}

func localDial(ctx context.Context) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath())
}

// ConnectDial uses the same transport for control requests and session streams.
func ConnectDial(ctx context.Context, dial func(context.Context) (net.Conn, error)) (*Client, error) {
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	return NewClient(conn, dial), nil
}

// Redial opens a new control connection over the existing transport, including
// after this control connection has closed. It never restarts the session
// server or retries terminal input. The caller still owns the old client.
func (c *Client) Redial(ctx context.Context) (*Client, error) {
	if c.dial == nil {
		return nil, errors.New("rex: transport cannot reopen a control connection")
	}
	return ConnectDial(ctx, c.dial)
}

// DialExtension opens an independent authenticated transport for a bounded
// bridge request. It never attaches a PTY or changes its dimensions.
func (c *Client) DialExtension(ctx context.Context) (net.Conn, error) {
	if c.dial == nil {
		return nil, errors.New("independent transport unavailable")
	}
	return c.dial(ctx)
}

// NewClient takes ownership of conn; dial opens independent session streams.
func NewClient(conn net.Conn, dial func(context.Context) (net.Conn, error)) *Client {
	c := &Client{conn: conn, dial: dial, pending: map[int64]chan Response{}, closed: make(chan struct{})}
	go c.read()
	return c
}

// Executable returns the path of the running executable and when it was
// modified last.
func Executable() (string, time.Time) {
	exe, err := os.Executable()
	if err != nil {
		return "", time.Time{}
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	st, err := os.Stat(exe)
	if err != nil {
		return exe, time.Time{}
	}
	return exe, st.ModTime()
}

// Restart shuts the server down, ending its sessions, waits for it to
// exit, and connects to a new one.
func (c *Client) Restart(pid int) (*Client, error) {
	c.Shutdown()
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for pid > 0 && processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return Connect()
}

// Closed is closed once the connection to the server is lost.
func (c *Client) Closed() <-chan struct{} { return c.closed }

func (c *Client) read() {
	// Control replies are normally small. Large replies, including history
	// and session lists, are assembled without imposing a message-size limit.
	r := bufio.NewReaderSize(c.conn, 64<<10)
	var scratch []byte
	for {
		line, storage, err := readControlLine(r, scratch)
		scratch = storage
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				ch <- Response{ID: id, Error: "connection to the server lost"}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			close(c.closed)
			return
		}
		var res Response
		decodeErr := json.Unmarshal(line, &res)
		// Unmarshal owns Response strings and RawMessage data. The receive
		// buffer can be reused while the caller decodes the delivered reply.
		if cap(scratch) > 256<<10 {
			scratch = nil
		}
		if decodeErr != nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[res.ID]
		delete(c.pending, res.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}
}

// ReadSlice avoids copying ordinary replies. Large lines assemble into one
// reusable buffer instead of allocating a copy of every reader-sized fragment.
func readControlLine(r *bufio.Reader, scratch []byte) (line, storage []byte, err error) {
	line, err = r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, scratch, err
	}
	scratch = append(scratch[:0], line...)
	for err == bufio.ErrBufferFull {
		line, err = r.ReadSlice('\n')
		scratch = append(scratch, line...)
	}
	return scratch, scratch, err
}

// call sends a request and decodes the response's data into out.
func (c *Client) exchange(req Request) (Response, error) {
	ch := make(chan Response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return Response{}, errors.New("rex: connection to the server lost")
	}
	c.nextID++
	req.ID = c.nextID
	c.pending[req.ID] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(req)
	c.wmu.Lock()
	_, err := c.conn.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return Response{}, err
	}
	var res Response
	select {
	case res = <-ch:
	case <-time.After(15 * time.Second):
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return Response{}, fmt.Errorf("rex: %s timed out: %w", req.Op, context.DeadlineExceeded)
	}
	if res.Error != "" {
		return Response{}, errors.New(res.Error)
	}
	return res, nil
}

func (c *Client) call(req Request, out any) error {
	res, err := c.exchange(req)
	if err == nil && out != nil && len(res.Data) > 0 {
		err = json.Unmarshal(res.Data, out)
	}
	return err
}

func (c *Client) Hello() (Hello, error) { return c.hello(nil) }

func (c *Client) HelloFrom(device DeviceInfo) (Hello, error) { return c.hello(&device) }

func (c *Client) hello(device *DeviceInfo) (h Hello, err error) {
	err = c.call(Request{Op: "hello", Device: device}, &h)
	if err == nil && h.Version == 4 && h.Host.ID == "" {
		h.Host.ID = legacyHostID(h.Host)
	}
	return
}

func (c *Client) List() ([]SessionInfo, error) { return c.list(nil) }

func (c *Client) ListFrom(device DeviceInfo) ([]SessionInfo, error) { return c.list(&device) }

// list sends the ETag of the last list, and reuses that list when the
// server answers that it is unchanged. Callers get their own copy.
func (c *Client) list(device *DeviceInfo) ([]SessionInfo, error) {
	c.mu.Lock()
	etag, listed := c.listETag, c.listed
	c.mu.Unlock()
	res, err := c.exchange(Request{Op: "list", Device: device, ETag: etag})
	if err != nil {
		return nil, err
	}
	if etag == "" || res.ETag != etag || len(res.Data) > 0 {
		listed = nil
		if len(res.Data) > 0 {
			if err := json.Unmarshal(res.Data, &listed); err != nil {
				return nil, err
			}
		}
		c.mu.Lock()
		c.listETag, c.listed = res.ETag, listed
		c.mu.Unlock()
	}
	return slices.Clone(listed), nil
}

func (c *Client) Create(o CreateOptions) (s SessionInfo, err error) {
	err = c.call(Request{Op: "create", Create: &o}, &s)
	return
}

func (c *Client) Kill(sid string) error { return c.call(Request{Op: "kill", SID: sid}, nil) }

// MarkAttention acknowledges one exact result. Call only when Hello.Attention
// is advertised; it never sends input or changes a size lease.
func (c *Client) MarkAttention(sid, notice string) error {
	return c.call(Request{Op: "markAttention", SID: sid, Notice: notice}, nil)
}

// RestoreAgent reattaches a live session or resumes its exact saved conversation.
// Call only when Hello.AgentRecovery is advertised. It never replays input.
func (c *Client) RestoreAgent(sid string, cols, rows int, retry bool) (r RestoreResult, err error) {
	err = c.call(Request{Op: "restoreAgent", SID: sid, Cols: cols, Rows: rows, Retry: retry}, &r)
	return
}

func (c *Client) Resize(sid string, cols, rows int) error {
	return c.call(Request{Op: "resize", SID: sid, Cols: cols, Rows: rows}, nil)
}

// ResendSize makes the next resize reach the server even at the size it
// was told last: a window taking a session's size back after another
// device held it.
func (s *Stream) ResendSize() {
	s.mu.Lock()
	s.sentCols, s.sentRows = 0, 0
	s.mu.Unlock()
}

// HoldsLock tells that a LockStream attached and holds the size lock, as
// far as its own resizes know, and since when.
func (s *Stream) HoldsLock() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked && !s.lockLost, s.lockedAt
}

// LockedResize resizes a session whose size owner holds; it fails once
// the lock was released.
func (c *Client) LockedResize(sid, owner string, cols, rows int) error {
	return c.call(Request{Op: "lockedResize", SID: sid, Owner: owner, Cols: cols, Rows: rows}, nil)
}

// UnlockSize gives the size back to the windows, at cols×rows if given.
func (c *Client) UnlockSize(sid string, cols, rows int) error {
	return c.call(Request{Op: "unlockSize", SID: sid, Cols: cols, Rows: rows}, nil)
}

// releaseSize also checks ownership for earlier version 6 servers, which
// ignore Owner on unlockSize. Each mobile attachment uses a distinct owner.
func (c *Client) releaseSize(sid, owner string) error {
	sessions, err := c.List()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID == sid && session.SizeLock == owner {
			return c.call(Request{Op: "unlockSize", SID: sid, Owner: owner}, nil)
		}
	}
	return nil
}

// Resync sends the session's attached clients its screen again, as after
// a resize: a window that changed how it decodes the session redraws.
func (c *Client) Resync(sid string) error {
	return c.call(Request{Op: "resync", SID: sid}, nil)
}

func (c *Client) Clear(sid string) error {
	return c.call(Request{Op: "clear", SID: sid}, nil)
}

func (c *Client) Layout() (json.RawMessage, error) {
	var l json.RawMessage
	err := c.call(Request{Op: "getLayout"}, &l)
	return l, err
}

func (c *Client) SetLayout(l json.RawMessage) error {
	return c.call(Request{Op: "setLayout", Layout: l}, nil)
}

func (c *Client) Shutdown() error { return c.call(Request{Op: "shutdown"}, nil) }

func (c *Client) Close() error { return c.conn.Close() }

// Stream is a session attached to, for a terminal's Conn: it attaches
// once it knows the size of the screen, from the first Resize, or after
// a moment at the size it had last.
type Stream struct {
	// preserveSize keeps this viewer's local layout from resizing the PTY.
	preserveSize bool
	// lockOwner, for a LockStream, takes the session's size as it attaches
	// and resizes it as the device's view changes; lockLost tells that a
	// window released the lock, after which the stream resizes nothing.
	lockOwner, lockDevice           string
	locked, lockLost                bool
	lockedAt                        time.Time
	leaseCancel                     context.CancelFunc
	beforeAttach                    <-chan struct{}
	releaseDone                     chan struct{}
	screenFrames                    bool
	wantScreenSync, inputOnly       bool
	screenSync                      bool
	stateSequence, stateAck         uint64 // sole reader; ack after consumption
	stateData                       []byte
	statePending                    []byte // compatibility ReadScreen chunks
	stateEffects                    []byte
	stateCols, stateRows            int
	stateConfirmed                  time.Time // guarded by mu
	stateRevision                   uint64    // guarded by mu
	stateWriteMu                    sync.Mutex
	frameLeft, frameCols, frameRows int
	c                               *Client
	sid                             string
	attachMu                        sync.Mutex // one attach at a time
	syncMu                          sync.Mutex // preserve the order of resize requests
	mu                              sync.Mutex
	cond                            *sync.Cond
	conn                            net.Conn
	err                             error
	closed                          bool
	readAttachStarted               bool // guarded by mu; one read-triggered viewer attach
	// cols and rows are the terminal's size; sentCols and sentRows the
	// size the server was told last.
	cols, rows         int
	sentCols, sentRows int
	resizeTimer        *time.Timer
	resizeAt           time.Time

	// OnData, when set, is called with the size of what the session
	// prints, from the reading goroutine.
	OnData func(n int)
}

// Stream returns a stream of the session sid, which attaches lazily.
func (c *Client) Stream(sid string, cols, rows int) *Stream {
	return c.stream(sid, cols, rows, false)
}

// ViewStream attaches an independent viewer without changing the session's
// PTY size. ReadScreen supplies the source grid with each output chunk on
// supporting servers; the viewer scales that grid instead of reflowing ANSI.
func (c *Client) ViewStream(sid string) *Stream {
	return c.stream(sid, 0, 0, true)
}

// LockStream attaches a device that takes the session's size: the PTY
// follows this device's view until the size lock is released, and windows
// show the session at that size meanwhile. Version 6 servers only.
// cols×rows, when known, is the size it takes as it attaches.
func (c *Client) LockStream(sid, owner, device string, cols, rows int) *Stream {
	return c.LockStreamAfter(sid, owner, device, cols, rows, nil)
}

// LockStreamAfter waits for the previous attachment's release before taking
// ownership, including on earlier version 6 servers with unconditional unlock.
func (c *Client) LockStreamAfter(sid, owner, device string, cols, rows int, released <-chan struct{}) *Stream {
	s := c.stream(sid, cols, rows, false)
	s.mu.Lock()
	s.lockOwner, s.lockDevice = owner, device
	s.beforeAttach = released
	s.mu.Unlock()
	return s
}

func (c *Client) stream(sid string, cols, rows int, preserveSize bool) *Stream {
	s := &Stream{c: c, sid: sid, cols: cols, rows: rows, preserveSize: preserveSize}
	s.cond = sync.NewCond(&s.mu)
	time.AfterFunc(400*time.Millisecond, func() {
		s.attach()
		s.sync()
	})
	return s
}

// attach attaches to the session at the terminal's size, once.
func (s *Stream) attach() {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	s.mu.Lock()
	before := s.beforeAttach
	s.mu.Unlock()
	if before != nil {
		select {
		case <-before:
		case <-s.c.Closed():
			s.mu.Lock()
			s.err = io.ErrClosedPipe
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
	}
	s.mu.Lock()
	if s.conn != nil || s.err != nil || s.closed {
		s.mu.Unlock()
		return
	}
	cols, rows := s.cols, s.rows
	owner, device := s.lockOwner, s.lockDevice
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dial := s.c.dial
	if dial == nil {
		dial = localDial
	}
	conn, err := dial(ctx)
	var frames bool
	var leased bool
	var stateSync bool
	if err == nil {
		conn.SetDeadline(time.Now().Add(15 * time.Second))
		// Every stream asks for frames: a window decoding a locked
		// session at the lock's size follows their geometry.
		// The lock is taken with the attach, so its snapshot already has
		// the device's size.
		s.mu.Lock()
		wantSync, inputOnly := s.wantScreenSync, s.inputOnly
		s.mu.Unlock()
		b, _ := json.Marshal(Attach{Op: "attach", SID: s.sid, Cols: cols, Rows: rows, ScreenFrames: true, ScreenSync: wantSync, ScreenGraphics: !inputOnly, InputOnly: inputOnly, Owner: owner, Device: device, SizeLease: owner != ""})
		if _, err = conn.Write(append(b, '\n')); err == nil {
			var answer attachResponse
			answer, err = readAttachOK(conn)
			frames, leased = answer.ScreenFrames, answer.SizeLease && owner != ""
			stateSync = answer.ScreenSync && frames && wantSync
		}
		conn.SetDeadline(time.Time{})
		if err != nil {
			conn.Close()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if err == nil {
			conn.Close()
		}
		return
	}
	if err != nil {
		s.err = err
	} else {
		s.conn, s.sentCols, s.sentRows, s.screenFrames = conn, cols, rows, frames
		s.screenSync = stateSync
		s.locked, s.lockedAt = owner != "", time.Now()
		if leased {
			ctx, cancel := context.WithCancel(context.Background())
			s.leaseCancel = cancel
			go s.renewSizeLock(ctx, sizeLockRenewInterval)
		}
	}
	s.cond.Broadcast()
}

// Only an attached, active view renews. Closing the stream stops renewal even
// if the explicit unlock cannot arrive before iOS suspends the application.
func (s *Stream) renewSizeLock(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.c.Closed():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		closed, lost, owner := s.closed, s.lockLost, s.lockOwner
		s.mu.Unlock()
		if closed || lost || ctx.Err() != nil {
			return
		}
		if err := s.c.call(Request{Op: "renewSize", SID: s.sid, Owner: owner}, nil); err != nil && err.Error() == errSizeUnlocked.Error() {
			s.mu.Lock()
			s.lockLost = true
			s.mu.Unlock()
			return
		}
	}
}

// sync tells the server the terminal's size, once attached, when it was
// told another.
func (s *Stream) sync() error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.mu.Lock()
	if s.closed || s.preserveSize || s.lockLost || s.conn == nil || s.cols == s.sentCols && s.rows == s.sentRows {
		s.mu.Unlock()
		return nil
	}
	cols, rows := s.cols, s.rows
	owner, locked := s.lockOwner, s.locked
	s.mu.Unlock()
	if !locked {
		if err := s.c.Resize(s.sid, cols, rows); err != nil {
			return err
		}
	} else if err := s.c.LockedResize(s.sid, owner, cols, rows); err != nil {
		if err.Error() == errSizeUnlocked.Error() {
			s.mu.Lock()
			s.lockLost = true
			s.mu.Unlock()
		}
		return err
	}
	s.mu.Lock()
	s.sentCols, s.sentRows = cols, rows
	s.mu.Unlock()
	return nil
}

type attachResponse struct {
	ScreenFrames   bool   `json:"screen_frames,omitempty"`
	ScreenSync     bool   `json:"screen_sync,omitempty"`
	ScreenGraphics bool   `json:"screen_graphics,omitempty"`
	SizeLease      bool   `json:"size_lease,omitempty"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

// readAttachOK reads the server's answer a byte at a time, leaving the first
// screen frame untouched for the stream's reader.
func readAttachOK(conn net.Conn) (attachResponse, error) {
	var res attachResponse
	var line []byte
	b := make([]byte, 1)
	for {
		if _, err := conn.Read(b); err != nil {
			return res, err
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
		if len(line) > 4096 {
			return res, errors.New("rex: bad answer")
		}
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return res, err
	}
	if !res.OK {
		return res, errors.New(res.Error)
	}
	return res, nil
}

func (s *Stream) wait() (net.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A read-only viewer has no initial layout to collect. Start on demand
	// instead of waiting for the fallback timer. Dial asynchronously so Close
	// can still release a blocked reader while the connection is in flight.
	if s.preserveSize && s.c != nil && !s.readAttachStarted && s.conn == nil && s.err == nil && !s.closed {
		s.readAttachStarted = true
		go s.attach()
	}
	for s.conn == nil && s.err == nil && !s.closed {
		s.cond.Wait()
	}
	if s.closed {
		return nil, io.EOF
	}
	return s.conn, s.err
}

// HasScreenSize reports whether this server supplies output geometry.
func (s *Stream) HasScreenSize() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screenFrames
}

func (s *Stream) Read(p []byte) (int, error) {
	n, _, _, err := s.ReadScreen(p)
	return n, err
}

// ReadSnapshot consumes the first complete screen frame on a fresh viewer.
// The caller is the stream's sole reader. Legacy servers return nil without
// consuming bytes and continue through ReadScreen. A failed partial transfer
// must never replace the viewer's last complete screen.
func (s *Stream) ReadSnapshot() ([]byte, int, int, error) {
	return s.readSnapshot(15 * time.Second)
}

func (s *Stream) readSnapshot(timeout time.Duration) ([]byte, int, int, error) {
	conn, err := s.wait()
	if err != nil {
		return nil, 0, 0, err
	}
	if !s.HasScreenSize() {
		return nil, 0, 0, nil
	}
	// A healthy control socket does not guarantee the viewer delivers its
	// snapshot. Bound the whole frame, then clear the deadline for idle live
	// sessions whose output may legitimately remain quiet indefinitely.
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, 0, 0, err
	}
	defer conn.SetReadDeadline(time.Time{})
	if s.syncEnabled() {
		return s.readScreenState(conn)
	}
	buf := make([]byte, 64<<10)
	n, cols, rows, err := s.ReadScreen(buf)
	if err != nil {
		return nil, 0, 0, err
	}
	data := make([]byte, n+s.frameLeft)
	copy(data, buf[:n])
	if _, err := io.ReadFull(conn, data[n:]); err != nil {
		return nil, 0, 0, err
	}
	s.frameLeft = 0
	return data, cols, rows, nil
}

// ReadScreen reads ANSI at its source size. Each frame carries geometry
// before its payload, so an in-flight resize cannot decode at another size.
// A legacy server returns zero geometry; callers use the session list size.
// Like Read, it must be called by a single reader.
func (s *Stream) ReadScreen(p []byte) (int, int, int, error) {
	if len(p) == 0 {
		return 0, 0, 0, nil
	}
	conn, err := s.wait()
	if err != nil {
		return 0, 0, 0, err
	}
	if s.syncEnabled() {
		return s.readStateBytes(conn, p)
	}
	cols, rows := 0, 0
	if s.HasScreenSize() {
		if s.frameLeft == 0 {
			var header [8]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return 0, 0, 0, err
			}
			length := binary.BigEndian.Uint32(header[:4])
			cols, rows = int(binary.BigEndian.Uint16(header[4:6])), int(binary.BigEndian.Uint16(header[6:8]))
			if length == 0 || length > 64<<20 || cols == 0 || rows == 0 {
				return 0, 0, 0, errors.New("rex: invalid screen frame")
			}
			s.frameLeft, s.frameCols, s.frameRows = int(length), cols, rows
		}
		cols, rows = s.frameCols, s.frameRows
		p = p[:min(len(p), s.frameLeft)]
	}
	n, err := conn.Read(p)
	if s.HasScreenSize() {
		s.frameLeft -= n
	}
	if n > 0 && s.OnData != nil {
		s.OnData(n)
	}
	return n, cols, rows, err
}

func (s *Stream) Write(p []byte) (int, error) {
	conn, err := s.wait()
	if err != nil {
		return 0, err
	}
	if s.syncEnabled() {
		written := 0
		for len(p) > 0 {
			n := min(len(p), 32<<10)
			if err := s.writeStateMessage(conn, screenInput, p[:n]); err != nil {
				return written, err
			}
			written += n
			p = p[n:]
		}
		return written, nil
	}
	return conn.Write(p)
}

// resizeDelay keeps window animations from making the shell redraw its
// prompt at every intermediate size, while the window is already drawing
// at the next one.
const resizeDelay = 75 * time.Millisecond

// Resize attaches at the first size and coalesces subsequent changes.
func (s *Stream) Resize(cols, rows int) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return io.ErrClosedPipe
	}
	if s.preserveSize {
		s.mu.Unlock()
		s.attach()
		return nil
	}
	cols, rows = max(cols, 1), max(rows, 1)
	attached := s.conn != nil
	if attached && s.cols == cols && s.rows == rows {
		s.mu.Unlock()
		return nil
	}
	s.cols, s.rows = cols, rows
	s.resizeAt = time.Now()
	if attached {
		if s.resizeTimer == nil {
			s.resizeTimer = time.AfterFunc(resizeDelay, s.resizeSettled)
		} else {
			s.resizeTimer.Reset(resizeDelay)
		}
	}
	s.mu.Unlock()
	if attached {
		return nil
	}
	s.attach()
	return s.sync()
}

func (s *Stream) resizeSettled() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if remaining := resizeDelay - time.Since(s.resizeAt); remaining > 0 {
		s.resizeTimer.Reset(remaining)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.sync()
}

// Close detaches; the session goes on.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.closed = true
	if s.leaseCancel != nil {
		s.leaseCancel()
	}
	if s.resizeTimer != nil {
		s.resizeTimer.Stop()
	}
	conn := s.conn
	s.cond.Broadcast()
	s.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// CloseAndReleaseSize detaches immediately and releases only this attachment's
// lock off the UI thread. Wait for completion before closing its control client.
func (s *Stream) CloseAndReleaseSize() <-chan struct{} {
	s.Close()
	s.mu.Lock()
	if s.releaseDone != nil {
		done := s.releaseDone
		s.mu.Unlock()
		return done
	}
	done := make(chan struct{})
	s.releaseDone = done
	owner := s.lockOwner
	s.mu.Unlock()
	go func() {
		defer close(done)
		// An attach or resize already in flight must finish first, so it
		// cannot retake ownership after the release.
		s.attachMu.Lock()
		defer s.attachMu.Unlock()
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		if owner != "" {
			s.c.releaseSize(s.sid, owner)
		}
	}()
	return done
}

// Size returns the size the session was last given.
func (s *Stream) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}
