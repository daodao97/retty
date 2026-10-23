package main

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/rex"
	"retty/internal/terminal"
)

func TestKittyOwnedServiceLegacyAndSynchronizedReconnect(t *testing.T) {
	registerFonts()
	client, _ := desktopFixtureClient(t)
	hello, err := client.Hello()
	if err != nil || !hello.ScreenGraphics {
		t.Fatal("service omitted graphics capability", hello, err)
	}
	sequence := fmt.Sprintf("\x1b[2;3H\x1b_Ga=T,f=32,s=1,v=1,i=17,p=1,c=10,r=4,C=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString([]byte{215, 90, 45, 255}))
	job, err := client.Create(rex.CreateOptions{Dir: t.TempDir(), Cols: 40, Rows: 10, Command: []string{"/bin/sh", "-c", "printf '%s' \"$1\"; sleep 0.4", "owned-image", sequence}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Kill(job.ID)
	for _, stateSync := range []bool{false, true} {
		if stateSync {
			deadline := time.Now().Add(2 * time.Second)
			exited := false
			for time.Now().Before(deadline) {
				all, _ := client.List()
				for _, s := range all {
					if s.ID == job.ID && s.Exited {
						exited = true
					}
				}
				if exited {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !exited {
				t.Fatal("owned image process did not finish")
			}
		}
		stream := client.ViewStream(job.ID)
		if stateSync {
			stream.PreferScreenSync(false)
		}
		term, err := terminal.New(terminal.Options{Conn: stream, Font: termFont, FixedCols: 40, FixedRows: 10, FitToView: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { term.Close(); stream.Close() })
		tt := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Fill() }, 500, 240)
		waitFor(t, tt, "Kitty pixels from owned service", func() bool {
			frame := tt.Image()
			count := 0
			for y := 0; y < frame.Bounds().Dy(); y++ {
				for x := 0; x < frame.Bounds().Dx(); x++ {
					r, g, b, a := frame.At(x, y).RGBA()
					if a == 65535 && r>>8 == 215 && g>>8 == 90 && b>>8 == 45 {
						count++
					}
				}
			}
			return count > 100
		})
		if stateSync {
			saveSettingsImage(t, tt, "kitty-service-reconnect")
		}
		term.Close()
		stream.Close()
	}
	all, _ := client.List()
	for _, s := range all {
		if s.ID == job.ID && (s.PID != job.PID || s.Cols != 40 || s.Rows != 10) {
			t.Fatal("graphics viewer changed source identity or size", s)
		}
	}
}
