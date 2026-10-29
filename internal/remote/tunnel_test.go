package remote

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"retty/internal/rex"
	"tailscale.com/types/key"
)

func TestClientKeyIsStablePerDesktop(t *testing.T) {
	a, b := tailcat.NewPrivateKey(), tailcat.NewPrivateKey()
	addrA, addrB := a.Public.Addr(), b.Public.Addr()
	SetClientSecret(nil)
	if !clientKey(addrA, 0).IsZero() {
		t.Fatal("a device without a secret must use ephemeral keys")
	}
	SetClientSecret([]byte("0123456789abcdef0123456789abcdef"))
	defer SetClientSecret(nil)
	first := clientKey(addrA, 0)
	if first.IsZero() || !first.Equal(clientKey(addrA, 0)) {
		t.Fatal("a desktop must see the same key on every connection")
	}
	if first.Equal(clientKey(addrB, 0)) || first.Equal(clientKey(addrA, 1)) {
		t.Fatal("desktops and slots must not share one key")
	}
	SetClientSecret([]byte("another device's secret, 32 byte"))
	if clientKey(addrA, 0).Equal(first) {
		t.Fatal("devices must not share one key")
	}
}

// Live tunnels never share a key, and a key just closed rests before reuse,
// so the desktop no longer answers its handshakes on the old direct path.
func TestTunnelKeysRestBetweenTunnels(t *testing.T) {
	SetClientSecret([]byte("0123456789abcdef0123456789abcdef"))
	defer SetClientSecret(nil)
	addr := tailcat.NewPrivateKey().Public.Addr()
	slot := func(i byte) key.NodePublic { return clientKey(addr, i).Public() }
	first := openTunnel(addr, addr)
	second := openTunnel(addr, addr)
	if first.pub != slot(0) || second.pub != slot(1) {
		t.Fatal("concurrent tunnels did not take separate slots")
	}
	first.close()
	third := openTunnel(addr, addr)
	if third.pub != slot(2) {
		t.Fatal("a key closed a moment ago was reused")
	}
	fourth, fifth := openTunnel(addr, addr), openTunnel(addr, addr)
	if fourth.pub != slot(3) || !fifth.pub.IsZero() {
		t.Fatal("exhausted slots did not fall back to an ephemeral key")
	}
	keyed.Lock()
	keyed.closed[slot(0)] = time.Now().Add(-keyRest)
	keyed.Unlock()
	rested := openTunnel(addr, addr)
	if rested.pub != slot(0) {
		t.Fatal("a rested key was not reused")
	}
	for _, tunnel := range []*tunnel{second, third, fourth, fifth, rested} {
		tunnel.close()
	}
}

// A process that ended without closing its tunnel leaves its slot unrecorded
// in memory; the next process starts past it, as it would past a key closed a
// moment ago.
func TestTunnelKeysSkipLastProcessSlot(t *testing.T) {
	SetClientSecret([]byte("0123456789abcdef0123456789abcdef"))
	defer SetClientSecret(nil)
	reset := func() {
		keyed.Lock()
		keyed.live, keyed.closed = nil, nil
		keyed.first, keyed.path, keyed.saved = 0, "", -1
		keyed.Unlock()
	}
	reset()
	defer reset()
	addr := tailcat.NewPrivateKey().Public.Addr()
	slot := func(i byte) key.NodePublic { return clientKey(addr, i).Public() }
	path := filepath.Join(t.TempDir(), "client-key-slot")
	SetKeySlotFile(path)
	if first := openTunnel(addr, addr); first.pub != slot(0) {
		t.Fatal("a first process did not start at slot 0")
	}
	// The process ends; its tunnel is never closed.
	reset()
	SetKeySlotFile(path)
	next := openTunnel(addr, addr)
	defer next.close()
	if next.pub != slot(1) {
		t.Fatal("a new process reused the slot the last one left live")
	}
	if data, _ := os.ReadFile(path); len(data) != 1 || data[0] != 1 {
		t.Fatalf("recorded slot %v, want 1", data)
	}
	reset()
	SetKeySlotFile(path)
	// Starting at 2, each closed key rests, so these take 2, 3, then 0.
	for range keySlots - 1 {
		openTunnel(addr, addr).close()
	}
	if data, _ := os.ReadFile(path); len(data) != 1 || data[0] != 0 {
		t.Fatalf("slots did not wrap around: recorded %v", data)
	}
}

// serveFake answers hello and list on conn until it closes; a nil stall
// answers at once.
func serveFake(conn net.Conn, stall <-chan struct{}) {
	defer conn.Close()
	decoder := json.NewDecoder(conn)
	for {
		var req rex.Request
		if decoder.Decode(&req) != nil {
			return
		}
		if stall != nil {
			<-stall
			return
		}
		var data []byte
		if req.Op == "hello" {
			data, _ = json.Marshal(rex.Hello{Version: rex.ProtocolVersion})
		} else {
			data, _ = json.Marshal([]rex.SessionInfo{{ID: "existing"}})
		}
		if json.NewEncoder(conn).Encode(rex.Response{ID: req.ID, Data: data}) != nil {
			return
		}
	}
}

