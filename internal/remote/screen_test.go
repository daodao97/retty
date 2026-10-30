package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeScreenHelper writes a retty-screen stand-in: like the real one,
// capture quits at once without a stdin to hold it open; input appends its
// events to log.
func fakeScreenHelper(t *testing.T, capture string) (helper, log string) {
	t.Helper()
	dir := t.TempDir()
	helper, log = filepath.Join(dir, "retty-screen"), filepath.Join(dir, "events")
	script := "#!/bin/sh\ncase \"$1\" in\ncapture) [ -p /dev/stdin ] || exit 0; " + capture + " ;;\ninput) cat >> '" + log + "' ;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return helper, log
}

func TestScreenViewRefusesUntilAllowed(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	helper, _ := fakeScreenHelper(t, "printf '\\000\\000\\000\\003abc'; cat >/dev/null")
	b.screen = &ScreenHost{Helper: helper, Allowed: func() bool { return false }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := DialScreen(ctx, c, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "未开启远程桌面") {
		t.Fatal("a screen not shared was shown:", err)
	}
}

func TestScreenViewReportsMissingPermission(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	helper, _ := fakeScreenHelper(t, "echo 'screen recording permission denied' >&2; exit 1")
	b.screen = &ScreenHost{Helper: helper, Allowed: func() bool { return true }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := DialScreen(ctx, c, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "屏幕录制权限") {
		t.Fatal("a missing permission was not explained:", err)
	}
}

func TestScreenViewStreamsFramesAndChecksInput(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	helper, log := fakeScreenHelper(t, "printf '\\000\\000\\000\\003abc'; cat >/dev/null")
	viewers := make(chan int, 4)
	b.screen = &ScreenHost{Helper: helper, Allowed: func() bool { return true }, OnViewers: func(n int) { viewers <- n }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := DialScreen(ctx, c, 640, 50, 10)
	if err != nil {
		t.Fatal(err)
	}
	kind, frame, err := s.Next()
	if err != nil || kind != ScreenFrameJPEG || string(frame) != "abc" {
		t.Fatal("first frame", kind, string(frame), err)
	}
	if n := <-viewers; n != 1 {
		t.Fatal("viewers", n)
	}
	if s.Send("rm -rf /") == nil {
		t.Fatal("an invalid event was sent")
	}
	for _, event := range []string{"move 0.5 0.25", "down 0.5 0.25 0", "up 0.5 0.25 0", "text aGk="} {
		if err := s.Send(event); err != nil {
			t.Fatal(err)
		}
	}
	// A raw line the client would refuse must still be dropped by the bridge.
	if _, err := s.conn.Write([]byte("key 999 0\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Send("scroll 0 -40"); err != nil {
		t.Fatal(err)
	}
	want := "move 0.5 0.25\ndown 0.5 0.25 0\nup 0.5 0.25 0\ntext aGk=\nscroll 0 -40\n"
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _ := os.ReadFile(log)
		if string(got) == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("events %q, want %q", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Close()
	select {
	case n := <-viewers:
		if n != 0 {
			t.Fatal("viewers after close", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a closed view still counted")
	}
}

func TestValidScreenEvent(t *testing.T) {
	for _, ok := range []string{"move 0 1", "drag 0.3 0.7 1", "scroll -120 40", "text 5L2g5aW9", "key 36 1048576", "click 0.5 0.5 0 2"} {
		if !ValidScreenEvent(ok) {
			t.Error("refused", ok)
		}
	}
	for _, bad := range []string{"", "move 1.5 0", "move 0.5", "down 0 0 2", "scroll 1 99999", "text a b", "text $(x)", "key 200 0", "exec ls", "click 0.5 0.5 0 4", "click 0.5 0.5 0"} {
		if ValidScreenEvent(bad) {
			t.Error("accepted", bad)
		}
	}
}

func TestScreenViewSendsLockState(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	helper, _ := fakeScreenHelper(t, "printf '\\000\\000\\000\\003abc'; cat >/dev/null")
	b.screen = &ScreenHost{Helper: helper, Allowed: func() bool { return true }, Locked: func() bool { return true }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := DialScreen(ctx, c, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 3 {
		kind, payload, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if kind == ScreenFrameLock {
			if string(payload) != "1" {
				t.Fatal("lock state", string(payload))
			}
			return
		}
	}
	t.Fatal("no lock state")
}

func TestScreenViewWaitsOutTheLockScreen(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	helper, _ := fakeScreenHelper(t, "printf '\\000\\000\\000\\003abc'; cat >/dev/null")
	var locked atomic.Bool
	locked.Store(true)
	b.screen = &ScreenHost{Helper: helper, Allowed: func() bool { return true }, Locked: locked.Load}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := DialScreen(ctx, c, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	kind, payload, err := s.Next()
	if err != nil || kind != ScreenFrameLock || string(payload) != "1" {
		t.Fatal("first frame", kind, string(payload), err)
	}
	locked.Store(false)
	for range 4 {
		kind, payload, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if kind == ScreenFrameJPEG {
			if string(payload) != "abc" {
				t.Fatal("frame", string(payload))
			}
			return
		}
	}
	t.Fatal("no frame after unlock")
}

func TestScreenUnlockPassesThePassword(t *testing.T) {
	c, b, _, _ := fileUploadFixture(t)
	got := make(chan string, 1)
	b.screen = &ScreenHost{Helper: "retty-screen", Allowed: func() bool { return true }, Unlock: func(ctx context.Context, password string) error {
		got <- password
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := UnlockScreen(ctx, c, "pässw0rd"); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p != "pässw0rd" {
		t.Fatal("password", p)
	}
	b.screen.Unlock = func(context.Context, string) error { return errors.New("密码不正确") }
	if err := UnlockScreen(ctx, c, "wrong"); err == nil || err.Error() != "密码不正确" {
		t.Fatal(err)
	}
	b.screen.Allowed = func() bool { return false }
	if err := UnlockScreen(ctx, c, "x"); err == nil {
		t.Fatal("unlocked while sharing is off")
	}
}
