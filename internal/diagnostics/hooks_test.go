package diagnostics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"retty/internal/agents"
)

func TestDiagnoseMissingCWDCommandAndScriptWithoutExecutingHooks(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "never-run")
	exe := filepath.Join(dir, "hook with spaces")
	os.WriteFile(exe, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700)
	missing := filepath.Join(dir, "deleted-project")
	commands := []string{"\"" + exe + "\"", "\"" + filepath.Join(dir, "missing-hook") + "\"", "/bin/sh ./missing-script.sh", agents.HookCommand("codex"), "if [ -n \"$CUSTOM_HOOK\" ]; then \"$CUSTOM_HOOK\"; fi"}
	var hooks []any
	for _, cmd := range commands {
		hooks = append(hooks, map[string]any{"type": "command", "command": cmd})
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": hooks}}}})
	config := filepath.Join(dir, "hooks.json")
	os.WriteFile(config, data, 0600)
	r, err := Collect(context.Background(), Request{Agent: "codex", ConfigDir: dir, CWD: missing, HookExecutable: exe})
	if err != nil || r.CWD.Code != "missing" || r.Config.Code != "ok" || len(r.Hooks) != 5 {
		t.Fatal(r, err)
	}
	if r.Hooks[0].Executable.Code != "ok" || r.Hooks[1].Executable.Code != "missing" || r.Hooks[2].Script == nil || r.Hooks[2].Script.Code != "missing" || !r.Hooks[3].Retty || r.Hooks[3].Executable.Path != exe || r.Hooks[4].Executable.Code != "dynamic" {
		t.Fatal(r.Hooks)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("diagnostic executed a hook")
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(data) {
		t.Fatal("diagnostic modified configuration")
	}
	// A compatibility symlink is usable while preserving the recorded cwd.
	os.Mkdir(filepath.Join(dir, "renamed-project"), 0700)
	os.Symlink(filepath.Join(dir, "renamed-project"), missing)
	r, err = Collect(context.Background(), Request{Agent: "codex", ConfigDir: dir, CWD: missing})
	if err != nil || r.CWD.Code != "ok" || r.CWD.Path != missing {
		t.Fatal("symlink cwd misdiagnosed", r.CWD, err)
	}
}

func TestLimitsInvalidConfigAndCustomProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte("{broken"), 0600)
	r, err := Collect(context.Background(), Request{Agent: "codex"})
	if err != nil || r.Config.Code != "invalid" || r.Config.Path != filepath.Join(dir, "hooks.json") {
		t.Fatal(r, err)
	}
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(strings.Repeat("x", (1<<20)+1)), 0600)
	r, err = Collect(context.Background(), Request{Agent: "codex"})
	if err != nil || r.Config.Code != "too_large" {
		t.Fatal(r.Config, err)
	}
	for _, req := range []Request{{Agent: "unknown"}, {Agent: "codex", CWD: "relative"}, {Agent: "codex", HookExecutable: "/tmp/x\nnext"}} {
		if _, err := Collect(context.Background(), req); err == nil {
			t.Fatal("accepted invalid request", req)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, Request{Agent: "codex"}); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
}
