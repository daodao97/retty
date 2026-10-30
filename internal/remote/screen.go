package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"retty/internal/rex"
)

// ScreenHost lets paired devices view and operate this Mac's screen through
// the retty-screen helper. A nil host, or one whose Allowed reports false,
// refuses every viewer.
type ScreenHost struct {
	// Helper is the path of the retty-screen executable.
	Helper string
	// Allowed reports whether the user turned screen sharing on.
	Allowed func() bool
	// OnViewers reports how many devices watch the screen, for the desktop
	// to show that it is seen. It must not block.
	OnViewers func(int)
	// Locked reports whether the Mac's screen is locked; nil never reports.
	Locked func() bool
	// Unlock types password at the lock screen through Screen Sharing; nil
	// refuses. Its error is the viewer's to read.
	Unlock func(ctx context.Context, password string) error
}

// Screen frames are [uint32 big-endian length][kind][payload], the length
// covering kind and payload.
const (
	ScreenFrameJPEG   byte = 1
	ScreenFrameStatus byte = 2
	// ScreenFrameLock is "1" while the screen is locked, "0" once it is not.
	ScreenFrameLock byte = 3
)

const (
	maxScreenViewers = 2
	maxScreenFrame   = 16 << 20
	maxScreenEvent   = 4096
)

type screenRequest struct {
	Op       string `json:"op"`
	MaxWidth int    `json:"maxWidth,omitempty"`
	Quality  int    `json:"quality,omitempty"`
	FPS      int    `json:"fps,omitempty"`
}

func clampInt(v, low, high, fallback int) int {
	if v == 0 {
		return fallback
	}
	return min(max(v, low), high)
}

func (b *Bridge) serveScreen(conn net.Conn, reader *bufio.Reader, line []byte) {
	encoder := json.NewEncoder(conn)
	fail := func(message string) { _ = encoder.Encode(rex.Response{Error: message}) }
	var req screenRequest
	if len(line) > 1024 || json.Unmarshal(line, &req) != nil {
		fail("无效的远程桌面请求")
		return
	}
	host := b.screen
	if host == nil || host.Helper == "" {
		fail("此电脑的 Retty 不支持远程桌面")
		return
	}
	if host.Allowed == nil || !host.Allowed() {
		fail("此电脑未开启远程桌面，请在电脑上的 Retty 设置中开启")
		return
	}
	ctx, cancel := context.WithCancel(b.imageContext)
	defer cancel()
	id, ok := b.claimViewer(host, cancel)
	if !ok {
		fail("远程桌面观看人数已满")
		return
	}
	defer b.releaseViewer(host, id)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	args := []string{"capture",
		strconv.Itoa(clampInt(req.MaxWidth, 320, 2560, 1280)),
		strconv.Itoa(clampInt(req.Quality, 30, 90, 60)),
		strconv.Itoa(clampInt(req.FPS, 1, 30, 15))}

	var wmu sync.Mutex
	send := func(kind byte, payload []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
		return writeScreenFrame(conn, kind, payload)
	}
	// lockedNow is the last reading of the lock screen, for input and for
	// waiting out the lock, which must not ask the system each time.
	var lockedNow atomic.Bool
	locked := func() bool {
		l := host.Locked != nil && host.Locked()
		lockedNow.Store(l)
		return l
	}
	answered := false
	// answer accepts the view, once: on its first frame, so a viewer who
	// cannot see learns why at once, or straight away at a lock screen,
	// which has no frame to give until the user unlocks the Mac.
	answer := func() bool {
		if answered {
			return true
		}
		if encoder.Encode(rex.Response{}) != nil {
			return false
		}
		answered = true
		conn.SetDeadline(time.Time{})
		go b.forwardScreenInput(ctx, cancel, host, reader, lockedNow.Load, func(status string) { send(ScreenFrameStatus, []byte(status)) })
		if host.Locked != nil {
			// The lock screen's state goes out as it changes, so the viewer
			// knows the Mac waits for its user.
			go func() {
				tick := time.NewTicker(time.Second)
				defer tick.Stop()
				for last := byte(0); ; {
					state := byte('0')
					if host.Locked() {
						state = '1'
					}
					lockedNow.Store(state == '1')
					if state != last {
						if send(ScreenFrameLock, []byte{state}) != nil {
							return
						}
						last = state
					}
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
					}
				}
			}()
		}
		return true
	}
	for {
		if locked() {
			// A locked Mac gives capture no display; the view waits at the
			// lock screen, as the poller reads it, and starts again once the
			// user unlocks it.
			if !answer() || !waitScreenUnlock(ctx, lockedNow.Load) {
				return
			}
		}
		gone, failure := captureScreen(ctx, host.Helper, args, answer, send)
		if gone {
			return
		}
		// Locking ends a capture, sometimes before the lock is reported.
		if locked() || (sleepScreen(ctx, time.Second) && locked()) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !answered {
			fail(failure)
		} else {
			send(ScreenFrameStatus, []byte(failure))
		}
		return
	}
}

