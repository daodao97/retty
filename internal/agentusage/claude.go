package agentusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// As in Magpie, /usage is handled by Claude Code itself. No prompt, tools,
// project settings, MCP servers or persisted conversation are involved.
var claudeArgs = []string{"-p", "/usage", "--output-format", "json", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "--no-session-persistence"}

func readClaude(ctx context.Context, binary, home string) ([]Window, error) {
	dir, err := os.MkdirTemp("", "retty-usage-")
	if err != nil {
		return nil, errors.New("无法创建用量读取目录")
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, binary, claudeArgs...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, commandEnv("claude", home), io.Discard
	cmd.WaitDelay = time.Second
	isolateReader(cmd)
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, errors.New("Claude 用量读取失败，请检查 CLI 登录")
	}
	return parseClaude(out.Bytes())
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("usage output too large")
	}
	return b.Buffer.Write(p)
}

var claudeLine = regexp.MustCompile(`^Current (session|week(?: \(([^)]+)\))?):\s*([0-9.]+)% used(?:\s*·\s*resets (.+))?$`)
var ansiUsage = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
var claudeDenied = regexp.MustCompile(`(?i)\b(401|403)\b|not (logged|signed) in|signed out|unauthorized|forbidden|authentication (failed|required)|invalid (access )?token`)

func parseClaude(data []byte) ([]Window, error) {
	type result struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	var r result
	if json.Unmarshal(data, &r) != nil {
		var all []result
		if json.Unmarshal(data, &all) != nil {
			return nil, errors.New("无法识别 Claude 用量数据")
		}
		for _, item := range all {
			if item.Type == "result" {
				r = item
			}
		}
	}
	text := ansiUsage.ReplaceAllString(r.Result, "")
	if r.IsError || claudeDenied.MatchString(text) {
		return nil, errors.New("Claude 未返回额度，请检查 CLI 登录")
	}
	var windows []Window
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		m := claudeLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || m[2] != "" && !strings.EqualFold(m[2], "all models") {
			continue
		}
		name := "5 小时"
		if m[1] != "session" {
			name = "7 天"
		}
		used, err := strconv.ParseFloat(m[3], 64)
		if err != nil || seen[name] {
			continue
		}
		seen[name] = true
		windows = append(windows, Window{Name: name, Used: Percent(used), Reset: m[4]})
	}
	if len(windows) == 0 {
		return nil, errors.New("当前账户未提供订阅额度")
	}
	return windows, nil
}
