//go:build retty_cli && (darwin || linux)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/rex"
)

// The real executable must propagate process codes through main, not just
// return an ExitError from a library. Only an owned foreground server is used.
func TestCLIControlProcessExitCodes(t *testing.T) {
	if os.Getenv("RETTY_CLI_E2E") != "1" {
		t.Skip("set RETTY_CLI_E2E=1 for owned process CLI tests")
	}
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	t.Setenv("RETTY_AGENT_SOCKET", "")
	t.Setenv("RETTY_SESSION", "")
	binary := os.Getenv("RETTY_CLI_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "retty")
		build := exec.Command("go", "build", "-tags", "retty_cli", "-o", binary, ".")
		build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	server := exec.Command(binary, "-server")
	server.Stdout, server.Stderr = io.Discard, io.Discard
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	t.Cleanup(func() {
		if c, err := localClient(context.Background()); err == nil {
			all, _ := c.List()
			for _, s := range all {
				c.Kill(s.ID)
			}
			c.Shutdown()
			c.Close()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			server.Process.Kill()
			<-done
		}
	})
	c := waitLocal(t, ctx)
	defer c.Close()
	command := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		var out, diag bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diag
		err := cmd.Run()
		if err == nil {
			return out.String(), 0
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		return out.String(), exit.ExitCode()
	}
	data, code := command("new", "--json", "--dir", dir, "--", "/bin/sh", "-c", "printf final-from-cli; exit 23")
	var job rex.SessionInfo
	if code != 0 || json.Unmarshal([]byte(data), &job) != nil || job.ID == "" {
		t.Fatal(data, code)
	}
	data, code = command("wait", job.ID, "--exit", "--json", "--timeout", "3s")
	if code != 23 || !strings.Contains(data, "\"exitCode\":23") {
		t.Fatal("main lost child exit code", data, code)
	}
	if data, code = command("read", job.ID); code != 0 || !strings.Contains(data, "final-from-cli") {
		t.Fatal(data, code)
	}
	data, code = command("new", "--json", "--dir", dir, "--", "/bin/cat")
	var cat rex.SessionInfo
	if code != 0 || json.Unmarshal([]byte(data), &cat) != nil || cat.ID == "" {
		t.Fatal(data, code)
	}
	if _, code = command("wait", cat.ID, "--exit", "--timeout", "30ms"); code != 124 {
		t.Fatal("timeout did not exit 124", code)
	}
	if data, code = command("skill"); code != 0 || !strings.Contains(data, "name: retty-control") {
		t.Fatal("bundled skill missing", code)
	}
	if data, code = command("status", "--data-dir", dir); code != 0 || !strings.Contains(data, "\"server\"") {
		t.Fatal("gateway status compatibility lost", data, code)
	}
}
