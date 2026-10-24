//go:build darwin || linux

package rex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"testing"
)

// These benchmarks use owned transports or PTYs and never contact an installed
// daemon. Keep them stable so before/after comparisons exercise the same work.
func BenchmarkControlConnection(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		front, back := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer back.Close()
			var req Request
			if json.NewDecoder(back).Decode(&req) == nil {
				data, _ := json.Marshal(Hello{Version: ProtocolVersion})
				json.NewEncoder(back).Encode(Response{ID: req.ID, Data: data})
			}
		}()
		client := NewClient(front, nil)
		if _, err := client.Hello(); err != nil {
			b.Fatal(err)
		}
		client.Close()
		<-client.Closed()
		<-done
	}
}

func BenchmarkServerHello(b *testing.B) {
	ready := make(chan struct{})
	close(ready)
	s := &Server{hostDone: ready}
	b.ReportAllocs()
	for b.Loop() {
		res := s.request(Request{Op: "hello"})
		if res.Error != "" || len(res.Data) == 0 {
			b.Fatal("hello failed: ", res.Error)
		}
	}
}

func BenchmarkListControl(b *testing.B) {
	for _, count := range []int{1, 8, 64, 256} {
		for _, cached := range []bool{false, true} {
			b.Run(fmt.Sprintf("sessions=%d/cached=%t", count, cached), func(b *testing.B) {
				infos := make([]SessionInfo, count)
				for i := range infos {
					infos[i] = SessionInfo{ID: fmt.Sprint(i), Program: "sh", Args: []string{"-l"}, Dir: "/owned/fixture", Idle: true, Cols: 120, Rows: 40}
				}
				data, _ := json.Marshal(infos)
				front, back := net.Pipe()
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer back.Close()
					r, w := json.NewDecoder(bufio.NewReader(back)), json.NewEncoder(back)
					for {
						var req Request
						if r.Decode(&req) != nil {
							return
						}
						res := Response{ID: req.ID, Data: data, ETag: "fixture"}
						if cached && req.ETag == res.ETag {
							res.Data = nil
						}
						if w.Encode(res) != nil {
							return
						}
					}
				}()
				client := NewClient(front, nil)
				b.Cleanup(func() { client.Close(); <-client.Closed(); <-done })
				if _, err := client.List(); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					got, err := client.List()
					if err != nil || len(got) != count {
						b.Fatalf("list returned %d sessions: %v", len(got), err)
					}
				}
			})
		}
	}
}

// Unlike BenchmarkListControl's fixed replies, this includes live process
// inspection, session locking, JSON encoding and ETag generation in the server.
func BenchmarkLiveServerList(b *testing.B) {
	for _, count := range []int{1, 8, 64} {
		b.Run(fmt.Sprintf("sessions=%d", count), func(b *testing.B) {
			dir := b.TempDir()
			b.Setenv("RETTY_DIR", dir)
			s := &Server{sessions: map[string]*session{}}
			for i := range count {
				id := fmt.Sprint(i)
				ss, err := newSession(id, CreateOptions{Command: []string{"/bin/cat"}, Dir: dir, Cols: 120, Rows: 38})
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(ss.kill)
				s.sessions[id], s.order = ss, append(s.order, id)
			}
			tag := s.request(Request{Op: "list"}).ETag
			if tag == "" {
				b.Fatal("missing ETag")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				res := s.request(Request{Op: "list", ETag: tag})
				if res.Error != "" || res.ETag != tag || len(res.Data) != 0 {
					b.Fatalf("stable live list failed: %s, ETag=%q, bytes=%d", res.Error, res.ETag, len(res.Data))
				}
			}
		})
	}
}
