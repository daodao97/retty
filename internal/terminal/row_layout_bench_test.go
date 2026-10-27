package terminal

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func snapshotScrollFrames(tb testing.TB, unicode bool) [][]byte {
	tb.Helper()
	if err := Load(); err != nil {
		tb.Fatal(err)
	}
	source, err := New(Options{Conn: newPipe(), FixedCols: 48, FixedRows: 33, NoBlink: true})
	if err != nil {
		tb.Fatal(err)
	}
	defer source.Close()
	var frames [][]byte
	for _, offset := range []int{0, 1, 2, 3, 4, 3, 2, 1} {
		source.Feed([]byte("\x1b[?1049h\x1b[H\x1b[2J\x1b[?25l"))
		for y := range 30 {
			id := offset + y
			text := "stable remote transcript / code output"
			if unicode {
				text = "中文 e\u0301 👩‍💻 terminal output"
			}
			source.Feed([]byte(fmt.Sprintf("\x1b[%d;1H\x1b[%dm%05d\x1b[0m %s", y+1, 31+id%6, id, text)))
		}
		source.Feed([]byte("\x1b[32;1H\x1b[48;2;40;40;40m> fixed input area\x1b[K\x1b[0m"))
		frames = append(frames, source.Snapshot())
	}
	return frames
}

// Full authenticated viewport replacement plus UI frame construction at phone
// geometry. This software benchmark does not measure device presentation FPS.
func BenchmarkSnapshotScrollFrame(b *testing.B) {
	for _, unicode := range []bool{false, true} {
		b.Run(fmt.Sprintf("unicode=%v", unicode), func(b *testing.B) {
			frames := snapshotScrollFrames(b, unicode)
			phone, err := New(Options{Conn: newPipe(), FixedCols: 48, FixedRows: 33, NoBlink: true, SmoothScroll: true})
			if err != nil {
				b.Fatal(err)
			}
			defer phone.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, phone).Fill() }, 393, 699)
			tt.SetScale(3)
			for _, frame := range frames {
				phone.restoreSnapshot(frame, 48, 33)
				tt.Frame()
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				phone.restoreSnapshot(frames[i%len(frames)], 48, 33)
				tt.Frame()
				i++
			}
		})
	}
}
