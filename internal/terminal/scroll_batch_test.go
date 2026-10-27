package terminal

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal/internal/vt"
)

// No reader or writer is started: the queued bytes are the exact writes a
// remote stream would receive, independent of goroutine scheduling.
func scrollFixture(tb testing.TB, modes string) *view {
	tb.Helper()
	if err := Load(); err != nil {
		tb.Fatal(err)
	}
	screen, err := vt.NewTerminal(80, 24, vt.Effects{})
	if err != nil {
		tb.Fatal(err)
	}
	screen.Write([]byte("\x1b[?1049h" + modes))
	term := &Terminal{term: screen}
	v := &view{t: term, scale: 1, cols: 80, rows: 24, cellW: 8, cellH: 16}
	tb.Cleanup(func() {
		if v.mouse != nil {
			v.mouse.Free()
		}
		if v.keys != nil {
			v.keys.Free()
		}
		screen.Free()
	})
	return v
}

func TestScrollBatchesReportsWithoutChangingInput(t *testing.T) {
	for _, tc := range []struct {
		name, modes, up, down string
	}{
		{"sgr", "\x1b[?1003h\x1b[?1006h", "\x1b[<64;3;4M", "\x1b[<65;3;4M"},
		{"legacy", "\x1b[?1000h", "\x1b[M`#$", "\x1b[Ma#$"},
		{"arrows", "\x1b[?1007h", "\x1b[A", "\x1b[B"},
		{"application-arrows", "\x1b[?1007h\x1b[?1h", "\x1bOA", "\x1bOB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := scrollFixture(t, tc.modes)
			v.t.opts.SmoothScroll = true
			for _, rows := range []int{-32, 64} {
				v.scroll(ui.InputEvent{X: 20, Y: 56, DY: float32(rows * 16), Precise: true})
			}
			v.t.in.push([]byte("typed-after-scroll\r"))
			queue := v.t.in.queue
			var got []byte
			for _, p := range queue {
				got = append(got, p...)
			}
			want := bytes.Join([][]byte{bytes.Repeat([]byte(tc.up), 32), bytes.Repeat([]byte(tc.down), 64), []byte("typed-after-scroll\r")}, nil)
			if !bytes.Equal(got, want) {
				t.Fatalf("scroll distance, coordinates, direction or input order changed: %q", got)
			}
			if len(queue) != 3 {
				t.Fatalf("two gestures and typing generated %d writes, want 3", len(queue))
			}
		})
	}
}

func TestScrollBatchKeepsFractionalMovement(t *testing.T) {
	v := scrollFixture(t, "\x1b[?1003h\x1b[?1006h")
	for _, dy := range []float32{7, 8, 17, -7, -9} {
		v.scroll(ui.InputEvent{X: 20, Y: 56, DY: dy, Precise: true})
	}
	want := [][]byte{bytes.Repeat([]byte("\x1b[<65;3;4M"), 2), []byte("\x1b[<64;3;4M")}
	if len(v.t.in.queue) != len(want) {
		t.Fatalf("fractional movement generated %d writes, want %d", len(v.t.in.queue), len(want))
	}
	for i, p := range want {
		if !bytes.Equal(v.t.in.queue[i], p) {
			t.Fatalf("fractional report %d = %q, want %q", i, v.t.in.queue[i], p)
		}
	}
}

func BenchmarkRemoteScrollBurst(b *testing.B) {
	for _, rows := range []int{3, 32, 128} {
		b.Run(fmt.Sprint(rows), func(b *testing.B) {
			v := scrollFixture(b, "\x1b[?1003h\x1b[?1006h")
			ev := ui.InputEvent{X: 20, Y: 56, DY: float32(rows * 16), Precise: true}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				v.scroll(ev)
				clear(v.t.in.queue)
				v.t.in.queue = v.t.in.queue[:0]
			}
		})
	}
}
