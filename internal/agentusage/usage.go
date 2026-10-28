// Package agentusage reads the current local CLI account's subscription usage.
// It starts no conversations and keeps neither credentials nor usage on disk.
package agentusage

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"retty/internal/agents"
)

type Window struct {
	Name  string
	Used  float64
	Reset string
}

type Reading struct {
	Agent   string
	Plan    string
	Windows []Window
	At      time.Time
	Error   string
	Resets  *ResetCredits
	// Identity and config location are held in memory to bind a confirmed reset
	// to the account that was displayed, even if the CLI signs in elsewhere.
	AccountKey string
	Account    string
	Home       string
}

func Percent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Max(0, math.Min(100, v))
}

// Read uses the CLI's own credential handling, including Keychain sign-ins.
// The short-lived Codex server is independent of the shared session daemon.
func Read(ctx context.Context, agent string) Reading {
	r := Reading{Agent: agent}
	binary, err := findBinary(agent)
	if err != nil {
		r.Error = "未找到 " + agent + " 命令"
		return r
	}
	dir, err := agents.ConfigDir(agent)
	if err != nil {
		r.Error = "无法读取本机配置目录"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch agent {
	case "codex":
		r, err = readCodex(ctx, binary, dir)
	case "claude":
		r.Windows, err = readClaude(ctx, binary, dir)
	default:
		err = errors.New("不支持的 Agent")
	}
	if ctx.Err() != nil {
		err = errors.New("读取超时，稍后重试")
	}
	if err != nil {
		r.Error, r.Windows = err.Error(), nil
	}
	r.At = time.Now()
	return r
}

// GUI launches have a smaller PATH than login shells. Probe ordinary install
// locations without sourcing shell startup files or invoking a shell.
func findBinary(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range installDirs() {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

func installDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".npm-global", "bin"), "/opt/homebrew/bin", "/usr/local/bin"}
}

// Finding an npm launcher by absolute path is insufficient: its env shebang
// still needs node. Give it the same install locations we searched for it.
func executionPath() string {
	var dirs []string
	seen := make(map[string]bool)
	for _, dir := range append(filepath.SplitList(os.Getenv("PATH")), installDirs()...) {
		if filepath.IsAbs(dir) && !seen[dir] {
			dirs = append(dirs, dir)
			seen[dir] = true
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func commandEnv(agent, dir string) []string {
	key := agents.ConfigEnv(agent)
	var env []string
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if name == key || name == "PATH" || strings.HasPrefix(name, "RETTY_") || name == "CLAUDECODE" || name == "CLAUDE_CODE_ENTRYPOINT" {
			continue
		}
		env = append(env, e)
	}
	return append(env, key+"="+dir, "PATH="+executionPath(), "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1")
}
