package remote

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tailscale/tailcat"
	"retty/internal/atomicfile"
	"tailscale.com/types/key"
)

// A Tailcat server keeps every node key that ever reached it as a WireGuard
// peer, and announces its endpoints to all of them on each new peer. A fresh
// key per tunnel would grow that list with every connection and availability
// probe until the desktop restarts. Each device therefore derives one stable
// key per desktop from a private secret, as Tailscale nodes keep theirs.
var clientSecret atomic.Pointer[[]byte]

// SetClientSecret sets the private seed of this device's tunnel keys. Without
// one, as in tests and the CLI, every tunnel uses an ephemeral key.
func SetClientSecret(secret []byte) {
	if len(secret) == 0 {
		clientSecret.Store(nil)
		return
	}
	secret = append([]byte(nil), secret...)
	clientSecret.Store(&secret)
}

// LoadClientSecret reads the desktop's secret from dir, creating it on first
// use, and sets it.
func LoadClientSecret(dir string) error {
	path := filepath.Join(dir, "client-secret")
	secret, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(secret) != 32 {
		secret = make([]byte, 32)
		rand.Read(secret)
		err = atomicfile.Write(path, secret)
	}
	if err != nil {
		return err
	}
	SetClientSecret(secret)
	SetKeySlotFile(filepath.Join(dir, "client-key-slot"))
	return nil
}

// clientKey is this device's key number slot for the desktop at addr, or
// zero for an ephemeral one.
func clientKey(addr tailcat.Addr, slot byte) key.NodePrivate {
	secret := clientSecret.Load()
	ci, err := tailcat.ParseAddr(addr)
	if secret == nil || err != nil {
		return key.NodePrivate{}
	}
	mac := hmac.New(sha256.New, *secret)
	mac.Write([]byte("retty tailcat client v1\x00"))
	mac.Write(ci.ServerPublic.NodePublic.AppendTo(nil))
	mac.Write([]byte{slot})
	raw := mac.Sum(nil)
	raw[0] &= 248
	raw[31] = raw[31]&127 | 64
	var k key.NodePrivate
	if k.UnmarshalText([]byte("privkey:"+hex.EncodeToString(raw))) != nil {
		return key.NodePrivate{}
	}
	return k
}

// The desktop trusts a key's last direct UDP path for a few seconds after
// its last pong. A new tunnel under a key closed moments ago would have its
// handshake answered to the dead socket, and wait out WireGuard's 5 second
// retry. Each desktop therefore gets a few key slots, and a tunnel takes the
// first one neither live nor closed within keyRest. Two live tunnels never
// share a key, which would contend for one relay slot and WireGuard peer.
// Only a burst of reconnections exhausts the slots and falls back to an
// ephemeral key.
const keySlots = 4
const keyRest = 10 * time.Second

var keyed struct {
	sync.Mutex
	live   map[key.NodePublic]bool
	closed map[key.NodePublic]time.Time
	// first is the slot this process tries first, and path records the last
	// slot it claimed, saved.
	first byte
	path  string
	saved int
}

// SetKeySlotFile keeps the last claimed slot in path. A process that ends
// without closing its tunnels, as when iOS ends a suspended app, leaves no
// record of its closed keys, and the next one would take the same slot
// within the desktop's trust of its dead path. Each process therefore starts
// past the slot the last one claimed.
func SetKeySlotFile(path string) {
	keyed.Lock()
	defer keyed.Unlock()
	keyed.path, keyed.first, keyed.saved = path, 0, -1
	if data, err := os.ReadFile(path); err == nil && len(data) == 1 && data[0] < keySlots {
		keyed.first, keyed.saved = (data[0]+1)%keySlots, int(data[0])
	}
}

type tunnel struct {
	addr   tailcat.Addr
	client *tailcat.Client
	pub    key.NodePublic // zero for an ephemeral key
	logf   atomic.Pointer[func(string, ...any)]
	used   atomic.Bool // a connection succeeded, so its stack is up
	once   sync.Once
	path   *pathDiscovery
}

func openTunnel(addr, server tailcat.Addr) *tunnel {
	t := &tunnel{addr: addr, client: &tailcat.Client{Server: server}}
	if k := claimKey(addr); !k.IsZero() {
		t.client.Key, t.pub = k, k.Public()
	}
	// Tailcat fixes its logger at first use; a warm tunnel handed to a
	// diagnosed connection reports to that connection's trace instead.
	t.client.Logf = func(format string, args ...any) {
		if logf := t.logf.Load(); logf != nil {
			(*logf)(format, args...)
		}
	}
	t.path = newPathDiscovery(func(ctx context.Context) (PathSample, error) {
		p, err := t.client.DiscoPing(ctx)
		if err != nil {
			return PathSample{}, err
		}
		return PathSample{At: time.Now(), Direct: p.Endpoint != "", Latency: time.Duration(p.LatencySeconds * float64(time.Second))}, nil
	})
	return t
}