// captureScreen runs one capture helper, sending its frames until it stops.
// gone tells that the viewer left or the view ended; otherwise failure says
// why the picture stopped. answer accepts the view on the first frame.
func captureScreen(ctx context.Context, helper string, args []string, answer func() bool, send func(byte, []byte) error) (gone bool, failure string) {
	capture := exec.CommandContext(ctx, helper, args...)
	var stderr bytes.Buffer
	capture.Stderr = &limitedWriter{w: &stderr, n: 4096}
	out, err := capture.StdoutPipe()
	if err != nil {
		return false, "无法启动屏幕采集"
	}
	// The helper captures while its stdin stays open, and ends with it, so
	// it never outlives the bridge.
	in, err := capture.StdinPipe()
	if err != nil {
		return false, "无法启动屏幕采集"
	}
	// A helper that ignores its closed stdin is ended rather than waited for.
	capture.WaitDelay = 2 * time.Second
	if err = capture.Start(); err != nil {
		return false, "无法启动屏幕采集"
	}
	// Its stderr is complete, and safe to read, only once it is waited for.
	waited := false
	end := func() {
		if !waited {
			in.Close()
			capture.Wait()
			waited = true
		}
	}
	defer end()

	// The first frame proves the permission and the display; without it the
	// viewer learns why at once instead of watching a blank screen.
	first := make(chan []byte, 1)
	latest := make(chan []byte, 1)
	go func() {
		defer close(latest)
		for i := 0; ; i++ {
			frame, err := readScreenFrame(out)
			if err != nil {
				if i == 0 {
					close(first)
				}
				return
			}
			if i == 0 {
				first <- frame
				continue
			}
			// Only the newest frame waits: a slow viewer skips frames rather
			// than falling behind the screen.
			select {
			case <-latest:
			default:
			}
			latest <- frame
		}
	}()
	var frame []byte
	select {
	case frame = <-first:
	case <-time.After(8 * time.Second):
	case <-ctx.Done():
		return true, ""
	}
	if frame == nil {
		capture.Process.Kill()
		end()
		return false, screenFailure(stderr.String(), "屏幕采集没有画面")
	}
	if !answer() {
		return true, ""
	}
	for err = send(ScreenFrameJPEG, frame); err == nil; err = send(ScreenFrameJPEG, frame) {
		var ok bool
		select {
		case frame, ok = <-latest:
		case <-ctx.Done():
			// A still screen sends no frames; the viewer leaving must end
			// the view anyway.
			return true, ""
		}
		if !ok {
			end()
			return false, screenFailure(stderr.String(), "屏幕采集已停止")
		}
	}
	return true, ""
}

// waitScreenUnlock waits until locked says the Mac is unlocked, or the view
// ends.
func waitScreenUnlock(ctx context.Context, locked func() bool) bool {
	for locked() {
		if !sleepScreen(ctx, time.Second) {
			return false
		}
	}
	return true
}

