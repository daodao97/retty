package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"retty/internal/rex"
	"strings"
	"testing"
	"time"
)

func clipboardPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	p := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	p.Set(1, 1, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&b, p); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func clipboardClient(t *testing.T, handler func(context.Context, string, []byte) error) (*rex.Client, *Bridge) {
	t.Helper()
	control, peer := net.Pipe()
	t.Cleanup(func() { control.Close(); peer.Close() })
	b := &Bridge{onImage: handler, imageGate: make(chan struct{}, 1), imageContext: context.Background(), devices: make(map[net.Conn]ConnectedDevice)}
	c := rex.NewClient(control, func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { defer server.Close(); b.serveConnection(server, "/no-session-server-needed") }()
		return client, nil
	})
	t.Cleanup(func() { c.Close() })
	return c, b
}
func TestImagePasteExtensionTransfersBytesWithoutUpgradingDaemon(t *testing.T) {
	data := clipboardPNG(t)
	calls := 0
	c, _ := clipboardClient(t, func(ctx context.Context, sid string, received []byte) error {
		calls++
		if sid != "existing-pane" || !bytes.Equal(data, received) {
			return errors.New("wrong payload")
		}
		return nil
	})
	if err := c.PasteImage(context.Background(), "existing-pane", data); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("image not applied exactly once")
	}
}
func TestImagePasteErrorsNeverApplyOrRetry(t *testing.T) {
	data := clipboardPNG(t)
	c, _ := clipboardClient(t, nil)
	if err := c.PasteImage(context.Background(), "pane", data); err == nil || !strings.Contains(err.Error(), "更新桌面端") {
		t.Fatal("unsupported desktop not reported", err)
	}
	calls := 0
	c, b := clipboardClient(t, func(context.Context, string, []byte) error { calls++; return errors.New("clipboard denied") })
	if err := c.PasteImage(context.Background(), "pane", data); err == nil || err.Error() != "clipboard denied" || calls != 1 {
		t.Fatal("clipboard failure was retried or lost", err, calls)
	}
	if err := c.PasteImage(context.Background(), "pane", []byte("bad image")); err == nil || calls != 1 {
		t.Fatal("invalid image reached desktop")
	}
	b.imageGate <- struct{}{}
	if err := c.PasteImage(context.Background(), "pane", data); err == nil || !strings.Contains(err.Error(), "正在粘贴") || calls != 1 {
		t.Fatal("parallel upload was accepted", err)
	}
	<-b.imageGate
}
func TestImagePasteCancellationDoesNotReplay(t *testing.T) {
	data := clipboardPNG(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	calls := 0
	c, _ := clipboardClient(t, func(ctx context.Context, _ string, _ []byte) error {
		calls++
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.PasteImage(ctx, "pane", data) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled paste succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client ignored cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("desktop callback ignored peer cancellation")
	}
	if calls != 1 {
		t.Fatal("cancelled image was replayed")
	}
}
func TestImageHeadersAreBoundedBeforeReadingBody(t *testing.T) {
	_, b := clipboardClient(t, func(context.Context, string, []byte) error { t.Error("invalid header applied"); return nil })
	for _, length := range []int{-1, 0, rex.MaxClipboardImage + 1} {
		client, server := net.Pipe()
		client.SetDeadline(time.Now().Add(time.Second))
		go func() { defer server.Close(); b.serveConnection(server, "") }()
		json.NewEncoder(client).Encode(rex.ImagePasteRequest{Op: "paste-image", SID: "pane", Length: length})
		var reply rex.Response
		if err := json.NewDecoder(client).Decode(&reply); err != nil || reply.Error == "" {
			t.Fatal("unbounded header accepted", err)
		}
		client.Close()
	}
}
func TestBridgeStillForwardsLegacyControlAndOpaqueTerminalBytes(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "retty-image-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	input := []byte("{\"op\":\"attach\",\"sid\":\"pane\"}\n\x16\x00中文🙂")
	observed := make(chan []byte, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		p := make([]byte, len(input))
		_, e = io.ReadFull(conn, p)
		if e == nil {
			observed <- p
			conn.Write([]byte("ok\n\x1b[2J"))
		}
	}()
	client, server := net.Pipe()
	defer client.Close()
	client.SetDeadline(time.Now().Add(time.Second))
	b := &Bridge{devices: make(map[net.Conn]ConnectedDevice)}
	go func() { defer server.Close(); b.serveConnection(server, socket) }()
	go client.Write(input)
	reply := make([]byte, 7)
	if _, e := io.ReadFull(client, reply); e != nil || string(reply) != "ok\n\x1b[2J" {
		t.Fatal("terminal response changed", e)
	}
	if p := <-observed; !bytes.Equal(p, input) {
		t.Fatal("terminal bytes were altered")
	}
	if len(b.Devices()) != 0 {
		t.Fatal("image/attach connection counted as a phone control connection")
	}
}

func TestTruncatedPNGDoesNotReachDesktopClipboard(t *testing.T) {
	data := clipboardPNG(t)
	truncated := data[:len(data)-8]
	if err := rex.ValidateClipboardImage(truncated); err != nil {
		t.Fatal("fixture must have a complete image header", err)
	}
	calls := 0
	c, _ := clipboardClient(t, func(context.Context, string, []byte) error { calls++; return nil })
	if err := c.PasteImage(context.Background(), "pane", truncated); err == nil || calls != 0 {
		t.Fatal("truncated image applied", err)
	}
}

func TestHeadlessCodexImageUploadUsesFileChannelWithoutClipboardOrPTY(t *testing.T) {
	c, _, _, _ := fileUploadFixture(t)
	data := clipboardPNG(t)
	path, err := c.UploadClipboardImage(context.Background(), "pane", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	received, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, received) {
		t.Fatal("remote PNG differs", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 || filepath.Base(path) != "clipboard.png" {
		t.Fatal("image file is not private", err)
	}
	if _, err := c.UploadClipboardImage(context.Background(), "pane", data[:len(data)-12], nil); err == nil {
		t.Fatal("truncated PNG uploaded")
	}
}
