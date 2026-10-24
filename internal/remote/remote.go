// Package remote carries the existing Rex protocol over Tailcat's encrypted tunnel.
package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"retty/internal/notification"
	"retty/internal/rex"
	"retty/internal/selfupdate"
	"tailscale.com/tailcfg"
)

const Port = 4242

func quiet(string, ...any) {}

// Link keeps the case-sensitive Tailcat capability in a URL query, never a hostname.
func Link(addr tailcat.Addr) string {
	return "retty://connect?" + url.Values{"v": {"1"}, "address": {string(addr)}}.Encode()
}

func ParseLink(raw string) (tailcat.Addr, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4096 {
		return "", errors.New("连接码过长")
	}
	addr := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "retty" || u.Host != "connect" || u.Path != "" || u.Fragment != "" || u.User != nil {
			return "", errors.New("请扫描 Retty 桌面端的连接二维码")
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || q.Get("v") != "1" || len(q["address"]) != 1 {
			return "", errors.New("不支持的连接码版本")
		}
		addr = q.Get("address")
	}
	ci, err := tailcat.ParseAddr(tailcat.Addr(addr))
	if err != nil || ci.PresharedKey.IsZero() {
		return "", errors.New("无效的 Retty 连接码")
	}
	return tailcat.Addr(addr), nil
}

// Bridge exposes session transport and uploads to paired clients. Closing it
// disconnects phones without forgetting a persisted identity or ending sessions.
type Bridge struct {
	approvalDir  string
	server       *tailcat.Server
	listener     net.Listener
	mu           sync.Mutex
	closed       bool
	conns        map[net.Conn]bool
	devices      map[net.Conn]ConnectedDevice
	disconnected map[string]bool
	onPeer       func(ConnectedDevice)
	onDevice     func(rex.DeviceInfo)
	onNotice     func(rex.DesktopActivity, notification.Notice) (notification.Route, error)
	onClose      func()
	onImage      func(context.Context, string, []byte) error
	onUpdate     func(context.Context, selfupdate.Request) (selfupdate.Status, error)
	imageGate    chan struct{}
	imageContext context.Context
	imageCancel  context.CancelFunc
	uploadDir    string
	uploadGate   chan struct{}
	filesGate    chan struct{}
	portGate     chan struct{}
	updateGate   chan struct{}
	identityLock *os.File
}

// OnClose registers cleanup before publishing this bridge to consumers.
func (b *Bridge) OnClose(fn func()) { b.mu.Lock(); b.onClose = fn; b.mu.Unlock() }

type Options struct {
	// BootstrapRegion optionally supplies the initial relay for an ephemeral
	// bridge, allowing isolated transports without a public DERP map request.
	// A persisted identity's region takes precedence.
	BootstrapRegion *tailcfg.DERPRegion
	// StateDir persists the identity and relay across restarts. Empty uses an
	// ephemeral capability, useful for isolated fixtures.
	StateDir string
	// OnDevice receives metadata only after a successful compatible control
	// response. Callbacks must not block; terminal bytes are forwarded unchanged.
	OnDevice func(rex.DeviceInfo)
	// OnNotice arbitrates desktop/APNs delivery on the session's source host.
	OnNotice func(rex.DesktopActivity, notification.Notice) (notification.Route, error)
	// OnPeer reports a completed device connection, including clients without
	// push registration. It must not block the forwarding goroutine.
	OnPeer func(ConnectedDevice)
	// OnPasteImage runs off the UI thread after a bounded authenticated upload.
	// It must copy to the desktop clipboard before sending Ctrl+V to the session.
	OnPasteImage func(context.Context, string, []byte) error
	// OnUpdate accepts only official CLI updates. Nil on desktop/mobile bridges.
	OnUpdate func(context.Context, selfupdate.Request) (selfupdate.Status, error)
}

