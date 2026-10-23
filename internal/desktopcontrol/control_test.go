package desktopcontrol

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEndpointIsPrivateSingleOwnerAndReopenable(t *testing.T) {
	dir := t.TempDir()
	s, err := Listen(dir, func(ctx context.Context, r Request) (Result, error) { return Result{SID: r.SID}, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	info, err := os.Stat(Path(dir))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if other, err := Listen(dir, func(context.Context, Request) (Result, error) { return Result{}, nil }); err == nil {
		other.Close()
		t.Fatal("second GUI stole endpoint")
	}
	r, err := Call(context.Background(), dir, Request{Op: "test", SID: "one"})
	if err != nil || r.SID != "one" {
		t.Fatal(r, err)
	}
	s.Close()
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatal("endpoint leaked", err)
	}
	next, err := Listen(dir, func(context.Context, Request) (Result, error) { return Result{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestEndpointRejectsOversizeAndDoesNotDeleteRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Listen(dir, nil); err == nil {
		s.Close()
		t.Fatal("deleted regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatal(string(data), err)
	}
	if err := decode(strings.NewReader(strings.Repeat("x", maxMessage+1)+"\n"), &Request{}); err == nil {
		t.Fatal("accepted oversized message")
	}
	if Path(filepath.Join(dir, strings.Repeat("long", 60))) == "" {
		t.Fatal("missing hashed endpoint")
	}
}

func TestEndpointCloseCancelsOwnedConnections(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{})
	exited := make(chan struct{})
	s, err := Listen(dir, func(ctx context.Context, r Request) (Result, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return Result{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := Call(context.Background(), dir, Request{Op: "wait"}); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter")
	}
	s.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("handler leaked")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("close succeeded unexpectedly")
		}
	case <-time.After(time.Second):
		t.Fatal("caller leaked")
	}
	if conn, err := net.Dial("unix", Path(dir)); err == nil {
		conn.Close()
		t.Fatal("closed endpoint still accepted")
	}
}

func TestEndpointCallerCancellationCancelsQueuedMutation(t *testing.T) {
	dir := t.TempDir()
	canceled := make(chan struct{})
	s, err := Listen(dir, func(ctx context.Context, r Request) (Result, error) {
		<-ctx.Done()
		close(canceled)
		return Result{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Call(ctx, dir, Request{Op: "queued"}); err == nil {
		t.Fatal("canceled call succeeded")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("queued mutation retained live context after caller cancellation")
	}
}