// sleepScreen waits d, or less if the view ends first, and tells which.
func sleepScreen(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// forwardScreenInput passes the viewer's events, checked one by one, to an
// input helper started with the view. The viewer closing its side ends the
// whole view.
func (b *Bridge) forwardScreenInput(ctx context.Context, cancel context.CancelFunc, host *ScreenHost, reader *bufio.Reader, locked func() bool, status func(string)) {
	defer cancel()
	var input *exec.Cmd
	var stdin io.WriteCloser
	var stderr bytes.Buffer
	defer func() {
		if stdin != nil {
			stdin.Close()
			input.Wait()
		}
	}()
	// The helper starts with the view, not with the first event, so the
	// first tap is not kept waiting for a process to start.
	start := func() bool {
		input = exec.CommandContext(ctx, host.Helper, "input")
		input.Stderr = &limitedWriter{w: &stderr, n: 4096}
		var err error
		if stdin, err = input.StdinPipe(); err == nil {
			err = input.Start()
		}
		if err != nil {
			input, stdin = nil, nil
			status("无法启动远程操作")
			return false
		}
		return true
	}
	start()
	lines := bufio.NewScanner(reader)
	lines.Buffer(make([]byte, 0, 1024), maxScreenEvent)
	for lines.Scan() {
		event := lines.Text()
		if !ValidScreenEvent(event) {
			continue
		}
		// The lock screen is for the user at the Mac; input from here waits
		// for no one and is dropped.
		if locked() {
			continue
		}
		if input == nil && !start() {
			continue
		}
		if _, err := io.WriteString(stdin, event+"\n"); err != nil {
			// The helper quit, as it does without accessibility access; the
			// next event starts it again once the user allows it.
			stdin.Close()
			input.Wait()
			status(screenFailure(stderr.String(), "远程操作已停止"))
			input, stdin = nil, nil
			stderr.Reset()
		}
	}
}

// ValidScreenEvent reports whether line is an input event retty-screen
// accepts: an operation and numbers in range, or base64 text.
func ValidScreenEvent(line string) bool {
	f := strings.Split(line, " ")
	unit := func(s string) bool {
		v, err := strconv.ParseFloat(s, 64)
		return err == nil && v >= 0 && v <= 1
	}
	integer := func(s string, low, high int64) bool {
		v, err := strconv.ParseInt(s, 10, 64)
		return err == nil && v >= low && v <= high
	}
	switch {
	case len(f) == 3 && f[0] == "move":
		return unit(f[1]) && unit(f[2])
	case len(f) == 4 && (f[0] == "down" || f[0] == "up" || f[0] == "drag"):
		return unit(f[1]) && unit(f[2]) && (f[3] == "0" || f[3] == "1")
	case len(f) == 5 && f[0] == "click":
		return unit(f[1]) && unit(f[2]) && (f[3] == "0" || f[3] == "1") && integer(f[4], 1, 3)
	case len(f) == 3 && f[0] == "scroll":
		return integer(f[1], -10000, 10000) && integer(f[2], -10000, 10000)
	case len(f) == 2 && f[0] == "text":
		return f[1] != "" && len(f[1]) < maxScreenEvent && strings.Trim(f[1], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=") == ""
	case len(f) == 3 && f[0] == "key":
		return integer(f[1], 0, 127) && integer(f[2], 0, 1<<24)
	}
	return false
}

// screenFailure turns the helper's message into one for the viewer.
func screenFailure(stderr, fallback string) string {
	switch {
	case strings.Contains(stderr, "screen recording permission"):
		return "电脑上的 Retty 没有屏幕录制权限，请在电脑的系统设置 → 隐私与安全性中允许"
	case strings.Contains(stderr, "accessibility permission"):
		return "电脑上的 Retty 没有辅助功能权限，只能查看，无法操作"
	}
	// Any other reason is the helper's own words, which tell more than a
	// generic message; only its first line, as it names no secrets.
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); line != "" {
		if len(line) > 200 {
			line = line[:200]
		}
		return fallback + "：" + line
	}
	return fallback
}

// StopScreen ends every view of the screen at once, as when the user stops
// sharing it from the desktop.
func (b *Bridge) StopScreen() {
	b.mu.Lock()
	stops := make([]context.CancelFunc, 0, len(b.screenStops))
	for _, stop := range b.screenStops {
		stops = append(stops, stop)
	}
	b.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

// claimViewer counts a viewer, which stop ends, keeping the Mac awake while
// any watches.
func (b *Bridge) claimViewer(host *ScreenHost, stop context.CancelFunc) (uint64, bool) {
	b.mu.Lock()
	if len(b.screenStops) >= maxScreenViewers {
		b.mu.Unlock()
		return 0, false
	}
	if b.screenStops == nil {
		b.screenStops = make(map[uint64]context.CancelFunc)
	}
	b.screenNext++
	id := b.screenNext
	b.screenStops[id] = stop
	n := len(b.screenStops)
	if n == 1 {
		// caffeinate ends with this process if it exits without cleanup.
		awake := exec.Command("caffeinate", "-di", "-w", strconv.Itoa(os.Getpid()))
		if awake.Start() == nil {
			b.screenAwake = awake
		}
	}
	b.mu.Unlock()
	if host.OnViewers != nil {
		host.OnViewers(n)
	}
	return id, true
}

func (b *Bridge) releaseViewer(host *ScreenHost, id uint64) {
	b.mu.Lock()
	delete(b.screenStops, id)
	n := len(b.screenStops)
	var awake *exec.Cmd
	if n == 0 {
		awake, b.screenAwake = b.screenAwake, nil
	}
	b.mu.Unlock()
	if awake != nil {
		awake.Process.Kill()
		go awake.Wait()
	}
	if host.OnViewers != nil {
		host.OnViewers(n)
	}
}

func readScreenFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxScreenFrame {
		return nil, fmt.Errorf("screen frame of %d bytes", n)
	}
	frame := make([]byte, n)
	_, err := io.ReadFull(r, frame)
	return frame, err
}

func writeScreenFrame(w io.Writer, kind byte, payload []byte) error {
	var head [5]byte
	binary.BigEndian.PutUint32(head[:4], uint32(len(payload)+1))
	head[4] = kind
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		keep := p[:min(len(p), l.n)]
		l.n -= len(keep)
		l.w.Write(keep)
	}
	return len(p), nil
}

