package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"retty/internal/diagnostics"
	"retty/internal/rex"
)

func (b *Bridge) serveHostDiagnostics(conn net.Conn, line []byte) {
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
		json.NewEncoder(conn).Encode(rex.Response{Error: "诊断请求繁忙"})
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	var req diagnostics.Request
	var report diagnostics.Report
	var err error
	if len(line) > 16384 || json.Unmarshal(line, &req) != nil {
		err = errors.New("无效诊断请求")
	} else {
		report, err = diagnostics.Collect(ctx, req)
	}
	res := rex.Response{}
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Data, _ = json.Marshal(report)
	}
	json.NewEncoder(conn).Encode(res)
}

// DiagnoseHost reads the owning gateway's configuration on a separate
// authenticated stream, independent of the session daemon and PTY.
func DiagnoseHost(ctx context.Context, client *rex.Client, req diagnostics.Request) (diagnostics.Report, error) {
	if err := req.Validate(); err != nil {
		return diagnostics.Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return diagnostics.Report{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	d, _ := ctx.Deadline()
	conn.SetDeadline(d)
	req.Op = "host-diagnostics"
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		return diagnostics.Report{}, err
	}
	var res rex.Response
	if err = json.NewDecoder(io.LimitReader(conn, (1<<20)+1)).Decode(&res); err != nil {
		return diagnostics.Report{}, err
	}
	if res.Error != "" {
		return diagnostics.Report{}, errors.New("无法读取主机诊断，请确认远端网关支持该功能；无需重启会话服务：" + res.Error)
	}
	var report diagnostics.Report
	err = json.Unmarshal(res.Data, &report)
	return report, err
}
