//go:build darwin || linux

package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"retty/internal/rex"
)

type pacedReceiver struct {
	net.Conn
	rate  int
	bytes atomic.Int64
	done  chan struct{}
	once  sync.Once
}

func (c *pacedReceiver) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p[:min(len(p), 4096)])
	c.bytes.Add(int64(n))
	if n > 0 && c.rate > 0 {
		timer := time.NewTimer(time.Duration(n) * time.Second / time.Duration(c.rate))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.done:
			return n, io.ErrClosedPipe
		}
	}
	return n, err
}
func (c *pacedReceiver) Close() error {
	c.once.Do(func() { close(c.done) })
	c.Conn.SetDeadline(time.Time{})
	return c.Conn.Close()
}

// This is a real encrypted connection with receiver-side bandwidth/processing
// shaping. It models backpressure, not packet loss, a radio or GPU presentation.
func TestEncryptedStreamPerformance(t *testing.T) {
	if os.Getenv("RETTY_REMOTE_E2E") != "1" || os.Getenv("RETTY_PERF_STREAM_OUTPUT") == "" {
		t.Skip("opt-in encrypted disposable stream measurement")
	}
	outputPath := os.Getenv("RETTY_PERF_STREAM_OUTPUT")
	if !filepath.IsAbs(outputPath) {
		t.Fatal("RETTY_PERF_STREAM_OUTPUT must be an absolute new JSON path")
	}
	if _, err := os.Lstat(outputPath); err == nil || !os.IsNotExist(err) {
		t.Fatal("output path must not exist", err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, _ := runtime.Caller(0)
	workload := filepath.Join(filepath.Dir(source), "..", "..", "scripts", "perf_fixture.py")
	var results []map[string]any
	for _, scenario := range []string{"fast", "slow-1Mbps-200ms", "dual-viewers"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "retty-netperf-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			t.Setenv("RETTY_DIR", dir)
			serverDone := make(chan error, 1)
			go func() { serverDone <- rex.Serve() }()
			var desktop *rex.Client
			t.Cleanup(func() {
				if desktop != nil {
					desktop.Shutdown()
					desktop.Close()
				}
				select {
				case err := <-serverDone:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("owned daemon failed to exit")
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for desktop == nil {
				conn, err := net.Dial("unix", rex.SocketPath())
				if err == nil {
					desktop = rex.NewClient(conn, nil)
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fixture server did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if hello, err := desktop.Hello(); err != nil || hello.PID != os.Getpid() {
				t.Fatalf("daemon does not belong to fixture: %v", err)
			}
			session, err := desktop.Create(rex.CreateOptions{Dir: dir, Command: []string{python, workload}, Cols: 120, Rows: 38})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			bridge, err := Start(ctx, rex.SocketPath(), Options{StateDir: filepath.Join(dir, "transport")})
			if err != nil {
				t.Fatal(err)
			}
			defer bridge.Close()
			quality := &Quality{}
			client, closeTunnel, err := connect(ctx, bridge.Link(), false, nil, quality)
			if err != nil {
				t.Fatal(err)
			}
			defer closeTunnel()
			defer client.Close()
			if _, err := client.Hello(); err != nil {
				t.Fatal(err)
			}
			if err := quality.Probe(ctx); err != nil {
				t.Fatal(err)
			}
			path := quality.Snapshot(time.Now()).Path
			if path.At.IsZero() {
				t.Fatal("missing transport path")
			}
			route := "relay"
			if path.Direct {
				route = "direct"
			}
			fast := client.ViewStream(session.ID).PreferScreenSync(false)
			defer fast.Close()
			firstStart := time.Now()
			initial, cols, rows, err := fast.ReadSnapshot()
			if err != nil || cols != 120 || rows != 38 {
				t.Fatalf("first viewport: %dx%d: %v", cols, rows, err)
			}
			firstMS := float64(time.Since(firstStart)) / float64(time.Millisecond)
			if enabled, _, _ := fast.ScreenStatus(); !enabled {
				t.Fatal("fast viewer did not negotiate screen_sync")
			}
			var lastMu sync.Mutex
			last := append([]byte(nil), initial...)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				buf := make([]byte, 64<<10)
				for {
					n, _, _, replacement, err := fast.ReadScreenUpdate(buf)
					if err != nil {
						return
					}
					lastMu.Lock()
					if replacement != nil {
						last = append(last[:0], replacement...)
					} else {
						last = append(last, buf[:n]...)
						if len(last) > 1<<20 {
							last = last[len(last)-(1<<20):]
						}
					}
					lastMu.Unlock()
				}
			}()
			defer func() {
				fast.Close()
				select {
				case <-readDone:
				case <-time.After(time.Second):
					t.Error("fast reader leaked")
				}
			}()
			until := func(marker string) {
				t.Helper()
				end := time.Now().Add(10 * time.Second)
				for {
					lastMu.Lock()
					found := bytes.Contains(last, []byte(marker))
					lastMu.Unlock()
					if found {
						return
					}
					if time.Now().After(end) {
						t.Fatalf("latest viewport did not contain %s", marker)
					}
					time.Sleep(time.Millisecond)
				}
			}
			var slow *rex.Stream
			var slowClient *rex.Client
			var slowBytes atomic.Int64
			var slowWire *pacedReceiver
			slowError := make(chan error, 1)
			if scenario != "fast" {
				control, err := client.DialExtension(ctx)
				if err != nil {
					t.Fatal(err)
				}
				slowClient = rex.NewClient(control, func(ctx context.Context) (net.Conn, error) {
					conn, err := client.DialExtension(ctx)
					if err != nil {
						return nil, err
					}
					slowWire = &pacedReceiver{Conn: conn, rate: 125000, done: make(chan struct{})}
					return slowWire, nil
				})
				defer slowClient.Close()
				slow = slowClient.ViewStream(session.ID).PreferScreenSync(false)
				defer slow.Close()
				if _, _, _, err := slow.ReadSnapshot(); err != nil {
					t.Fatal(err)
				}
				if enabled, _, _ := slow.ScreenStatus(); !enabled {
					t.Fatal("slow viewer did not negotiate screen_sync")
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					buf := make([]byte, 64<<10)
					for {
						n, _, _, replacement, err := slow.ReadScreenUpdate(buf)
						if err != nil {
							slowError <- err
							return
						}
						slowBytes.Add(int64(n + len(replacement)))
						if scenario == "slow-1Mbps-200ms" {
							select {
							case <-ctx.Done():
								return
							case <-time.After(200 * time.Millisecond):
							}
						}
					}
				}()
				defer func() {
					slow.Close()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("slow reader leaked")
					}
				}()
			}
			until("READY")
			before, err := desktop.List()
			if err != nil || len(before) != 1 {
				t.Fatal("missing owned session", err)
			}
			if _, err := fast.Write([]byte("OUTPUT 1048576\n")); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			time.Sleep(5 * time.Second)
			after, err := desktop.List()
			if err != nil || len(after) != 1 {
				t.Fatal("missing owned session after output", err)
			}
			throughput := float64(after[0].Output-before[0].Output) / time.Since(started).Seconds()
			if throughput < 1048576*.8 {
				t.Fatalf("PTY output blocked by viewer: %.0f B/s", throughput)
			}
			if scenario != "fast" {
				if slowBytes.Load() == 0 {
					t.Fatal("slow viewer did not consume any updates")
				}
				select {
				case err := <-slowError:
					t.Fatal("slow viewer disconnected during measurement", err)
				default:
				}
			}
			if _, err := fast.Write([]byte("STOP\n")); err != nil {
				t.Fatal(err)
			}
			until("OUTPUT_STOPPED")
			var latencies []float64
			for i := 0; i < 20; i++ {
				marker := fmt.Sprintf("PERF_ECHO_%d", i)
				start := time.Now()
				if _, err := fast.Write([]byte(fmt.Sprintf("ECHO %d\n", i))); err != nil {
					t.Fatal(err)
				}
				until(marker)
				latencies = append(latencies, float64(time.Since(start))/float64(time.Millisecond))
			}
			current, err := desktop.List()
			if err != nil || len(current) != 1 || current[0].PID != session.PID || current[0].Cols != 120 || current[0].Rows != 38 {
				t.Fatal("viewers changed process or geometry", err)
			}
			if err := quality.Probe(ctx); err != nil {
				t.Fatal(err)
			}
			if quality.Snapshot(time.Now()).Path.Direct != path.Direct {
				t.Fatal("transport path changed during measurement")
			}
			sorted := append([]float64(nil), latencies...)
			sort.Float64s(sorted)
			wireBytes := int64(0)
			if slowWire != nil {
				wireBytes = slowWire.bytes.Load()
			}
			results = append(results, map[string]any{"route": route, "slow_decrypted_wire_bytes": wireBytes, "scenario": scenario, "first_viewport_ms": firstMS, "output_bytes_s": throughput, "echo_ms": latencies, "p50_ms": (sorted[9] + sorted[10]) / 2, "p95_ms": sorted[18], "p99_ms": sorted[19], "slow_decoded_bytes": slowBytes.Load()})
		})
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sumFile := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}
	system, err := exec.Command("uname", "-srv").Output()
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"kind": "retty-stream-benchmark", "version": 1,
		"binary_sha256": sumFile(executable), "git": map[string]any{},
		"machine": map[string]any{"os": strings.TrimSpace(string(system)), "arch": runtime.GOARCH, "cpus": runtime.NumCPU()},
		"parameters": map[string]any{"go": runtime.Version(), "protocol": "screen_sync", "duration": 5, "output_rate": 1048576,
			"echo_samples": 20, "cols": 120, "rows": 38, "receiver_bytes_s": 125000, "processing_delay_ms": 200,
			"driver_sha256": map[string]string{"test": sumFile(source), "workload": sumFile(workload)}},
		"results": results, "limit": "Receiver-side shaping after decryption; not radio loss or GPU display latency.",
	}
	if t.Failed() {
		report["error"] = "one or more encrypted stream scenarios failed"
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
}
