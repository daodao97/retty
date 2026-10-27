package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/async"
	"github.com/egoist/mygo/ui"
	"retty/internal/files"
	"retty/internal/rex"
)

func awaitFileWork[T any](t *testing.T, ready <-chan T) T {
	t.Helper()
	select {
	case value := <-ready:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("file worker did not reach its synchronization point")
		var zero T
		return zero
	}
}

// Wait without draining UI callbacks, so replacement/close can be tested
// after a result is queued but before the UI has received it.
func awaitFileQueue(t *testing.T, a *App, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		a.postMu.Lock()
		queued := len(a.posted)
		a.postMu.Unlock()
		if queued >= count {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("file notifications were not queued")
		}
	}
}

func blockedDesktopFilePreview(t *testing.T, a *App, b *desktopFiles) context.Context {
	t.Helper()
	started := make(chan context.Context, 1)
	b.read = func(ctx context.Context, _ *rex.Client, _ files.Request) (files.Result, error) {
		started <- ctx
		<-ctx.Done()
		return files.Result{}, ctx.Err()
	}
	a.previewFile(b, filepath.Join(b.cwd, "blocked.txt"), false, 256<<10)
	return awaitFileWork(t, started)
}

func desktopFileTaskFixture(t *testing.T) (*App, *ui.Tester, *desktopFiles) {
	t.Helper()
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.tab().Focus.terminal().info.Dir = t.TempDir()
	a.toggleFiles()
	tt.Frame()
	b := a.focusedFiles()
	t.Cleanup(b.stop)
	waitFor(t, tt, "initial directory", func() bool { return !b.loading })
	a.runPosted()
	return a, tt, b
}

func TestDesktopFileTaskReplacesQueuedPreview(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "error"}[fail], func(t *testing.T) {
			a, tt, b := desktopFileTaskFixture(t)
			gate := make(chan struct{})
			defer close(gate)
			b.read = func(ctx context.Context, _ *rex.Client, req files.Request) (files.Result, error) {
				if req.Path == "old.txt" {
					if fail {
						return files.Result{}, errors.New("old request failed")
					}
					return files.Result{Data: []byte("old reply")}, nil
				}
				select {
				case <-gate:
				case <-ctx.Done():
					return files.Result{}, ctx.Err()
				}
				return files.Result{Data: []byte("new reply")}, nil
			}
			a.previewFile(b, "old.txt", false, 256<<10)
			count := 3 // running, published value, success
			if fail {
				count = 2
			}
			awaitFileQueue(t, a, count)
			a.previewFile(b, "new.txt", false, 256<<10)
			a.runPosted()
			if b.err != "" || len(b.previewData) != 0 || !b.previewLoading {
				t.Fatal("queued old preview reached the new selection", b.err, string(b.previewData))
			}
			gate <- struct{}{}
			waitFor(t, tt, "replacement preview", func() bool { return !b.previewLoading })
			if string(b.previewData) != "new reply" || b.err != "" {
				t.Fatal("replacement preview was lost", string(b.previewData), b.err)
			}
		})
	}
}

func TestDesktopFileTaskCancelsRunningPreview(t *testing.T) {
	a, tt, b := desktopFileTaskFixture(t)
	ctx := blockedDesktopFilePreview(t, a, b)
	b.read = func(context.Context, *rex.Client, files.Request) (files.Result, error) {
		return files.Result{Data: []byte("replacement")}, nil
	}
	a.previewFile(b, "replacement.txt", false, 256<<10)
	if ctx.Err() == nil {
		t.Fatal("previous preview request was not canceled")
	}
	waitFor(t, tt, "new preview", func() bool { return !b.previewLoading })
	if b.previewTask.Snapshot().Phase != async.Succeeded || string(b.previewData) != "replacement" || b.err != "" {
		t.Fatal("cancellation overwrote replacement result", b.err, string(b.previewData))
	}
}

func TestDesktopFileTaskRootChangeCancelsChildren(t *testing.T) {
	a, tt, b := desktopFileTaskFixture(t)
	started := make(chan context.Context, 1)
	b.read = func(ctx context.Context, _ *rex.Client, req files.Request) (files.Result, error) {
		if req.Path == "child" {
			started <- ctx
			<-ctx.Done()
			return files.Result{Path: "child", Entries: []files.Entry{{Name: "stale"}}}, nil
		}
		return files.Result{Path: req.Path, Entries: []files.Entry{{Name: "current"}}}, nil
	}
	a.toggleFileDirectory(b, "child")
	ctx := awaitFileWork(t, started)
	child := b.dirs["child"]
	a.loadFileDirectory(b, b.cwd)
	if ctx.Err() == nil {
		t.Fatal("root change left a child request running")
	}
	waitFor(t, tt, "new root", func() bool { return !b.loading })
	if b.dirs["child"] != nil || len(child.entries) != 0 || len(b.entries) != 1 || b.entries[0].Name != "current" {
		t.Fatal("canceled child filled the replacement tree", b.entries, child.entries)
	}
}

