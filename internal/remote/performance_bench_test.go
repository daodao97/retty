package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"retty/internal/files"
	"retty/internal/rex"
)

// Opt-in: uses the actual encrypted Tailcat transport between two owned local
// peers, including any relay it selects. It does not represent Wi-Fi or a phone.
func BenchmarkTailcatRPC(b *testing.B) {
	if os.Getenv("RETTY_REMOTE_E2E") != "1" {
		b.Skip("set RETTY_REMOTE_E2E=1 for real encrypted transport")
	}
	dir, err := os.MkdirTemp("/tmp", "retty-transport-perf-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	b.Setenv("RETTY_DIR", dir)
	if audit := os.Getenv("RETTY_PERF_AUDIT_DIR"); audit != "" {
		before := runtime.NumGoroutine()
		var memoryBefore runtime.MemStats
		runtime.ReadMemStats(&memoryBefore)
		// Registered before the server cleanup, so this runs after the PTY,
		// bridge and control client have stopped. Draining tunnels is async.
		b.Cleanup(func() {
			time.Sleep(2500 * time.Millisecond)
			deadline := time.Now().Add(3 * time.Second)
			for runtime.NumGoroutine() > before+4 && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			runtime.GC()
			var memoryAfterOneGC runtime.MemStats
			runtime.ReadMemStats(&memoryAfterOneGC)
			runtime.GC() // sync.Pool victims may survive one collection
			var memoryAfter runtime.MemStats
			runtime.ReadMemStats(&memoryAfter)
			if err := os.MkdirAll(audit, 0700); err != nil {
				b.Error(err)
				return
			}
			base := filepath.Join(audit, fmt.Sprintf("cleanup-%d", time.Now().UnixNano()))
			profile, err := os.OpenFile(base+".goroutines.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				b.Error(err)
				return
			}
			pprof.Lookup("goroutine").WriteTo(profile, 2)
			profile.Close()
			heap, err := os.OpenFile(base+".heap", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				b.Error(err)
				return
			}
			pprof.Lookup("heap").WriteTo(heap, 0)
			heap.Close()
			data, _ := json.MarshalIndent(map[string]any{
				"goroutines_before": before, "goroutines_after": runtime.NumGoroutine(),
				"heap_alloc_after_one_gc": memoryAfterOneGC.HeapAlloc,
				"heap_alloc_before":       memoryBefore.HeapAlloc, "heap_alloc_after_gc": memoryAfter.HeapAlloc,
				"heap_objects_before": memoryBefore.HeapObjects, "heap_objects_after_gc": memoryAfter.HeapObjects,
				"note": "Heap includes process-global runtime/library caches; inspect stack ownership before calling a delta a leak.",
			}, "", "  ")
			if err := os.WriteFile(base+".json", data, 0600); err != nil {
				b.Error(err)
			}
		})
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- rex.Serve() }()
	var desktop *rex.Client
	b.Cleanup(func() {
		if desktop != nil {
			desktop.Shutdown()
			desktop.Close()
		}
		select {
		case err := <-serverDone:
			if err != nil {
				b.Error(err)
			}
		case <-time.After(5 * time.Second):
			b.Error("owned server did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", rex.SocketPath())
		if err == nil {
			desktop = rex.NewClient(conn, nil)
			break
		}
		if time.Now().After(deadline) {
			b.Fatal("owned server did not start: ", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hello, err := desktop.Hello(); err != nil || hello.PID != os.Getpid() {
		b.Fatalf("server is not owned by this benchmark: %v", err)
	}
	if _, err := desktop.Create(rex.CreateOptions{Dir: dir, Command: []string{"/bin/cat"}, Cols: 120, Rows: 38}); err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bridge, err := Start(ctx, rex.SocketPath(), Options{StateDir: filepath.Join(dir, "transport")})
	if err != nil {
		b.Fatal(err)
	}
	defer bridge.Close()
	quality := &Quality{}
	client, closeTunnel, err := connect(ctx, bridge.Link(), false, nil, quality)
	if err != nil {
		b.Fatal(err)
	}
	defer closeTunnel()
	defer client.Close()
	if _, err := client.Hello(); err != nil {
		b.Fatal(err)
	}
	if err := quality.Probe(ctx); err != nil {
		b.Fatal("cannot measure transport path: ", err)
	}
	initial := quality.Snapshot(time.Now()).Path
	if initial.At.IsZero() {
		b.Fatal("missing transport path metadata")
	}
	route := "relay"
	if initial.Direct {
		route = "direct"
	}
	benchmark := func(name string, run func(*testing.B)) {
		b.Run(route+"/"+name, func(b *testing.B) {
			check := func() {
				b.Helper()
				if err := quality.Probe(ctx); err != nil || quality.Snapshot(time.Now()).Path.Direct != initial.Direct {
					b.Fatal("transport path changed or became unavailable; do not compare this run: ", err)
				}
			}
			check()
			run(b)
			b.StopTimer()
			check()
		})
	}
	benchmark("hello", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := client.Hello(); err != nil {
				b.Fatal(err)
			}
		}
	})
	benchmark("cached-list", func(b *testing.B) {
		if _, err := client.List(); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			list, err := client.List()
			if err != nil || len(list) != 1 {
				b.Fatalf("list: %d entries: %v", len(list), err)
			}
		}
	})
	path := filepath.Join(dir, "preview.txt")
	data := bytes.Repeat([]byte("fixture 中\n"), 1<<19)
	if err := os.WriteFile(path, data, 0600); err != nil {
		b.Fatal(err)
	}
	for _, length := range []int{4096, 512 << 10, files.MaxRead} {
		benchmark(fmt.Sprintf("file-bytes=%d", length), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(length))
			calls := 0
			for b.Loop() {
				calls++
				r, err := BrowseFiles(ctx, client, files.Request{Kind: "read", Path: path, Length: length})
				if err != nil || !bytes.Equal(r.Data, data[:length]) {
					b.Fatalf("file request %d failed or changed payload: %v", calls, err)
				}
			}
		})
	}
	data = nil   // fixture payload is not application retention in cleanup audits
	client = nil // child benchmark closures also capture this variable
}
