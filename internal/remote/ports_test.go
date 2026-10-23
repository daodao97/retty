package remote

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPortForwardHTTPWebSocketAndOwnedCleanupWithoutDaemon(t *testing.T) {
	c, _, socket, _ := fileUploadFixture(t)
	os.Remove(socket)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			w.Header().Set("X-Fixture", "remote-host")
			io.WriteString(w, "remote:"+r.URL.RequestURI())
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		kind, data, err := conn.Read(r.Context())
		if err == nil {
			conn.Write(r.Context(), kind, append([]byte("echo:"), data...))
		}
	}))
	defer server.Close()
	_, portString, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portString)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forward, err := ForwardPort(ctx, c, port)
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()
	res, err := http.Get("http://" + forward.Address() + "/dev?q=1")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(data) != "remote:/dev?q=1" || res.Header.Get("X-Fixture") != "remote-host" {
		t.Fatal(string(data), res.Header)
	}
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wsCancel()
	ws, _, err := websocket.Dial(wsCtx, "ws://"+forward.Address()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = ws.Write(wsCtx, websocket.MessageText, []byte("hmr")); err != nil {
		t.Fatal(err)
	}
	_, data, err = ws.Read(wsCtx)
	ws.CloseNow()
	if err != nil || string(data) != "echo:hmr" {
		t.Fatal(string(data), err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", forward.Address(), 20*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("canceled forward kept listening")
}

func TestPortForwardValidatesPortsAndRetainsBufferedServerBytes(t *testing.T) {
	c, _, socket, _ := fileUploadFixture(t)
	os.Remove(socket)
	for _, port := range []int{-1, 0, 65536} {
		if conn, err := DialPort(context.Background(), c, port); err == nil {
			conn.Close()
			t.Fatal("invalid port accepted", port)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Write([]byte("early-server-banner"))
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	conn, err := DialPort(context.Background(), c, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil || string(data) != "early-server-banner" {
		t.Fatal(string(data), err)
	}
}
