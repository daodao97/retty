// Package diagnostics performs bounded, read-only host checks. It never runs
// hooks or shell startup files, and does not repair or trust configuration.
package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anmitsu/go-shlex"
	"retty/internal/agents"
	"retty/internal/runtimeinfo"
)

type Request struct {
	Op             string `json:"op,omitempty"`
	Agent          string `json:"agent"`
	ConfigDir      string `json:"configDir,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	HookExecutable string `json:"hookExecutable,omitempty"`
}

type PathCheck struct {
	Path   string `json:"path,omitempty"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type Hook struct {
	Event      string     `json:"event"`
	Command    string     `json:"command"`
	Executable PathCheck  `json:"executable"`
	Script     *PathCheck `json:"script,omitempty"`
	Retty      bool       `json:"retty,omitempty"`
}

type Report struct {
	At         time.Time        `json:"at"`
	Build      runtimeinfo.Info `json:"build"`
	Executable string           `json:"executable"`
	CWD        PathCheck        `json:"cwd"`
	Shell      PathCheck        `json:"shell"`
	Config     PathCheck        `json:"config"`
	Hooks      []Hook           `json:"hooks,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`
	Note       string           `json:"note"`
}

func (r Request) Validate() error {
	if r.Agent != "codex" && r.Agent != "claude" && r.Agent != "gemini" && r.Agent != "qwen" {
		return errors.New("unsupported hook agent")
	}
	for _, p := range []string{r.ConfigDir, r.CWD, r.HookExecutable} {
		if len(p) > 4096 || strings.ContainsAny(p, "\x00\r\n") || p != "" && !filepath.IsAbs(p) {
			return errors.New("diagnostic paths must be bounded absolute paths")
		}
	}
	return nil
}

func CheckPath(path string, executable, directory bool) PathCheck {
	r := PathCheck{Path: path, Code: "unknown", Detail: "未提供路径"}
	if path == "" {
		return r
	}
	st, err := os.Stat(path)
	if err != nil {
		r.Code, r.Detail = "unreadable", "无法访问路径"
		if errors.Is(err, os.ErrNotExist) {
			r.Code, r.Detail = "missing", "路径不存在（ENOENT），检查是否改名或移动"
		} else if errors.Is(err, os.ErrPermission) {
			r.Code, r.Detail = "permission", "无权访问路径"
		}
		return r
	}
	if directory && !st.IsDir() || !directory && !st.Mode().IsRegular() {
		r.Code, r.Detail = "wrong_type", "路径类型不符"
		return r
	}
	if executable && st.Mode().Perm()&0111 == 0 {
		r.Code, r.Detail = "not_executable", "文件没有执行权限"
		return r
	}
	r.Code, r.Detail = "ok", "路径可用"
	return r
}

func Collect(ctx context.Context, req Request) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if err := req.Validate(); err != nil {
		return Report{}, err
	}
	r := Report{At: time.Now(), Build: runtimeinfo.Current(), CWD: CheckPath(req.CWD, false, true), Shell: CheckPath("/bin/sh", true, false), Note: "只读配置检查，未执行任何 Hook；路径可用不代表 Hook 已成功运行。启动前失败的 Hook 不会产生 Retty 事件。仅检查所选目录的 hooks.json/settings.json，不汇总项目、内联 TOML、插件或托管 Hook。"}
	r.Executable, _ = os.Executable()
	if resolved, err := filepath.EvalSymlinks(r.Executable); err == nil {
		r.Executable = resolved
	}
	dir := req.ConfigDir
	if dir == "" {
		var err error
		dir, err = agents.ConfigDir(req.Agent)
		if err != nil {
			return r, err
		}
	}
	file := "settings.json"
	if req.Agent == "codex" {
		file = "hooks.json"
	}
	r.Config = CheckPath(filepath.Join(dir, file), false, false)
	if r.Config.Code != "ok" {
		return r, nil
	}
	f, err := os.Open(r.Config.Path)
	if err != nil {
		r.Config.Code, r.Config.Detail = "unreadable", "无法读取 Hook 配置"
		return r, nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		r.Config.Code, r.Config.Detail = "too_large", "无法读取配置或配置超过 1 MiB"
		return r, nil
	}
	var root struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(data, &root) != nil || root.Hooks == nil {
		r.Config.Code, r.Config.Detail = "invalid", "Hook 配置不是有效的 hooks 对象"
		return r, nil
	}
	events := make([]string, 0, len(root.Hooks))
	for event := range root.Hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		var groups []struct {
			Hooks []struct{ Type, Command string }
		}
		if len(event) > 128 || json.Unmarshal(root.Hooks[event], &groups) != nil {
			r.Config.Code, r.Config.Detail = "invalid", "Hook 事件格式无效"
			continue
		}
		for _, group := range groups {
			for _, item := range group.Hooks {
				if item.Type != "command" {
					continue
				}
				if len(r.Hooks) == 128 || len(item.Command) > 4096 {
					r.Truncated = true
					continue
				}
				h := Hook{Event: event, Command: item.Command, Executable: PathCheck{Code: "dynamic", Detail: "动态或复合命令，未执行"}}
				if item.Command == agents.HookCommand(req.Agent) {
					h.Retty = true
					h.Executable = CheckPath(req.HookExecutable, true, false)
					if req.HookExecutable == "" {
						h.Executable.Detail = "未取得会话 RETTY_HOOK 路径"
					}
				} else {
					inspectCommand(&h, req.CWD)
				}
				r.Hooks = append(r.Hooks, h)
			}
		}
	}
	return r, nil
}

func inspectCommand(h *Hook, cwd string) {
	words, err := shlex.Split(h.Command, true)
	if err != nil || len(words) == 0 {
		return
	}
	first := words[0]
	// Shell syntax is not evaluated or treated as a missing binary.
	if strings.ContainsAny(first, "$`=;|&()<>\\") || first == "if" || first == "then" || first == "env" || first == "cd" || first == "exec" || first == "." || first == "source" {
		return
	}
	path := first
	if !filepath.IsAbs(path) {
		if strings.ContainsRune(path, '/') {
			if cwd == "" {
				return
			}
			path = filepath.Join(cwd, path)
		} else {
			var err error
			path, err = exec.LookPath(path)
			if err != nil {
				h.Executable = PathCheck{Path: first, Code: "unknown", Detail: "诊断进程 PATH 中未找到；Agent 的 PATH 可能不同"}
				return
			}
		}
	}
	h.Executable = CheckPath(path, true, false)
	if !filepath.IsAbs(first) && !strings.ContainsRune(first, '/') && h.Executable.Code == "ok" {
		h.Executable.Detail = "诊断进程 PATH 可解析；Agent 的 PATH 可能不同"
	}
	base := filepath.Base(path)
	if len(words) < 2 || (base != "sh" && base != "bash" && base != "zsh" && base != "python" && base != "python3" && base != "node") {
		return
	}
	script := words[1]
	if strings.HasPrefix(script, "-") || strings.ContainsAny(script, "$`;|&()<>\\") {
		return
	}
	if !filepath.IsAbs(script) {
		if cwd == "" {
			return
		}
		script = filepath.Join(cwd, script)
	}
	check := CheckPath(script, false, false)
	h.Script = &check
}