func TestResumeRedialsWhileCheckStalls(t *testing.T) {
	conn, peer := net.Pipe()
	stall := make(chan struct{})
	defer close(stall)
	go serveFake(peer, stall)
	client := rex.NewClient(conn, func(context.Context) (net.Conn, error) {
		local, remote := net.Pipe()
		go serveFake(remote, nil)
		return local, nil
	})
	defer client.Close()
	started := time.Now()
	next, hello, sessions, err := resumeConnection(context.Background(), client, nil, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next == client || hello.Version != rex.ProtocolVersion || len(sessions) != 1 {
		t.Fatalf("got %v %+v %+v", next == client, hello, sessions)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("resume waited %v for the stalled socket", elapsed)
	}
}

func TestResumeKeepsAnsweringSocket(t *testing.T) {
	conn, peer := net.Pipe()
	go serveFake(peer, nil)
	var dials atomic.Int32
	client := rex.NewClient(conn, func(context.Context) (net.Conn, error) {
		dials.Add(1)
		local, remote := net.Pipe()
		go serveFake(remote, nil)
		return local, nil
	})
	defer client.Close()
	next, _, sessions, err := resumeConnection(context.Background(), client, nil, time.Second)
	if err != nil || next != client || len(sessions) != 1 {
		t.Fatalf("got %v %+v %v", next == client, sessions, err)
	}
	if dials.Load() != 0 {
		t.Fatal("a healthy socket was redialed")
	}
}

// Live Tailcat: a probe parks its tunnel for the connection, and repeated
// connections with a device secret reuse a few peers on the desktop.
func TestTailcatStableKeyAndWarmTunnel(t *testing.T) {
	if os.Getenv("RETTY_REMOTE_E2E") != "1" {
		t.Skip("set RETTY_REMOTE_E2E=1 for live Tailcat transport")
	}
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	go rex.Serve()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", rex.SocketPath())
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	desktop, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.Close()
	defer desktop.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bridge, err := Start(ctx, rex.SocketPath(), Options{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	defer CloseWarmTunnels()
	if err := LoadClientSecret(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer SetClientSecret(nil)
	link := bridge.Link()
	addr, _ := ParseLink(link)

	started := time.Now()
	if _, _, err := Preview(ctx, link, nil); err != nil {
		t.Fatalf("probe: %v", err)
	}
	cold := time.Since(started)
	warm.Lock()
	parked := warm.tunnels[addr]
	warm.Unlock()
	if parked == nil {
		t.Fatal("a probe that reached the desktop did not park its tunnel")
	}
	// The preview alone must prewarm path discovery, before OpenConnection
	// attaches a metrics subscriber or creates any terminal streams.
	deadline = time.Now().Add(10 * time.Second)
	for {
		parked.t.path.mu.Lock()
		path := parked.t.path.last
		parked.t.path.mu.Unlock()
		if !path.At.IsZero() {
			t.Logf("preview path: direct=%v RTT=%v", path.Direct, path.Latency)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parked preview did not discover a transport path")
		}
		time.Sleep(20 * time.Millisecond)
	}
	device := rex.DeviceInfo{Name: "Test iPhone", OS: "iOS"}
	for i := range 3 {
		started = time.Now()
		quality := &Quality{}
		client, closeTunnel, _, sessions, err := OpenConnection(ctx, link, device, nil, quality)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		t.Logf("connection %d took %v (cold probe %v), %d sessions", i, time.Since(started), cold, len(sessions))
		if i == 0 && takeTunnel(addr) != nil {
			t.Fatal("the connection left the probe's tunnel parked")
		}
		if i == 0 && quality.Snapshot(time.Now()).Path.At.IsZero() {
			t.Fatal("warm connection did not inherit preview path metrics")
		}
		CloseConnection(client, closeTunnel)
		if i == 0 {
			select {
			case <-parked.t.path.done:
			case <-time.After(5 * time.Second):
				t.Fatal("preview discovery survived the connection's tunnel close")
			}
		}
	}
	// The probe's tunnel became the first connection; the next two took the
	// following slots without waiting for the first to rest.
	peers := len(bridge.server.Status().Peer)
	if peers != 3 {
		t.Fatalf("desktop holds %d peers after a probe and three connections, want 3", peers)
	}
	// After the burst, reconnections at a human pace cycle through the slots
	// and add no peers at all.
	settled := 0
	for i := range 2 * keySlots {
		if i == keySlots {
			settled = len(bridge.server.Status().Peer)
		}
		client, closeTunnel, _, _, err := OpenConnection(ctx, link, device, nil, nil)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		CloseConnection(client, closeTunnel)
		time.Sleep(keyRest / 2)
	}
	if peers := len(bridge.server.Status().Peer); peers != settled {
		t.Fatalf("paced reconnections grew the desktop's peers from %d to %d", settled, peers)
	}
}
