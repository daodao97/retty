package remote

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"retty/internal/rex"
)

// The native runner types a command into this disposable shell. Reading its
// result verifies the actual PTY input order, rather than the keyboard proxy.
func TestTailcatIOSInputContinuation(t *testing.T) {
	fixture := os.Getenv("RETTY_IOS_INPUT_FIXTURE")
	if fixture == "" {
		t.Skip("set RETTY_IOS_INPUT_FIXTURE for the native iPhone runner")
	}
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	go rex.Serve()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", rex.SocketPath())
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	desktop, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer desktop.Close()
	defer desktop.Shutdown()
	session, err := desktop.Create(rex.CreateOptions{Dir: dir, Command: []string{"/bin/sh"}, Cols: 48, Rows: 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bridge, err := Start(ctx, rex.SocketPath(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	data, _ := json.Marshal(struct{ Link, Existing, Created, Directory string }{Link: bridge.Link(), Existing: session.ID, Directory: dir})
	if err := os.WriteFile(fixture, data, 0600); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture), "ios-finished")); err == nil {
			got, err := os.ReadFile(filepath.Join(dir, "input-result"))
			if err != nil || string(got) != "abc你好XYZ你好tail" {
				t.Fatalf("native English/Chinese continuation reached the shell as %q: %v", got, err)
			}
			list, err := desktop.List()
			// This fixture has no desktop viewer holding its size: the phone
			// may legitimately own the grid. Its shell identity must survive.
			if err != nil || len(list) != 1 || list[0].ID != session.ID || list[0].PID != session.PID {
				t.Fatalf("native input recreated the fixture: %+v, %v", list, err)
			}
			t.Log("PASS: English, repeated nine-key Chinese candidates and trailing text reached the same PTY in order")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native input fixture timed out")
}
