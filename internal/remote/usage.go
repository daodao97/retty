package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"retty/internal/agentusage"
	"retty/internal/rex"
)

type usageRequest struct {
	Op      string `json:"op"`
	Refresh bool   `json:"refresh,omitempty"`
}

type usageFlight struct {
	done  chan struct{}
	value agentusage.Snapshot
	err   error
}

func (b *Bridge) usageSnapshot(ctx context.Context, refresh bool) (agentusage.Snapshot, error) {
	b.mu.Lock()
	age := time.Since(b.usageValue.At)
	if !b.usageValue.At.IsZero() && (age < 3*time.Minute && !refresh || age < 10*time.Second) {
		value := b.usageValue
		b.mu.Unlock()
		return value, nil
	}
	pending := b.usagePending
	if pending == nil {
		pending = &usageFlight{done: make(chan struct{})}
		b.usagePending = pending
		go func() {
			read := b.usageRead
			if read == nil {
				read = agentusage.CollectSnapshot
			}
			work, cancel := context.WithTimeout(b.imageContext, 22*time.Second)
			defer cancel()
			value := read(work)
			err := work.Err()
			if err == nil && value.At.IsZero() {
				err = errors.New("empty usage snapshot")
			}
			b.mu.Lock()
			if err == nil {
				b.usageValue = value
			}
			pending.value, pending.err = value, err
			b.usagePending = nil
			close(pending.done)
			b.mu.Unlock()
		}()
	}
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return agentusage.Snapshot{}, ctx.Err()
	case <-pending.done:
		return pending.value, pending.err
	}
}
func (b *Bridge) serveUsage(conn net.Conn, line []byte) {
	var req usageRequest
	if len(line) > 1024 || json.Unmarshal(line, &req) != nil {
		json.NewEncoder(conn).Encode(rex.Response{Error: "无效用量请求"})
		return
	}
	ctx, cancel := context.WithTimeout(b.imageContext, 24*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(24 * time.Second))
	value, err := b.usageSnapshot(ctx, req.Refresh)
	res := rex.Response{}
	if err != nil {
		res.Error = "主机用量读取超时，请稍后重试"
	} else {
		res.Data, _ = json.Marshal(value)
	}
	json.NewEncoder(conn).Encode(res)
}

func ReadUsage(ctx context.Context, client *rex.Client, refresh bool) (agentusage.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return agentusage.Snapshot{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(usageRequest{Op: "agent-usage", Refresh: refresh}); err != nil {
		return agentusage.Snapshot{}, err
	}
	var res rex.Response
	if err = json.NewDecoder(io.LimitReader(conn, (1<<20)+1)).Decode(&res); err != nil {
		return agentusage.Snapshot{}, errors.New("无法读取主机用量，请确认连接后重试")
	}
	if res.Error != "" {
		if res.Error == "主机用量读取超时，请稍后重试" {
			return agentusage.Snapshot{}, errors.New(res.Error)
		}
		return agentusage.Snapshot{}, errors.New("远端网关尚不支持用量读取，请更新 GUI 或 CLI 网关")
	}
	var value agentusage.Snapshot
	if json.Unmarshal(res.Data, &value) != nil || value.At.IsZero() || len(value.Readings) > 16 {
		return value, errors.New("主机未返回有效用量数据")
	}
	return value, nil
}
