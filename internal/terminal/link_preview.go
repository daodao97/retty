package terminal

import (
	"context"
	"image"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// LinkPreviewSource is prepared on the UI thread. Load runs in a worker and
// must use captured host/path/transport values, returning a bounded image.
// Key identifies all of those values; changing it cancels the previous load.
type LinkPreviewSource struct {
	Key, Label string
	Load       func(context.Context) (image.Image, error)
}

const linkPreviewDelay = 350 * time.Millisecond

var previewWorkers = make(chan struct{}, 2)

type linkPreview struct {
	source  LinkPreviewSource
	since   time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	started bool       // UI thread
	bitmap  *ui.Bitmap // guarded by mu; created on UI thread
	mu      sync.Mutex
	image   image.Image
	err     error
	loaded  bool
}

// CancelLinkPreview cancels hover without changing terminal contents or input.
func (t *Terminal) CancelLinkPreview() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelLinkPreviewLocked()
}

func (t *Terminal) cancelLinkPreviewLocked() {
	if state := t.previewState; state != nil {
		state.cancel()
		state.mu.Lock()
		state.image, state.bitmap = nil, nil
		state.mu.Unlock()
		t.previewState = nil
	}
}

func (v *view) previewLink(c *ui.Context, e ui.Element, link Link, x, y float32) {
	t := v.t
	if t.opts.OnPreviewLink == nil {
		return
	}
	var source *LinkPreviewSource
	if link.URL != "" || link.Path != "" {
		source = t.opts.OnPreviewLink(link)
	}
	if source == nil || source.Key == "" || source.Load == nil {
		if v.preview != nil {
			t.CancelLinkPreview()
			v.preview = nil
		}
		return
	}
	if v.preview == nil || v.preview.source.Key != source.Key || v.preview.ctx.Err() != nil {
		if v.preview != nil {
			t.CancelLinkPreview()
		}
		ctx, cancel := context.WithCancel(context.Background())
		v.preview = &linkPreview{source: *source, since: c.Now(), ctx: ctx, cancel: cancel}
		t.mu.Lock()
		if t.closed {
			cancel()
		} else {
			t.previewState = v.preview
		}
		t.mu.Unlock()
	}
	state := v.preview
	if state.ctx.Err() != nil {
		return
	}
	if remaining := linkPreviewDelay - c.Now().Sub(state.since); remaining > 0 {
		c.After(remaining)
		return
	}
	if !state.started {
		state.started = true
		invalidate := c.Services().Invalidate
		go func() {
			select {
			case <-state.ctx.Done():
				return
			case previewWorkers <- struct{}{}:
			}
			defer func() { <-previewWorkers }()
			img, err := state.source.Load(state.ctx)
			state.mu.Lock()
			if state.ctx.Err() == nil {
				state.image, state.err, state.loaded = img, err, true
			}
			state.mu.Unlock()
			invalidate()
		}()
	}
	state.mu.Lock()
	loaded, img, err := state.loaded, state.image, state.err
	if loaded && err == nil && img != nil && state.bitmap == nil {
		state.bitmap = ui.NewBitmap(img)
	}
	bitmap := state.bitmap
	state.mu.Unlock()
	// Hover failures stay quiet; activating the link still has normal errors.
	if loaded && bitmap == nil || state.ctx.Err() != nil {
		return
	}
	root := c.Root().Bounds()
	bounds := e.Bounds()
	w, h := float32(300), float32(220)
	if root.W < 340 {
		w = max(root.W-24, 80)
	}
	if bitmap != nil {
		b := img.Bounds()
		h = min(float32(b.Dy())*w/float32(max(b.Dx(), 1)), 240) + 36
	}
	left := max(float32(8), min(bounds.X+x+16, root.W-w-8))
	top := bounds.Y + y + 24
	if top+h > root.H-8 {
		top = max(float32(8), bounds.Y+y-h-12)
	}
	ui.Overlay(c, func() {
		ui.Column(c.Key("terminal-image-hover")).Absolute().Left(left).Top(top).Size(w, h).Padding(8).Gap(6).Radius(10).
			Background(ui.Hex("#202227")).Border(1, ui.Hex("#4b4d55")).Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, .25)).PassThrough().Role(ui.RoleTooltip).Label("终端图片预览").Children(func() {
			if bitmap != nil {
				ui.Image(c, bitmap).Fit(ui.Contain).Grow(1).FillWidth().MinHeight(0).Label("终端悬停图片")
			} else {
				ui.Box(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(16, 16) })
			}
			ui.Text(c, state.source.Label).FontSize(11).TextColor(ui.Hex("#ececf0")).SingleLine().Ellipsis("…").FillWidth()
		})
	})
}