func Start(ctx context.Context, socket string, options Options) (*Bridge, error) {
	b := &Bridge{approvalDir: rex.Dir(), server: &tailcat.Server{Logf: quiet, Region: options.BootstrapRegion}, conns: make(map[net.Conn]bool), devices: make(map[net.Conn]ConnectedDevice)}
	stateDir := options.StateDir
	if stateDir != "" {
		identity, lock, err := loadIdentity(ctx, stateDir)
		if err != nil {
			return nil, err
		}
		b.identityLock = lock
		b.server.Key, b.server.PresharedKey, b.server.Region = identity.Key, identity.PresharedKey, identity.Region
	}
	b.imageContext, b.imageCancel = context.WithCancel(context.Background())
	b.imageGate = make(chan struct{}, 1)
	b.uploadGate = make(chan struct{}, 2)
	if stateDir != "" {
		b.uploadDir = filepath.Join(stateDir, "uploads")
	}
	b.onDevice, b.onNotice, b.onPeer, b.onImage = options.OnDevice, options.OnNotice, options.OnPeer, options.OnPasteImage
	b.onUpdate = options.OnUpdate
	ln, err := b.server.Listen(ctx, "tcp", fmt.Sprintf(":%d", Port))
	if err != nil {
		b.imageCancel()
		b.server.Close()
		releaseIdentity(b.identityLock)
		return nil, err
	}
	b.listener = ln
	if stateDir != "" {
		if err := saveIdentity(stateDir, b.server); err != nil {
			b.Close()
			return nil, err
		}
	}
	go b.accept(socket)
	return b, nil
}

func (b *Bridge) Link() string { return Link(b.server.TailcatAddr()) }

func (b *Bridge) accept(socket string) {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			conn.Close()
			return
		}
		// A disconnected device may keep redialing; refusing it must not
		// stop the listener from accepting other devices.
		if b.disconnected[deviceConnectionID(conn)] {
			b.mu.Unlock()
			conn.Close()
			continue
		}
		b.conns[conn] = true
		b.mu.Unlock()
		go func() {
			defer func() { conn.Close(); b.mu.Lock(); delete(b.conns, conn); delete(b.devices, conn); b.mu.Unlock() }()
			b.serveConnection(conn, socket)
		}()
	}
}

func (b *Bridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.imageCancel != nil {
		b.imageCancel()
	}
	if b.onClose != nil {
		b.onClose()
	}
	for c := range b.conns {
		c.Close()
	}
	b.mu.Unlock()
	b.listener.Close()
	// Keep the overlay alive long enough to deliver TCP FINs. Closing the
	// WireGuard/DERP engine immediately can leave the peer awaiting EOF.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b.server.DrainTCP(ctx)
	b.server.Close()
	releaseIdentity(b.identityLock)
}

// Connect owns a Tailcat client until closeTunnel is called. Every Rex stream
// shares its tunnel; a separate TCP connection carries each terminal's bytes.
func Connect(ctx context.Context, raw string) (*rex.Client, func(), error) {
	return connect(ctx, raw, true, nil, nil)
}

func connect(ctx context.Context, raw string, reportFailure bool, diagnostics *Diagnostics, quality *Quality) (*rex.Client, func(), error) {
	t, client, err := dialTunnel(ctx, raw, reportFailure, diagnostics)
	if err != nil {
		return nil, nil, err
	}
	unbind := func() {}
	if quality != nil {
		unbind = t.path.bind(quality)
	}
	return client, func() {
		unbind()
		t.close()
	}, nil
}

