package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func requestFixture() Request {
	return Request{SID: "pane", ThreadID: "thread", Agent: "codex", Title: "Structured command", Detail: "No terminal menu exists", Options: []Option{{"once", "Allow once"}, {"deny", "Deny"}}}
}
func TestApprovalSingleWinnerAndIsolation(t *testing.T) {
	s := NewStore()
	var n atomic.Int32
	r := requestFixture()
	id, err := s.Add(r, func(choice string) error { n.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Answer("other", "thread", id, "once"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err = s.Answer("pane", "other", id, "once"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err = s.Answer("pane", "thread", id, "arbitrary RPC payload"); err == nil {
		t.Fatal("unknown choice accepted")
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() { _ = s.Answer("pane", "thread", id, "deny") })
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatal("duplicate decision", n.Load())
	}
	if len(s.List("pane", "thread")) != 0 {
		t.Fatal("resolved request retained")
	}
	r.Expires = time.Now().Add(-time.Second)
	id, _ = s.Add(r, func(string) error { t.Fatal("expired decision"); return nil })
	if !errors.Is(s.Answer("pane", "thread", id, "once"), ErrStale) {
		t.Fatal("expiry ignored")
	}
}
func TestApprovalPrivateSocketNoRequestFiles(t *testing.T) {
	dir := t.TempDir()
	root := directory(dir)
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewStore()
	closeEndpoint, err := Listen(ctx, dir, "pane", s)
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	var n int
	id, _ := s.Add(requestFixture(), func(string) error { n++; return nil })
	got, err := List(ctx, dir, "pane", "thread")
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatal(got, err)
	}
	if !ViewerActive(dir, "pane") || ViewerActive(dir, "other") {
		t.Fatal("viewer lease crossed pane")
	}
	if err = Answer(ctx, dir, "pane", "thread", id, "deny"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if Answer(ctx, dir, "pane", "thread", "../../escape", "once") == nil {
		t.Fatal("path accepted")
	}
	files, _ := os.ReadDir(root)
	for _, f := range files {
		st, _ := f.Info()
		if f.Name() == pane("pane")+".viewer" {
			if st.Size() != 0 {
				t.Fatal("viewer retained content")
			}
		} else if st.Mode()&os.ModeSocket == 0 {
			t.Fatal("request persisted", f.Name())
		}
	}
	os.Chtimes(filepath.Join(root, pane("pane")+".viewer"), time.Now().Add(-10*time.Second), time.Now().Add(-10*time.Second))
	if ViewerActive(dir, "pane") {
		t.Fatal("old viewer retained")
	}
	cancel()
	closeEndpoint()
	if Answer(context.Background(), dir, "pane", "thread", id, "once") == nil {
		t.Fatal("closed endpoint answered")
	}
}
