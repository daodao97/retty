package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

var ErrAutomationsUnsupported = errors.New("请更新主机的桌面端或 CLI 网关以查看自动化")

const maxAutomationsResponse = 4 << 20

type automationRequest struct {
	Op string `json:"op"`
	ID string `json:"id,omitempty"`
}

type AutomationEntry struct {
	Task automation.Automation `json:"task"`
	Run  automation.Run        `json:"run"`
}

// AutomationsSnapshot is read from the gateway host's schedule files. History
// is returned only for the selected task, newest first.
type AutomationsSnapshot struct {
	At      time.Time         `json:"at"`
	Entries []AutomationEntry `json:"entries"`
	History []automation.Run  `json:"history,omitempty"`
}

func (b *Bridge) serveAutomations(conn net.Conn, line []byte) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(b.imageContext, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var req automationRequest
	res := rex.Response{}
	var snapshot AutomationsSnapshot
	switch {
	case len(line) > 1024 || json.Unmarshal(line, &req) != nil || len(req.ID) > 128:
		res.Error = "无效的自动化请求"
	case b.automationDir == "":
		res.Error = "主机未配置自动化目录"
	default:
		list, err := automation.List(b.automationDir)
		if err != nil {
			res.Error = "无法读取主机自动化"
			break
		}
		snapshot.At = time.Now()
		found := req.ID == ""
		for _, task := range list {
			snapshot.Entries = append(snapshot.Entries, AutomationEntry{Task: task, Run: automation.LastRun(b.automationDir, task.ID)})
			if task.ID == req.ID {
				found = true
				snapshot.History = automation.History(b.automationDir, task.ID, 50)
			}
		}
		if !found {
			res.Error = "自动化已不存在，请返回列表刷新"
			break
		}
		res.Data, _ = json.Marshal(snapshot)
		if len(res.Data) > maxAutomationsResponse-4096 {
			res = rex.Response{Error: "自动化记录过大，无法读取"}
		}
	}
	// As with file browsing, let the requester close this short-lived stream.
	if json.NewEncoder(conn).Encode(res) == nil {
		var extra [1]byte
		conn.Read(extra[:])
	}
}

// ReadAutomations uses an authenticated extension; it never starts a task or
// sends a new operation to the session daemon.
func ReadAutomations(ctx context.Context, client *rex.Client, id string) (AutomationsSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return AutomationsSnapshot{}, err
	}
	defer func() { conn.SetDeadline(time.Time{}); conn.Close() }()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(automationRequest{Op: "automations-read", ID: id}); err != nil {
		return AutomationsSnapshot{}, err
	}
	var res rex.Response
	if err = json.NewDecoder(io.LimitReader(conn, maxAutomationsResponse)).Decode(&res); err != nil {
		if ctx.Err() != nil {
			return AutomationsSnapshot{}, ctx.Err()
		}
		return AutomationsSnapshot{}, fmt.Errorf("无法读取自动化，请确认主机连接后重试: %w", err)
	}
	if res.Error != "" {
		if strings.Contains(res.Error, "unknown op") {
			return AutomationsSnapshot{}, ErrAutomationsUnsupported
		}
		return AutomationsSnapshot{}, errors.New(res.Error)
	}
	var snapshot AutomationsSnapshot
	if json.Unmarshal(res.Data, &snapshot) != nil || snapshot.At.IsZero() {
		return AutomationsSnapshot{}, errors.New("主机未返回有效的自动化数据")
	}
	return snapshot, nil
}
