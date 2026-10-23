package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"retty/internal/rex"
)

type portRequest struct {
	Op   string `json:"op"`
	Port int    `json:"port"`
}

func (b *Bridge) servePort(conn net.Conn, reader *bufio.Reader, line []byte) {
	var req portRequest
	if len(line) > 1024 || json.Unmarshal(line, &req) != nil || req.Port < 1 || req.Port > 65535 {
		json.NewEncoder(conn).Encode(rex.Response{Error: "无效的开发端口"})
		return
	}
	b.mu.Lock()
	if b.portGate == nil {
		b.portGate = make(chan struct{}, 32)
	}
	gate := b.portGate
	b.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	default:
		json.NewEncoder(conn).Encode(rex.Response{Error: "开发端口连接繁忙"})
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, 5*time.Second)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(req.Port)))
	if err != nil && ctx.Err() == nil {
		upstream, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("::1", strconv.Itoa(req.Port)))
	}
	if err != nil {
		json.NewEncoder(conn).Encode(rex.Response{Error: fmt.Sprintf("无法连接远端回环端口 %d", req.Port)})
		return
	}
	defer upstream.Close()
	if err = json.NewEncoder(conn).Encode(rex.Response{}); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	stop := context.AfterFunc(b.imageContext, func() { upstream.Close(); conn.Close() })
	defer stop()
	tailcat.ProxyConns(&prefixedConn{Conn: conn, reader: reader}, upstream)
}

// DialPort opens a separate authenticated stream to a loopback TCP port on
// that bridge's host. It never sends a request to the PTY/session daemon.
func DialPort(ctx context.Context, client *rex.Client, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("port must be between 1 and 65535")
	}
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
	deadline := time.Now().Add(6 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(portRequest{Op: "port-forward", Port: port}); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(conn, 4096))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var res rex.Response
	if err = json.Unmarshal(line, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	// The bounded header reader may have prefetched application bytes.
	pending := make([]byte, reader.Buffered())
	io.ReadFull(reader, pending)
	conn.SetDeadline(time.Time{})
	success = true
	if len(pending) == 0 {
		return conn, nil
	}
	return &prefixedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(pending), conn)}, nil
}

// PortForward is an owned local listener and its active sockets. Closing it
// cancels the streams without affecting the bridge, daemon or any terminal.
type PortForward struct {
	listener net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]bool
	once     sync.Once
}

func ForwardPort(ctx context.Context, client *rex.Client, port int) (*PortForward, error) {
	// Verify the remote endpoint before publishing an apparently usable URL.
	probe, err := DialPort(ctx, client, port)
	if err != nil {
		return nil, err
	}
	probe.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &PortForward{listener: listener, ctx: ctx, cancel: cancel, conns: map[net.Conn]bool{}}
	go f.serve(client, port)
	go func() { <-ctx.Done(); f.Close() }()
	return f, nil
}
func (f *PortForward) Address() string { return f.listener.Addr().String() }
func (f *PortForward) Close() error {
	var err error
	f.once.Do(func() {
		f.cancel()
		err = f.listener.Close()
		f.mu.Lock()
		for conn := range f.conns {
			conn.Close()
		}
		f.mu.Unlock()
	})
	return err
}
func (f *PortForward) serve(client *rex.Client, port int) {
	slots := make(chan struct{}, 16)
	for {
		local, err := f.listener.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			local.Close()
			continue
		}
		f.mu.Lock()
		if f.ctx.Err() != nil {
			f.mu.Unlock()
			local.Close()
			<-slots
			continue
		}
		f.conns[local] = true
		f.mu.Unlock()
		go func() {
			defer func() { local.Close(); f.mu.Lock(); delete(f.conns, local); f.mu.Unlock(); <-slots }()
			upstream, err := DialPort(f.ctx, client, port)
			if err != nil {
				return
			}
			defer upstream.Close()
			stop := context.AfterFunc(f.ctx, func() { upstream.Close(); local.Close() })
			defer stop()
			tailcat.ProxyConns(local, upstream)
		}()
	}
}
