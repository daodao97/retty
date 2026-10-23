package terminal

import (
	"context"
	"image"
	imagecolor "image/color"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestImageHoverDelayCancellationAndLateResponse(t *testing.T) {
	loadLib(t)
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{})
	release := make(chan struct{})
	var loaded atomic.Int32
	term, err := New(Options{Conn: newPipe(), OnPreviewLink: func(link Link) *LinkPreviewSource {
		if link.Path == "" {
			return nil
		}
		return &LinkPreviewSource{Key: link.Path, Label: link.Path, Load: func(ctx context.Context) (image.Image, error) {
			loaded.Add(1)
			if link.Path == "first.png" {
				close(firstStarted)
				<-ctx.Done()
				close(firstCanceled)
				<-release
			}
			img := image.NewNRGBA(image.Rect(0, 0, 24, 12))
			img.Set(0, 0, imagecolor.NRGBA{200, 50, 20, 255})
			return img, nil
		}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 600, 300)
	term.Feed([]byte("\x1b[Hfirst.png\r\nsecond.png"))
	tt.Frame()
	v := term.v
	point := func(row int) (float32, float32) {
		return float32(v.ox+3*v.cellW) / v.scale, float32(v.oy+row*v.cellH+v.cellH/2) / v.scale
	}
	x, y := point(0)
	tt.Move(x, y)
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	if loaded.Load() != 0 {
		t.Fatal("hover read before delay")
	}
	tt.Advance(200 * time.Millisecond)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("hover did not load without Command")
	}
	x, y = point(1)
	tt.Move(x, y)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("moving to another path did not cancel")
	}
	waitFor(t, tt, "second hover", func() bool {
		return v.preview != nil && v.preview.source.Key == "second.png" && v.preview.bitmap != nil
	})
	close(release)
	// The first loader deliberately returns success after cancellation.
	time.Sleep(10 * time.Millisecond)
	tt.Frame()
	if v.preview.source.Key != "second.png" || !tt.HasText("second.png") || tt.HasText("first.png") {
		t.Fatal("late response replaced current hover")
	}
	term.CancelLinkPreview()
	if v.preview.ctx.Err() == nil {
		t.Fatal("owner cancellation did not reach loader")
	}
	tt.Move(580, 280)
	tt.Frame()
	if v.preview != nil {
		t.Fatal("leaving a path retained its preview")
	}
}
