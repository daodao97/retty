package terminal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Supplemental workloads keep unchanged paths visible as controls; these use
// the software tester and do not report hardware display FPS.
func BenchmarkFrameWorkload(b *testing.B) {
	for _, kind := range []string{"idle", "single-cell", "unicode"} {
		b.Run(kind, func(b *testing.B) {
			if err := Load(); err != nil {
				b.Fatal(err)
			}
			term, err := New(Options{Conn: newPipe(), NoBlink: true})
			if err != nil {
				b.Fatal(err)
			}
			defer term.Close()
			tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 1000, 620)
			term.Feed([]byte("\x1b[?25l" + strings.Repeat("colored 中文🙂 e\u0301\r\n", 80)))
			tt.Frame()
			b.ReportAllocs()
			var n int
			for b.Loop() {
				n++
				switch kind {
				case "single-cell":
					term.Feed([]byte(fmt.Sprintf("\x1b[H%d", n%10)))
				case "unicode":
					term.Feed([]byte(fmt.Sprintf("\x1b[32m中文🙂 e\u0301 %d\x1b[0m\r\n", n)))
				}
				tt.Frame()
			}
		})
	}
}

func BenchmarkImageReplacementFrame(b *testing.B) {
	if err := Load(); err != nil {
		b.Fatal(err)
	}
	term, err := New(Options{Conn: newPipe(), NoBlink: true})
	if err != nil {
		b.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 600, 240)
	payloads := [2]string{}
	for i := range payloads {
		pixels := bytes.Repeat([]byte{byte(80 + i*80), 90, 180, 255}, 40*20)
		payloads[i] = fmt.Sprintf("\x1b[2;3H\x1b_Ga=T,f=32,s=40,v=20,i=11,p=1,c=20,r=7,C=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString(pixels))
	}
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		term.Feed([]byte(payloads[n%2]))
		tt.Frame()
		if len(term.v.images) != 1 || term.v.images[0].bmp == nil {
			b.Fatal("image workload did not render")
		}
	}
}

func BenchmarkSearchHistory(b *testing.B) {
	if err := Load(); err != nil {
		b.Fatal(err)
	}
	term, err := New(Options{Conn: newPipe(), NoBlink: true, FixedCols: 120, FixedRows: 38})
	if err != nil {
		b.Fatal(err)
	}
	defer term.Close()
	term.Feed([]byte(strings.Repeat("\x1b[32mneedle-alpha needle-beta 中文🙂\x1b[0m\r\n", 10000)))
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		if err := term.SetSearch([]string{"needle-alpha", "needle-beta"}[n%2]); err != nil {
			b.Fatal(err)
		}
		for {
			state := term.SearchState()
			if state.Err != nil {
				b.Fatal(state.Err)
			}
			if !state.Busy {
				if state.Total == 0 {
					b.Fatal("history search lost matches")
				}
				break
			}
		}
	}
}
