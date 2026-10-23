package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/diagnostics"
	"retty/internal/rex"
	"retty/internal/runtimeinfo"
)

func TestDiagnosticsShowsMissingAgentCWDCommandAndVersions(t *testing.T) {
	a, tt := newStaticTestApp(t)
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/missing/hook-runner"}]}]}}`), 0600)
	p := a.tab().Focus
	p.info.Dir = dir
	p.info.Agent = rex.AgentState{ID: "codex", CWD: filepath.Join(dir, "renamed-project"), ConfigDir: dir, HookEvent: "PreToolUse", Updated: time.Now()}
	a.hello = rex.Hello{PID: 321, Version: rex.ProtocolVersion, Build: runtimeinfo.Info{Version: "old-service"}, Exe: "/old/Retty"}
	a.openDiagnostics()
	waitFor(t, tt, "diagnostic collection", func() bool { return a.diagnostics != nil && !a.diagnostics.busy })
	if r := a.diagnostics.report; r == nil || r.CWD.Code != "missing" || len(r.Hooks) != 1 || r.Hooks[0].Executable.Code != "missing" {
		t.Fatal("failed paths missing", a.diagnostics)
	}
	texts := strings.Join(tt.Texts(), " ")
	if !strings.Contains(texts, "ENOENT") || !strings.Contains(texts, "old-service") || !strings.Contains(texts, "PreToolUse") || !strings.Contains(texts, "未执行任何 Hook") {
		t.Fatal(texts)
	}
	saveSettingsImage(t, tt, "hook-version-diagnostics")
	if err := tt.Click("Copy hook diagnostics"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tt.Clipboard(), "/missing/hook-runner") {
		t.Fatal("diagnostic copy lost failing command")
	}
}

func TestDiagnosticsDropsLateReplyAndDoesNotFallBackFromOfflineHost(t *testing.T) {
	a, tt := newStaticTestApp(t)
	t.Setenv("RETTY_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	a.diagnosticRead = func(context.Context, *rex.Client, diagnostics.Request) (diagnostics.Report, error) {
		close(entered)
		<-release
		return diagnostics.Report{Note: "late-result"}, nil
	}
	a.openDiagnostics()
	<-entered
	a.settingsOpen = false
	tt.Frame()
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if a.diagnostics.report != nil {
			t.Fatal("closed diagnostic accepted a late result")
		}
		time.Sleep(5 * time.Millisecond)
	}
	called := false
	a.diagnosticRead = func(context.Context, *rex.Client, diagnostics.Request) (diagnostics.Report, error) {
		called = true
		return diagnostics.Report{}, nil
	}
	p := a.tab().Focus
	p.host = &desktopHost{key: "offline-remote"}
	p.Tab.Host = p.host
	a.openDiagnostics()
	waitFor(t, tt, "offline diagnostic", func() bool { return !a.diagnostics.busy })
	if called || a.diagnostics.report != nil || !strings.Contains(a.diagnostics.err, "不会回退") {
		t.Fatal("remote diagnostic used local config", called, a.diagnostics)
	}
}
