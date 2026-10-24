package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"retty/internal/files"
	"retty/internal/rex"
	"strings"
	"time"
)

var ErrFileBrowseUnsupported = errors.New("请更新远端桌面端或 CLI 网关以支持文件浏览")
var ErrGitReviewUnsupported = errors.New("请更新远端桌面端或 CLI 网关以支持 Git 状态和轮次 Diff；无需重启会话服务")

func (b *Bridge) serveFiles(conn net.Conn, line []byte) {
	b.mu.Lock()
	if b.filesGate == nil {
		b.filesGate = make(chan struct{}, 4)
	}
	gate := b.filesGate
	b.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	default:
		_ = json.NewEncoder(conn).Encode(rex.Response{Error: "文件请求繁忙，请稍后重试"})
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	// gVisor's deadline callbacks otherwise keep their channels until the
	// timeout, even after a short-lived file connection has closed.
	defer conn.SetDeadline(time.Time{})
	go func() { var extra [1]byte; _, _ = conn.Read(extra[:]); cancel() }()
	var req files.Request
	var result files.Result
	err := json.Unmarshal(line, &req)
	if len(line) > 16384 {
		err = errors.New("文件请求过大")
	}
	if err == nil {
		result, err = files.Do(files.WithSnapshotStore(ctx, filepath.Join(rex.Dir(), "git-snapshots")), req)
	}
	res := rex.Response{}
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Data, _ = json.Marshal(result)
	}
	if err := json.NewEncoder(conn).Encode(res); err == nil {
		// Let the requester close after decoding the complete response. An
		// active server close leaves TIME-WAIT on this host while the peer
		// can immediately reuse its source port for the next file request.
		// The reader above cancels on peer close, and the deadline still
		// bounds clients that stop reading or never close their connection.
		<-ctx.Done()
	}
}

// BrowseFiles uses a separate authenticated stream and never attaches or
// resizes the terminal. The old daemon does not need to know these operations.
func BrowseFiles(ctx context.Context, client *rex.Client, req files.Request) (files.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return files.Result{}, err
	}
	defer func() {
		conn.SetDeadline(time.Time{})
		conn.Close()
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	req.Op = "files-browse"
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		return files.Result{}, err
	}
	var res rex.Response
	if err = json.NewDecoder(io.LimitReader(conn, 7<<20)).Decode(&res); err != nil {
		if ctx.Err() != nil {
			return files.Result{}, ctx.Err()
		}
		return files.Result{}, fmt.Errorf("读取文件响应失败: %w", err)
	}
	if res.Error != "" {
		if strings.HasPrefix(req.Kind, "git-") && res.Error == "不支持的文件操作" {
			return files.Result{}, ErrGitReviewUnsupported
		}
		if strings.Contains(res.Error, "unknown op") {
			return files.Result{}, ErrFileBrowseUnsupported
		}
		return files.Result{}, errors.New(res.Error)
	}
	var result files.Result
	err = json.Unmarshal(res.Data, &result)
	return result, err
}
