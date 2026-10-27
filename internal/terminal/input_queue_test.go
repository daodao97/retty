package terminal

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func runInputQueue(t *testing.T, q *inputQueue, w io.Writer) {
	t.Helper()
	done := make(chan struct{})
	go func() { q.run(w); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		q.close()
		<-done
		t.Error("input writer did not finish")
	}
}

type gatedInputWriter struct {
	writes  chan []byte
	release chan struct{}
}

func (w *gatedInputWriter) Write(p []byte) (int, error) {
	w.writes <- bytes.Clone(p)
	<-w.release
	return len(p), nil
}

func TestInputQueueBatchesBacklogWithoutReorderingReversals(t *testing.T) {
	var q inputQueue
	w := &gatedInputWriter{writes: make(chan []byte, 256), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { q.run(w); close(done) }()
	t.Cleanup(func() { q.close(); close(w.release); <-done })
	q.push([]byte("first-in-flight"))
	if got := <-w.writes; string(got) != "first-in-flight" {
		t.Fatalf("first write = %q", got)
	}
	var want []byte
	for i := range 60 {
		p := []byte("\x1b[<64;3;4M")
		if i%2 != 0 {
			p = []byte("\x1b[<65;3;4M")
		}
		q.push(bytes.Clone(p))
		want = append(want, p...)
		if i == 30 {
			q.push([]byte("typed-between-gestures\r"))
			want = append(want, "typed-between-gestures\r"...)
		}
	}
	w.release <- struct{}{}
	if got := <-w.writes; !bytes.Equal(got, want) {
		t.Fatalf("backlog was not sent together, in order: got %d bytes, want %d", len(got), len(want))
	}
}

func TestInputQueueBoundsBatchAndKeepsLargePaste(t *testing.T) {
	var q inputQueue
	parts := [][]byte{bytes.Repeat([]byte("a"), 20<<10), bytes.Repeat([]byte("b"), 20<<10), bytes.Repeat([]byte("c"), 70<<10), []byte("after-paste\r")}
	for _, p := range parts {
		q.push(p)
	}
	var got [][]byte
	runInputQueue(t, &q, inputWriterFunc(func(p []byte) (int, error) {
		got = append(got, bytes.Clone(p))
		if len(got) == len(parts) {
			q.close()
		}
		return len(p), nil
	}))
	if len(got) != len(parts) || !bytes.Equal(bytes.Join(got, nil), bytes.Join(parts, nil)) {
		t.Fatal("batching changed a large paste or the following key")
	}
}

type inputWriterFunc func([]byte) (int, error)

func (f inputWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestInputQueueCompletesShortWrites(t *testing.T) {
	var q inputQueue
	q.push([]byte("scroll-and-typed-input\r"))
	var got []byte
	runInputQueue(t, &q, inputWriterFunc(func(p []byte) (int, error) {
		n := min(3, len(p))
		got = append(got, p[:n]...)
		if len(got) >= len("scroll-and-typed-input\r") {
			q.close()
		}
		return n, nil
	}))
	if string(got) != "scroll-and-typed-input\r" {
		t.Fatalf("short writes lost input: %q", got)
	}
}

func TestInputQueueStopsOnWriterFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{{"no-progress", 0, nil}, {"invalid-count", 10, nil}, {"disconnected", 0, io.ErrClosedPipe}} {
		t.Run(tc.name, func(t *testing.T) {
			var q inputQueue
			q.push([]byte("x"))
			calls := 0
			runInputQueue(t, &q, inputWriterFunc(func([]byte) (int, error) {
				calls++
				return tc.n, tc.err
			}))
			q.push([]byte("not-replayed"))
			if calls != 1 || !q.closed || len(q.queue) != 0 {
				t.Fatal("failed writer retained or replayed input")
			}
		})
	}
}
