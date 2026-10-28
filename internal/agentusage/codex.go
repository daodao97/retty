package agentusage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type codexWindow struct {
	Used  *float64 `json:"usedPercent"`
	Mins  int64    `json:"windowDurationMins"`
	Reset int64    `json:"resetsAt"`
}

type codexLimits struct {
	Plan      string       `json:"planType"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexRPC struct {
	scanner *bufio.Scanner
	enc     *json.Encoder
	id      int
	home    string
}

func readCodex(ctx context.Context, binary, home string) (Reading, error) {
	r := Reading{Agent: "codex", Home: home}
	err := withCodex(ctx, binary, home, func(rpc *codexRPC) error {
		var err error
		r, err = rpc.read()
		return err
	})
	return r, err
}

func withCodex(ctx context.Context, binary, home string, run func(*codexRPC) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Env, cmd.Dir, cmd.Stderr = commandEnv("codex", home), os.TempDir(), io.Discard
	cmd.WaitDelay = time.Second
	isolateReader(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return errors.New("无法启动 Codex 用量读取")
	}
	defer func() { in.Close(); cancel(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	rpc := &codexRPC{scanner: scanner, enc: json.NewEncoder(in), home: home}
	if err := rpc.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "retty_usage", "version": "1.0"}}, nil); err != nil {
		return err
	}
	if err := rpc.enc.Encode(map[string]string{"method": "initialized"}); err != nil {
		return err
	}
	return run(rpc)
}

func (rpc *codexRPC) call(method string, params any, result any) error {
	rpc.id++
	if err := rpc.enc.Encode(map[string]any{"id": rpc.id, "method": method, "params": params}); err != nil {
		return errors.New("无法连接 Codex 用量读取")
	}
	for rpc.scanner.Scan() {
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(rpc.scanner.Bytes(), &reply) != nil || reply.ID != rpc.id {
			continue
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return errors.New("Codex 未返回额度，请检查登录或更新 CLI")
		}
		if result == nil {
			return nil
		}
		if json.Unmarshal(reply.Result, result) != nil {
			return errors.New("无法识别 Codex 用量数据")
		}
		return nil
	}
	return errors.New("Codex 用量读取已断开")
}

func (rpc *codexRPC) read() (Reading, error) {
	r := Reading{Agent: "codex", Home: rpc.home}
	var account struct {
		Account *struct{ Type, Email string } `json:"account"`
	}
	if err := rpc.call("account/read", map[string]bool{"refreshToken": false}, &account); err != nil {
		return r, err
	}
	if account.Account == nil {
		return r, errors.New("请先在 Codex CLI 登录")
	}
	if account.Account.Type != "chatgpt" {
		return r, errors.New("当前登录方式不提供订阅额度")
	}
	var result struct {
		Limits    codexLimits            `json:"rateLimits"`
		ByID      map[string]codexLimits `json:"rateLimitsByLimitId"`
		Resets    *ResetCredits          `json:"rateLimitResetCredits"`
		AccountID string                 `json:"accountId"`
	}
	if err := rpc.call("account/rateLimits/read", nil, &result); err != nil {
		return r, err
	}
	limits := result.Limits
	if general, ok := result.ByID["codex"]; ok {
		limits = general
	}
	r.Plan, r.Windows, r.Resets = limits.Plan, codexWindows(limits), result.Resets
	r.Account = account.Account.Email
	if result.AccountID != "" {
		r.AccountKey = "id:" + result.AccountID
	} else if r.Account != "" {
		r.AccountKey = "email:" + strings.ToLower(r.Account)
	}
	if r.Resets != nil {
		r.Resets.normalize(time.Now())
	}
	r.At = time.Now()
	if len(r.Windows) == 0 && r.Resets == nil {
		return r, errors.New("当前账户未提供订阅额度")
	}
	return r, nil
}

func codexWindows(limits codexLimits) []Window {
	var windows []Window
	for i, w := range []*codexWindow{limits.Primary, limits.Secondary} {
		if w == nil || w.Used == nil {
			continue
		}
		name := []string{"短周期", "长周期"}[i]
		if w.Mins > 0 {
			switch {
			case w.Mins%1440 == 0:
				name = fmt.Sprintf("%d 天", w.Mins/1440)
			case w.Mins%60 == 0:
				name = fmt.Sprintf("%d 小时", w.Mins/60)
			default:
				name = fmt.Sprintf("%d 分钟", w.Mins)
			}
		}
		reset := ""
		if w.Reset > 0 {
			reset = time.Unix(w.Reset, 0).Local().Format("01-02 15:04")
		}
		windows = append(windows, Window{Name: name, Used: Percent(*w.Used), Reset: reset})
	}
	return windows
}
