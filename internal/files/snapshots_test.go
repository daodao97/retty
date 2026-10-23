package files

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"retty/internal/gitstate"
)

func TestSnapshotFileRequestsUseImmutableHostStoreAndVerifyTurnIdentity(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-qb", "main", dir).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	store := gitstate.SnapshotStore{Dir: t.TempDir()}
	ctx := WithSnapshotStore(context.Background(), store.Dir)
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("before\n"), 0600)
	if _, err := store.CaptureTurn(ctx, dir, "pane", "turn-1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("after\n"), 0600)
	request := Request{Kind: "git-changes", Path: dir, Base: "turn", Session: "pane", Turn: "turn-1"}
	result, err := Do(ctx, request)
	if err != nil || result.Comparison == nil || len(result.Entries) != 1 || result.Entries[0].Name != "file.txt" {
		t.Fatal(result, err)
	}
	diff := Request{Kind: "git-diff", Path: path, Root: result.Path, OldTree: result.Comparison.Base, NewTree: result.Comparison.Current.Tree}
	r, err := Do(ctx, diff)
	if err != nil || !strings.Contains(string(r.Data), "-before") || !strings.Contains(string(r.Data), "+after") {
		t.Fatal(string(r.Data), err)
	}
	request.Turn = "turn-2"
	if _, err := Do(ctx, request); err == nil {
		t.Fatal("previous turn was accepted as current")
	}
	request.Turn = ""
	if _, err := Do(ctx, request); err == nil {
		t.Fatal("legacy session without a canonical turn identity guessed a baseline")
	}
	if _, err := Do(context.Background(), request); err == nil {
		t.Fatal("a caller supplied its own snapshot store over the request")
	}
	diff.Path = filepath.Join(filepath.Dir(dir), "outside")
	if _, err := Do(ctx, diff); err == nil {
		t.Fatal("diff escaped its repository")
	}
}
