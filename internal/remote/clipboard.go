package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/tailscale/tailcat"
	"image/png"
	"io"
	"net"
	"retty/internal/rex"
	"time"
)

// Read only the first protocol line. Subsequent attach bytes remain opaque;
// image payloads are binary and never enter a JSON observer or the old daemon.
func firstProtocolLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > 1<<20 {
			return nil, errors.New("remote request too large")
		}
		line = append(line, part...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
}

func (b *Bridge) serveConnection(conn net.Conn, socket string) {
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReaderSize(conn, 64<<10)
	line, err := firstProtocolLine(reader)
	if err != nil {
		return
	}
	var req rex.ImagePasteRequest
	if json.Unmarshal(line, &req) == nil && req.Op == "paste-image" {
		b.serveImage(conn, reader, req)
		return
	}
	if req.Op == "upload-files" {
		var upload rex.FileUploadRequest
		if len(line) > rex.MaxUploadHeader+1 || json.Unmarshal(line, &upload) != nil {
			_ = json.NewEncoder(conn).Encode(rex.Response{Error: "文件上传清单无效"})
			return
		}
		b.serveFileUpload(conn, reader, socket, upload)
		return
	}
	if req.Op == "notification-claim" {
		b.serveNotice(conn, line)
		return
	}
	if req.Op == "files-browse" {
		b.serveFiles(conn, line)
		return
	}
	if req.Op == "port-forward" {
		b.servePort(conn, reader, line)
		return
	}
	if req.Op == "host-diagnostics" {
		b.serveHostDiagnostics(conn, line)
		return
	}
	if req.Op == "self-update" {
		b.serveUpdate(conn, line)
		return
	}
	conn.SetReadDeadline(time.Time{})
	if req.Op == "agent-approval" {
		b.serveApproval(conn, socket, line)
		return
	}
	local, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return
	}
	defer local.Close()
	observed := b.observe(conn)
	observed.Conn = &prefixedConn{Conn: conn, reader: io.MultiReader(bytes.NewReader(line), reader)}
	tailcat.ProxyConns(observed, local)
}

type prefixedConn struct {
	net.Conn
	reader io.Reader
}

func (c *prefixedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (b *Bridge) serveImage(conn net.Conn, reader io.Reader, req rex.ImagePasteRequest) {
	encoder := json.NewEncoder(conn)
	fail := func(message string) { _ = encoder.Encode(rex.Response{Error: message}) }
	if b.onImage == nil {
		fail("请更新桌面端以支持图片粘贴")
		return
	}
	if req.SID == "" || len(req.SID) > 128 || req.Length <= 0 || req.Length > rex.MaxClipboardImage {
		fail("图片过大或会话无效，请裁剪后重试（最多32MB）")
		return
	}
	select {
	case b.imageGate <- struct{}{}:
		defer func() { <-b.imageGate }()
	default:
		fail("图片正在粘贴，请稍后再试")
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, rex.ImagePasteTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if encoder.Encode(rex.Response{Data: json.RawMessage(`{"ready":true}`)}) != nil {
		return
	}
	image := make([]byte, req.Length)
	if _, err := io.ReadFull(reader, image); err != nil {
		return
	}
	if err := rex.ValidateClipboardImage(image); err != nil {
		fail(err.Error())
		return
	}
	if _, err := png.Decode(bytes.NewReader(image)); err != nil {
		fail("剪贴板图片不完整，请重新复制后再试")
		return
	}
	// A disconnected phone must not apply a late paste to its old session.
	go func() {
		var extra [1]byte
		_, err := reader.Read(extra[:])
		if err != nil {
			cancel()
		}
	}()
	if err := b.onImage(ctx, req.SID, image); err != nil {
		fail(err.Error())
		return
	}
	_ = encoder.Encode(rex.Response{})
}
