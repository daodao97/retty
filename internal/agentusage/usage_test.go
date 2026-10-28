package agentusage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test executable acts as both CLIs, checking that collectors only send
// account reads and /usage. No installed CLI or real account is touched.
func TestMain(m *testing.M) {
	if mode := os.Getenv("USAGE_TEST_HELPER"); mode != "" {
		if mode == "tree-child" {
			_ = os.WriteFile(os.Getenv("USAGE_TREE_READY"), []byte("ready"), 0600)
			time.Sleep(600 * time.Millisecond)
			_ = os.WriteFile(os.Getenv("USAGE_TREE_SURVIVED"), []byte("survived"), 0600)
			os.Exit(0)
		}
		if mode == "tree" {
			child := exec.Command(os.Args[0])
			child.Env = append(os.Environ(), "USAGE_TEST_HELPER=tree-child")
			_ = child.Run()
			os.Exit(0)
		}
		if mode == "timeout" {
			time.Sleep(time.Minute)
			os.Exit(1)
		}
		if mode == "claude" {
			if !reflect.DeepEqual(os.Args[1:], claudeArgs) {
				os.Exit(2)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": "Current session: 13% used · resets Oct 28 at 3pm (Asia/Shanghai)\nCurrent week (all models): 42% used\nCurrent week (Sonnet): 78% used"})
			os.Exit(0)
		}
		if strings.HasPrefix(mode, "reset-") {
			runResetHelper(mode)
			os.Exit(0)
		}
		if !reflect.DeepEqual(os.Args[1:], []string{"app-server", "--listen", "stdio://"}) {
			os.Exit(3)
		}
		dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
		for _, method := range []string{"initialize", "initialized", "account/read", "account/rateLimits/read"} {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if dec.Decode(&req) != nil || req.Method != method {
				os.Exit(4)
			}
			if method == "initialized" {
				continue
			}
			var result any = map[string]any{}
			switch method {
			case "account/read":
				typ := "chatgpt"
				if mode == "api" {
					typ = "apiKey"
				}
				result = map[string]any{"account": map[string]string{"type": typ}}
			case "account/rateLimits/read":
				// An unrelated notification must never be mistaken for the reply.
				_ = enc.Encode(map[string]string{"method": "account/updated"})
				_ = json.Unmarshal([]byte(`{"rateLimits":{"planType":"wrong","primary":{"usedPercent":99}},"rateLimitsByLimitId":{"codex":{"planType":"pro","primary":{"usedPercent":17,"windowDurationMins":300,"resetsAt":1900000000},"secondary":{"usedPercent":42,"windowDurationMins":10080}}}}`), &result)
				if mode == "codex-resets" {
					result.(map[string]any)["rateLimitResetCredits"] = map[string]any{"availableCount": 3, "credits": []any{map[string]any{"id": "later", "resetType": "codexRateLimits", "status": "available", "expiresAt": 2000000000}, map[string]any{"id": "first", "resetType": "codexRateLimits", "status": "available", "expiresAt": 1900000000}}}
				}
			}
			_ = enc.Encode(map[string]any{"id": req.ID, "result": result})
		}
		// Exit only when the reader closes its owned server's input.
		var ignored any
		_ = dec.Decode(&ignored)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helper(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("USAGE_TEST_HELPER", mode)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestCodexReadsOnlyAccountAndGeneralLimits(t *testing.T) {
	r, err := readCodex(context.Background(), helper(t, "codex"), t.TempDir())
	plan, windows := r.Plan, r.Windows
	if err != nil || plan != "pro" || len(windows) != 2 {
		t.Fatalf("%s %+v %v", plan, windows, err)
	}
	if windows[0].Name != "5 小时" || windows[0].Used != 17 || windows[0].Reset == "" || windows[1].Name != "7 天" {
		t.Fatalf("wrong windows: %+v", windows)
	}
}

func TestCodexAPIKeyIsNotZeroQuota(t *testing.T) {
	r, err := readCodex(context.Background(), helper(t, "api"), t.TempDir())
	windows := r.Windows
	if err == nil || len(windows) > 0 {
		t.Fatalf("API account: %+v %v", windows, err)
	}
}

func TestOwnedReaderTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := readCodex(ctx, helper(t, "timeout"), t.TempDir())
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("unbounded reader: %v", err)
	}
}

func TestClaudeUsageIsolationAndWindows(t *testing.T) {
	windows, err := readClaude(context.Background(), helper(t, "claude"), t.TempDir())
	if err != nil || len(windows) != 2 || windows[0].Used != 13 || windows[1].Used != 42 || !strings.Contains(windows[0].Reset, "Asia/Shanghai") {
		t.Fatalf("%+v %v", windows, err)
	}
}

func TestClaudeUnavailableNeverBecomesZero(t *testing.T) {
	for _, text := range []string{"", "You are currently using your subscription to power your Claude Code usage", "Current session: 1% used\nError 401 unauthorized", "Current week (Sonnet): 5% used"} {
		data, _ := json.Marshal(map[string]string{"type": "result", "result": text})
		if windows, err := parseClaude(data); err == nil || len(windows) != 0 {
			t.Fatalf("accepted unavailable %q: %+v %v", text, windows, err)
		}
	}
	data := []byte(`[{"type":"system"},{"type":"result","result":"\u001b[32mCurrent session: 0% used\u001b[0m"}]`)
	if windows, err := parseClaude(data); err != nil || len(windows) != 1 || windows[0].Used != 0 {
		t.Fatalf("real zero: %+v %v", windows, err)
	}
}

func TestBoundsAndAbsentWindow(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{-3, 0}, {0, 0}, {120, 100}, {math.NaN(), 0}, {math.Inf(1), 0}} {
		if got := Percent(tc.in); got != tc.want {
			t.Fatalf("%v: %v", tc.in, got)
		}
	}
	if windows := codexWindows(codexLimits{Primary: &codexWindow{Mins: 300}}); len(windows) != 0 {
		t.Fatal("missing percent became zero")
	}
	var b limitedBuffer
	if _, err := fmt.Fprint(&b, strings.Repeat("a", 1<<20+1)); err == nil || b.Len() > 1<<20 {
		t.Fatal("output not bounded")
	}
}

func TestDesktopPathSupportsEnvShebangLaunchers(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX env shebang")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PATH", "/usr/bin:/bin")
	dir := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"usage-node": "#!/bin/sh\nprintf 'runtime found'\n", "codex": "#!/usr/bin/env usage-node\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := findBinary("codex")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env = commandEnv("codex", root)
	out, err := cmd.Output()
	if err != nil || string(out) != "runtime found" {
		t.Fatalf("GUI launcher lost runtime: %q %v", out, err)
	}
}

func TestReaderCancellationIncludesOwnedLauncherChild(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unix process groups")
	}
	root := t.TempDir()
	ready, survived := filepath.Join(root, "ready"), filepath.Join(root, "survived")
	t.Setenv("USAGE_TREE_READY", ready)
	t.Setenv("USAGE_TREE_SURVIVED", survived)
	binary := helper(t, "tree")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _, _ = readCodex(ctx, binary, root); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned launcher child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader cancellation hung")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(survived); err == nil {
		t.Fatal("npm-style launcher child outlived its canceled reader")
	}
}
