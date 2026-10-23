// Package desktopcontrol is a same-user, local GUI automation endpoint. It is
// independent of the session daemon: closing it cannot end shells or agents.
package desktopcontrol

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"retty/internal/browsercontrol"
	"retty/internal/rex"
)

type Action struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Shortcut string `json:"shortcut,omitempty"`
	Allowed  bool   `json:"allowed"`
}
type Request struct {
	Browser   *browsercontrol.Request `json:"browser,omitempty"`
	Op        string                  `json:"op"`
	SID       string                  `json:"sid,omitempty"`
	Action    string                  `json:"action,omitempty"`
	Direction string                  `json:"direction,omitempty"`
	Create    *rex.CreateOptions      `json:"create,omitempty"`
	Deadline  time.Time               `json:"deadline,omitzero"`
}
type Result struct {
	Browser *browsercontrol.Result `json:"browser,omitempty"`
	SID     string                 `json:"sid,omitempty"`
	Actions []Action               `json:"actions,omitempty"`
}
type response struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}
type Handler func(context.Context, Request) (Result, error)

func Path(dir string) string {
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	p := filepath.Join(dir, "desktop-control.sock")
	if len(p) < 100 {
		return p
	}
	h := sha256.Sum256([]byte(p))
	return filepath.Join(os.TempDir(), fmt.Sprintf("retty-ui-%d-%x.sock", os.Getuid(), h[:8]))
}

type Server struct {
	ln     *net.UnixListener
	lock   *os.File
	once   sync.Once
	cancel context.CancelFunc
}

func Listen(dir string, handler Handler) (*Server, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "desktop-control.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another desktop owns automation in this data directory")
	}
	path := Path(dir)
	if in, err := os.Lstat(path); err == nil {
		if in.Mode()&os.ModeSocket == 0 {
			lock.Close()
			return nil, errors.New("desktop endpoint path is not a socket")
		}
		if err = os.Remove(path); err != nil {
			lock.Close()
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		lock.Close()
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		ln.Close()
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, lock: lock, cancel: cancel}
	go s.serve(ctx, handler)
	return s, nil
}
func (s *Server) Close() error {
	var err error
	s.once.Do(func() { s.cancel(); err = s.ln.Close(); s.lock.Close() })
	return err
}

const maxMessage = 1 << 20

func (s *Server) serve(ctx context.Context, handler Handler) {
	slots := make(chan struct{}, 16)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			var req Request
			err := decode(conn, &req)
			budget := 4 * time.Second
			if req.Browser != nil && req.Op == "browser" && req.Browser.Validate() == nil {
				budget = req.Browser.Budget()
			}
			reqCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			conn.SetDeadline(time.Now().Add(budget + time.Second))
			var res Result
			if err == nil {
				if !req.Deadline.IsZero() {
					deadlineCtx, deadlineCancel := context.WithDeadline(reqCtx, req.Deadline)
					defer deadlineCancel()
					reqCtx = deadlineCtx
				}
				// The caller closes on cancellation. Cancel a queued UI mutation
				// immediately rather than letting it run after a shorter deadline.
				go func() { var b [1]byte; conn.Read(b[:]); cancel() }()
				res, err = handler(reqCtx, req)
			}
			r := response{Result: res}
			if err != nil {
				r.Error = err.Error()
			}
			json.NewEncoder(conn).Encode(r)
		}()
	}
}
func decode(r io.Reader, v any) error {
	return decodeLimit(r, v, maxMessage)
}
func decodeLimit(r io.Reader, v any, limit int) error {
	// One JSON line per independent connection; no unbounded read or trailing
	// values. A newline permits handlers to answer without waiting for EOF.
	b := bufio.NewReader(io.LimitReader(r, int64(limit+1)))
	line, err := b.ReadBytes('\n')
	if err != nil {
		return err
	}
	if len(line) > limit {
		return errors.New("desktop control message exceeds size limit")
	}
	return json.Unmarshal(line, v)
}
func Call(ctx context.Context, dir string, req Request) (Result, error) {
	budget := 5 * time.Second
	if req.Browser != nil && req.Op == "browser" {
		if err := req.Browser.Validate(); err != nil {
			return Result{}, err
		}
		budget = req.Browser.Budget() + time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		req.Deadline = deadline
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", Path(dir))
	if err != nil {
		return Result{}, fmt.Errorf("desktop GUI unavailable in %s: %w", dir, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		return Result{}, err
	}
	var res response
	if err = decodeLimit(conn, &res, 16<<20); err != nil {
		return Result{}, err
	}
	if res.Error != "" {
		return Result{}, errors.New(res.Error)
	}
	return res.Result, nil
}
