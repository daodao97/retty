package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"retty/internal/files"
	"retty/internal/rex"
	"retty/internal/terminal"
)

func TestDesktopImageHoverUsesPaneDirectoryAndRenders(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 160, 100))
	for y := range 100 {
		for x := range 160 {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y * 2), 160, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	os.WriteFile(filepath.Join(dir, "hover-image.png"), b.Bytes(), 0600)
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.terminal().info.Dir = dir
	source := a.terminalImagePreview(p, terminal.Link{Path: "hover-image.png"})
	if source == nil {
		t.Fatal("no image source")
	}
	got, err := source.Load(context.Background())
	if err != nil || got.Bounds() != img.Bounds() {
		t.Fatal(got, err)
	}
	p.terminal().term.Feed([]byte("\x1b[H\x1b[2Jhover-image.png\r\nAgent output remains selectable"))
	tt.Frame()
	r, ok := tt.Find("Terminal")
	if !ok {
		t.Fatal("no terminal")
	}
	tt.Move(r.X+30, r.Y+10)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	waitFor(t, tt, "image hover", func() bool { _, ok := tt.Find("终端悬停图片"); return ok && tt.HasText("hover-image.png") })
	tt.Frame()
	saveSettingsImage(t, tt, "terminal-image-hover")
	if !tt.Focused("Terminal") {
		tt.ClickAt(r.X+400, r.Y+50)
	}
	if source := a.terminalImagePreview(p, terminal.Link{URL: "https://example.com/image.png"}); source != nil {
		t.Fatal("hover fetched HTTP URL")
	}
	p.terminal().info.Program = "ssh"
	if source := a.terminalImagePreview(p, terminal.Link{Path: "hover-image.png"}); source != nil {
		t.Fatal("plain ssh path read local file")
	}
	p.terminal().info.Program = ""
	// File workspaces hide the terminal, canceling an in-flight read.
	canceled := make(chan struct{})
	started := make(chan struct{})
	p.terminal().term.Close()
	p.terminal().term, err = terminal.New(terminal.Options{Conn: nopConn{}, OnPreviewLink: func(link terminal.Link) *terminal.LinkPreviewSource {
		return &terminal.LinkPreviewSource{Key: "fixture", Load: func(ctx context.Context) (image.Image, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.terminal().term.Close() })
	p.terminal().term.Feed([]byte("\x1b[Hhover-image.png"))
	tt.Frame()
	tt.Move(r.X+30, r.Y+10)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fixture hover did not start")
	}
	a.toggleFiles()
	a.tab().Zoom = a.tab().Focus
	a.cancelHiddenImagePreviews(false)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("hiding terminal did not cancel hover")
	}
}

func TestDesktopImageHoverRemoteReadsCapturedHostAndConnection(t *testing.T) {
	// The same path exists locally in red. Only the pane's captured remote
	// transport is allowed to supply the blue preview.
	dir := t.TempDir()
	path := filepath.Join(dir, "same.png")
	red := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	red.Set(0, 0, color.NRGBA{220, 0, 0, 255})
	var local bytes.Buffer
	png.Encode(&local, red)
	os.WriteFile(path, local.Bytes(), 0600)
	blue := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	blue.Set(0, 0, color.NRGBA{0, 0, 220, 255})
	var remote bytes.Buffer
	png.Encode(&remote, blue)
	control, peer := net.Pipe()
	defer peer.Close()
	c := rex.NewClient(control, func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			var req files.Request
			if json.NewDecoder(server).Decode(&req) != nil {
				return
			}
			if req.Path != path {
				t.Error("remote path changed", req.Path)
			}
			r := files.Result{Entry: &files.Entry{Regular: true, Size: int64(remote.Len()), Version: "remote-version"}}
			if req.Kind == "read" {
				if req.Version != "remote-version" {
					t.Error("remote version not pinned")
				}
				r.Data = remote.Bytes()
			}
			data, _ := json.Marshal(r)
			json.NewEncoder(server).Encode(rex.Response{Data: data})
		}()
		return client, nil
	})
	defer c.Close()
	h := &desktopHost{client: c}
	p := &Pane{host: h, content: &terminalPane{info: rex.SessionInfo{Dir: dir}}}
	a := &App{}
	source := a.terminalImagePreview(p, terminal.Link{Path: "same.png"})
	if source == nil {
		t.Fatal("missing remote source")
	}
	h.client = nil // a reconnect must not redirect an already-captured read.
	img, err := source.Load(context.Background())
	if err != nil || color.NRGBAModel.Convert(img.At(0, 0)) != blue.At(0, 0) {
		t.Fatal("remote hover fell back to local bytes", img, err)
	}
	if a.terminalImagePreview(p, terminal.Link{Path: "same.png"}) != nil {
		t.Fatal("disconnected host created a new local reader")
	}
}