// ScreenStream is a viewer's side of a remote screen.
type ScreenStream struct {
	conn   net.Conn
	reader *bufio.Reader
	wmu    sync.Mutex
}

// DialScreen asks the bridge for its screen. maxWidth, quality and fps may
// be zero for the bridge's defaults.
func DialScreen(ctx context.Context, client *rex.Client, maxWidth, quality, fps int) (*ScreenStream, error) {
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err = json.NewEncoder(conn).Encode(screenRequest{Op: "screen-view", MaxWidth: maxWidth, Quality: quality, FPS: fps}); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(conn, 256<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	var res rex.Response
	if err = json.Unmarshal(line, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		if strings.Contains(res.Error, "unknown op") || strings.Contains(res.Error, "screen-view") {
			return nil, errors.New("电脑上的 Retty 版本不支持远程桌面，请先更新")
		}
		return nil, errors.New(res.Error)
	}
	conn.SetDeadline(time.Time{})
	success = true
	return &ScreenStream{conn: conn, reader: reader}, nil
}

// Next returns the next frame: a JPEG, or a status message from the host.
func (s *ScreenStream) Next() (kind byte, payload []byte, err error) {
	frame, err := readScreenFrame(s.reader)
	if err != nil {
		return 0, nil, err
	}
	return frame[0], frame[1:], nil
}

// Send passes one input event, in retty-screen's format, to the host.
func (s *ScreenStream) Send(event string) error {
	if !ValidScreenEvent(event) {
		return fmt.Errorf("invalid screen event %q", event)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := io.WriteString(s.conn, event+"\n")
	return err
}

func (s *ScreenStream) Close() error { return s.conn.Close() }

type screenUnlockRequest struct {
	Op       string `json:"op"`
	Password string `json:"password"`
}

// serveScreenUnlock types a viewer's password at the lock screen, one
// request at a time. The bridge keeps nothing of the password.
func (b *Bridge) serveScreenUnlock(conn net.Conn, line []byte) {
	encoder := json.NewEncoder(conn)
	fail := func(message string) { _ = encoder.Encode(rex.Response{Error: message}) }
	var req screenUnlockRequest
	if len(line) > 1024 || json.Unmarshal(line, &req) != nil {
		fail("无效的解锁请求")
		return
	}
	host := b.screen
	if host == nil || host.Unlock == nil {
		fail("此电脑的 Retty 不支持远程解锁")
		return
	}
	if host.Allowed == nil || !host.Allowed() {
		fail("此电脑未开启远程桌面，请在电脑上的 Retty 设置中开启")
		return
	}
	if !b.screenUnlocking.CompareAndSwap(false, true) {
		fail("正在解锁，请稍候")
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, 45*time.Second)
	defer cancel()
	conn.SetDeadline(time.Now().Add(50 * time.Second))
	err := host.Unlock(ctx, req.Password)
	// The next unlock may follow as soon as the viewer hears this one's end.
	b.screenUnlocking.Store(false)
	if err != nil {
		fail(err.Error())
		return
	}
	_ = encoder.Encode(rex.Response{})
}

// UnlockScreen asks the bridge to unlock its Mac's screen with password.
func UnlockScreen(ctx context.Context, client *rex.Client, password string) error {
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(55 * time.Second))
	if err = json.NewEncoder(conn).Encode(screenUnlockRequest{Op: "screen-unlock", Password: password}); err != nil {
		return err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 4096)).ReadBytes('\n')
	if err != nil {
		return err
	}
	var res rex.Response
	if err = json.Unmarshal(line, &res); err != nil {
		return err
	}
	if res.Error != "" {
		if strings.Contains(res.Error, "unknown op") {
			return errors.New("电脑上的 Retty 版本不支持远程解锁，请先更新")
		}
		return errors.New(res.Error)
	}
	return nil
}
