//go:build darwin || linux

package rex

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

type cadenceDelayConn struct {
	net.Conn
	delay time.Duration
}

func (c *cadenceDelayConn) Write(b []byte) (int, error) { time.Sleep(c.delay); return c.Conn.Write(b) }

// This opt-in measurement models a viewer consuming each interactive screen
// over a fixed RTT. It reports cadence without asserting scheduler timings.
// No installed daemon or device is used.
func TestScreenSyncScrollCadence(t *testing.T) {
	if os.Getenv("RETTY_SCREEN_SYNC_MEASURE") != "1" {
		t.Skip("opt-in isolated screen cadence measurement")
	}

	for _, rtt := range []time.Duration{0, 12 * time.Millisecond, 40 * time.Millisecond} {
		s := stateFixture(t)
		s.vt.Feed([]byte("initial"))
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.attachScreenSync(&cadenceDelayConn{server, rtt / 2}, bufio.NewReader(server), 0, 0, false)
		}()
		v := &Stream{conn: &cadenceDelayConn{client, rtt / 2}, screenFrames: true, screenSync: true}
		v.cond = sync.NewCond(&v.mu)
		t.Cleanup(func() { v.Close(); <-done })
		if _, _, _, err := v.ReadSnapshot(); err != nil {
			t.Fatal(err)
		}
		var samples []float64
		for i := 0; i < 40; i++ {
			s.mu.Lock()
			data := []byte(fmt.Sprintf("\x1b[Hscroll-%04d", i))
			s.vt.Feed(data)
			s.broadcastLocked(data)
			s.mu.Unlock()
			start := time.Now()
			_, _, _, snapshot, err := v.ReadScreenUpdate(make([]byte, 65536))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(snapshot, []byte(fmt.Sprintf("scroll-%04d", i))) {
				t.Fatalf("stale screen at step %d", i)
			}
			if i >= 5 {
				samples = append(samples, float64(time.Since(start))/float64(time.Millisecond))
			}
		}
		v.Close()
		sort.Float64s(samples)
		t.Logf("RTT=%v p50=%.2fms p95=%.2fms", rtt, samples[len(samples)/2], samples[len(samples)*95/100])
	}
}