func claimKey(addr tailcat.Addr) key.NodePrivate {
	keyed.Lock()
	defer keyed.Unlock()
	for i := range byte(keySlots) {
		slot := (keyed.first + i) % keySlots
		k := clientKey(addr, slot)
		if k.IsZero() {
			return k
		}
		pub := k.Public()
		if keyed.live[pub] || time.Since(keyed.closed[pub]) < keyRest {
			continue
		}
		if keyed.live == nil {
			keyed.live = make(map[key.NodePublic]bool)
		}
		keyed.live[pub] = true
		if keyed.path != "" && int(slot) != keyed.saved && atomicfile.Write(keyed.path, []byte{slot}) == nil {
			keyed.saved = int(slot)
		}
		return k
	}
	return key.NodePrivate{}
}

func (t *tunnel) setLog(logf func(string, ...any)) {
	if logf == nil {
		t.logf.Store(nil)
		return
	}
	t.logf.Store(&logf)
}

func (t *tunnel) dial(parent context.Context) (net.Conn, error) {
	if t.used.Load() {
		// A stream/control redial after suspension should not wait for
		// the periodic discovery timer to find the new local path.
		t.path.request()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	conn, err := t.client.DialTCPPort(ctx, Port)
	if err == nil {
		t.used.Store(true)
		t.path.start()
	}
	return conn, err
}

// close releases the tunnel's key at once, then drains and shuts it down in
// the background: the final FIN/ACK must leave before the userspace TCP
// stack stops.
func (t *tunnel) close() {
	t.once.Do(func() {
		t.path.cancel()
		if !t.pub.IsZero() {
			keyed.Lock()
			delete(keyed.live, t.pub)
			if keyed.closed == nil {
				keyed.closed = make(map[key.NodePublic]time.Time)
			}
			keyed.closed[t.pub] = time.Now()
			keyed.Unlock()
		}
		go func() {
			if t.used.Load() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				t.client.DrainTCP(ctx)
				cancel()
			}
			t.client.Close()
		}()
	})
}

// A desktop that answered an availability probe is likely the next one
// connected to, and is probed again shortly. Its tunnel stays up for a while
// so either reuses the relay connection and WireGuard session.
const warmFor = 45 * time.Second
const maxWarm = 4

var warm struct {
	sync.Mutex
	tunnels map[tailcat.Addr]*warmTunnel
}

type warmTunnel struct {
	t     *tunnel
	timer *time.Timer
	since time.Time
}

func parkTunnel(t *tunnel) {
	t.setLog(nil)
	warm.Lock()
	defer warm.Unlock()
	if warm.tunnels == nil {
		warm.tunnels = make(map[tailcat.Addr]*warmTunnel)
	}
	if old := warm.tunnels[t.addr]; old != nil {
		old.timer.Stop()
		old.t.close()
	}
	for len(warm.tunnels) >= maxWarm {
		var oldest tailcat.Addr
		for addr, w := range warm.tunnels {
			if oldest == "" || w.since.Before(warm.tunnels[oldest].since) {
				oldest = addr
			}
		}
		warm.tunnels[oldest].timer.Stop()
		warm.tunnels[oldest].t.close()
		delete(warm.tunnels, oldest)
	}
	w := &warmTunnel{t: t, since: time.Now()}
	w.timer = time.AfterFunc(warmFor, func() {
		warm.Lock()
		if warm.tunnels[t.addr] == w {
			delete(warm.tunnels, t.addr)
		}
		warm.Unlock()
		t.close()
	})
	warm.tunnels[t.addr] = w
}

func takeTunnel(addr tailcat.Addr) *tunnel {
	warm.Lock()
	defer warm.Unlock()
	w := warm.tunnels[addr]
	if w == nil || !w.timer.Stop() {
		return nil
	}
	delete(warm.tunnels, addr)
	return w.t
}

// CloseWarmTunnels ends parked probe tunnels, as when the app is suspended
// and their sockets would die anyway.
func CloseWarmTunnels() {
	warm.Lock()
	tunnels := warm.tunnels
	warm.tunnels = nil
	warm.Unlock()
	for _, w := range tunnels {
		if w.timer.Stop() {
			w.t.close()
		}
	}
}