// dialTunnel opens a control connection, on a probe's parked tunnel when one
// is still up, else on a new tunnel.
func dialTunnel(ctx context.Context, raw string, reportFailure bool, diagnostics *Diagnostics) (*tunnel, *rex.Client, error) {
	var addr tailcat.Addr
	err := diagnostics.Measure(StageLink, func() error {
		var err error
		addr, err = ParseLink(raw)
		if err != nil {
			return &ConnectionError{Kind: InvalidLink, Cause: err}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	diagnostics.target(addr)
	logf := tunnelLog(diagnostics)
	if t := takeTunnel(addr); t != nil {
		t.setLog(logf)
		var client *rex.Client
		if diagnostics.Measure(StageTunnel, func() error {
			var err error
			client, err = rex.ConnectDial(ctx, t.dial)
			return err
		}) == nil {
			return t, client, nil
		}
		t.close()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	if err := diagnostics.Measure(StageNetwork, func() error { return prepareNetwork(ctx, addr) }); err != nil {
		return nil, nil, err
	}
	server := addr
	if runtime.GOOS == "ios" {
		server = hostnameRelayAddr(addr)
	}
	t := openTunnel(addr, server)
	t.setLog(logf)
	var client *rex.Client
	err = diagnostics.Measure(StageTunnel, func() error {
		var err error
		client, err = rex.ConnectDial(ctx, t.dial)
		return err
	})
	if err != nil {
		t.close()
		if reportFailure {
			log.Printf("Retty tunnel dial failed: %s", err)
		}
		return nil, nil, err
	}
	return t, client, nil
}

func tunnelLog(diagnostics *Diagnostics) func(string, ...any) {
	debugTunnel := os.Getenv("RETTY_DEBUG_TUNNEL") == "1"
	if diagnostics == nil && !debugTunnel {
		return nil
	}
	return func(format string, args ...any) {
		if event := diagnostics.TransportLog(format, args...); event != "" && debugTunnel {
			log.Printf("Retty tunnel: %s", event)
		}
	}
}

// Preview reads host and session display metadata without registering a phone
// or attaching to a terminal. A desktop that answers keeps its tunnel parked
// briefly, for the next probe or a connection to reuse.
func Preview(ctx context.Context, raw string) (rex.Hello, []rex.SessionInfo, error) {
	t, client, err := dialTunnel(ctx, raw, false, nil)
	if err != nil {
		return rex.Hello{}, nil, err
	}
	hello, sessions, err := readDesktopInfo(ctx, client, true)
	client.Close()
	if err != nil {
		t.close()
	} else {
		parkTunnel(t)
	}
	return hello, sessions, err
}

func readDesktopInfo(ctx context.Context, client *rex.Client, list bool) (rex.Hello, []rex.SessionInfo, error) {
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	var sessions []rex.SessionInfo
	if list {
		// The bridge registers phones only after hello followed by list.
		// Reading the list first keeps a preview invisible on older bridges too.
		var err error
		sessions, err = client.List()
		if ctx.Err() != nil {
			return rex.Hello{}, nil, ctx.Err()
		}
		if err != nil {
			return rex.Hello{}, nil, err
		}
	}
	hello, err := client.Hello()
	if ctx.Err() != nil {
		return hello, nil, ctx.Err()
	}
	if err != nil {
		return hello, nil, err
	}
	if hello.Version <= 0 || hello.Host.Name == "" {
		return hello, nil, errors.New("invalid Retty hello")
	}
	if !list {
		return hello, nil, nil
	}
	if !rex.CompatibleProtocol(hello.Version) {
		return hello, nil, &ConnectionError{Kind: ProtocolMismatch}
	}
	return hello, sessions, nil
}

// iOS networks may use DNS64 or a domain-based packet tunnel. Dialing the
// relay's baked-in IPs bypasses those routes; keep its TLS hostname and let
// the device resolver choose the reachable address family.
func hostnameRelayAddr(addr tailcat.Addr) tailcat.Addr {
	ci, err := tailcat.ParseAddr(addr)
	if err != nil {
		return addr
	}
	for _, region := range ci.Region {
		for _, node := range region.Nodes {
			if node.HostName != "" {
				if ip, err := netip.ParseAddr(node.IPv4); err == nil && ip.Is4() {
					node.IPv4 = ""
				}
				if ip, err := netip.ParseAddr(node.IPv6); err == nil && ip.Is6() {
					node.IPv6 = ""
				}
			}
		}
	}
	return ci.Addr()
}