func TestDesktopFileTaskRootChangeDropsQueuedDirectory(t *testing.T) {
	a, tt, b := desktopFileTaskFixture(t)
	gate := make(chan struct{})
	defer close(gate)
	next := filepath.Join(b.cwd, "next")
	b.read = func(ctx context.Context, _ *rex.Client, req files.Request) (files.Result, error) {
		if req.Path == next {
			select {
			case <-gate:
			case <-ctx.Done():
				return files.Result{}, ctx.Err()
			}
		}
		return files.Result{Path: req.Path, Entries: []files.Entry{{Name: req.Path}}}, nil
	}
	a.loadFileDirectory(b, b.cwd)
	awaitFileQueue(t, a, 3)
	a.loadFileDirectory(b, next)
	a.runPosted()
	if !b.loading || len(b.entries) != 0 || b.dir != next {
		t.Fatal("queued old directory displaced the new root", b.dir, b.entries)
	}
	gate <- struct{}{}
	waitFor(t, tt, "replacement directory", func() bool { return !b.loading })
	if b.dir != next || len(b.entries) != 1 || b.entries[0].Name != next {
		t.Fatal("new directory result lost", b.dir, b.entries)
	}
}

func TestDesktopFileTaskCloseDropsQueuedPreview(t *testing.T) {
	a, _, b := desktopFileTaskFixture(t)
	b.read = func(context.Context, *rex.Client, files.Request) (files.Result, error) {
		return files.Result{Data: []byte("closed preview")}, nil
	}
	a.previewFile(b, "closed.txt", false, 256<<10)
	awaitFileQueue(t, a, 3)
	a.toggleFiles()
	a.runPosted()
	if a.focusedFiles() != nil || len(b.previewData) != 0 || b.previewTask != nil {
		t.Fatal("queued preview survived closing its owner")
	}
}

func TestDesktopFileTaskDisconnectedHostDropsQueuedPreview(t *testing.T) {
	a, _, b := desktopFileTaskFixture(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	b.pane.host, b.host, b.client = h, h, client
	b.read = func(context.Context, *rex.Client, files.Request) (files.Result, error) {
		return files.Result{Data: []byte("remote preview")}, nil
	}
	a.previewFile(b, "remote.txt", false, 256<<10)
	awaitFileQueue(t, a, 3)
	h.err = "disconnected"
	a.runPosted()
	if len(b.previewData) != 0 {
		t.Fatal("disconnected host published its queued preview")
	}
}

func TestFileComponentReconnectsSameTransportAndKeepsSelection(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	client, _ := desktopFixtureClient(t)
	h := fixtureDesktopHost(t, client)
	dir := t.TempDir()
	b := &desktopFiles{cwd: dir, dir: dir, base: "head", host: h}
	p := &Pane{ID: a.id(), host: h, dir: dir, content: b}
	b.pane = p
	b.read = func(_ context.Context, captured *rex.Client, req files.Request) (files.Result, error) {
		if captured != client {
			return files.Result{}, errors.New("wrong transport")
		}
		if req.Kind == "read" {
			return files.Result{Data: []byte("remote preview")}, nil
		}
		return files.Result{Path: dir, Entries: []files.Entry{{Name: "remote.txt", Regular: true}}}, nil
	}
	a.placePane(nil, p, false)
	t.Cleanup(b.stop)
	a.syncFiles()
	waitFor(t, tt, "remote directory", func() bool { return !b.loading })
	path := filepath.Join(dir, "remote.txt")
	a.previewFile(b, path, false, 256<<10)
	waitFor(t, tt, "remote preview", func() bool { return b.previewCode != nil })
	b.filter = "remote"
	h.err = "disconnected"
	a.syncFiles()
	if !b.offline || b.loading || b.previewLoading || b.previewTask != nil {
		t.Fatal("offline component retained requests")
	}
	h.err = ""
	a.syncFiles()
	waitFor(t, tt, "reconnected preview", func() bool { return !b.loading && b.previewCode != nil })
	if b.offline || b.preview != path || b.filter != "remote" || string(b.previewData) != "remote preview" {
		t.Fatal("same-client recovery lost the selection or failed to restart requests")
	}
}
