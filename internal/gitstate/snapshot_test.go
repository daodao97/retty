package gitstate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTurnSnapshotKeepsExistingChangesOutAndNeverTouchesSourceIndexOrFilters(t *testing.T) {
	dir, git := gitFixture(t)
	write(t, filepath.Join(dir, "tracked.txt"), "committed\n")
	write(t, filepath.Join(dir, "deleted.txt"), "delete me\n")
	git("add", ".")
	git("commit", "-qm", "fixture")
	write(t, filepath.Join(dir, "tracked.txt"), "staged\n")
	git("add", ".")
	write(t, filepath.Join(dir, "tracked.txt"), "already dirty\r\n")
	write(t, filepath.Join(dir, "unrelated.txt"), "already here\n")
	index, _ := os.ReadFile(filepath.Join(dir, ".git/index"))
	refs := git("for-each-ref", "--format=%(refname) %(objectname)")
	marker := filepath.Join(t.TempDir(), "filter-must-not-run")
	git("config", "filter.danger.clean", "touch "+marker)
	git("config", "filter.danger.process", "touch "+marker)
	git("config", "filter.danger.required", "true")
	write(t, filepath.Join(dir, ".git/info/attributes"), "*.txt filter=danger\n")
	store := SnapshotStore{Dir: filepath.Join(t.TempDir(), "private snapshots")}
	ctx := context.Background()
	baseline, err := store.Capture(ctx, dir, "pane-1")
	if err != nil {
		t.Fatal("capture", err)
	}
	write(t, filepath.Join(dir, "tracked.txt"), "this turn\r\n")
	odd := "new 中文\n\"quoted\" \\.txt"
	write(t, filepath.Join(dir, odd), "new file\n")
	if err := os.Remove(filepath.Join(dir, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	comparison, err := store.Compare(ctx, dir, "pane-1", "turn")
	if err != nil || comparison.Base != baseline.Tree || len(comparison.Files) != 3 {
		t.Fatalf("turn changes %+v: %v", comparison, err)
	}
	for _, f := range comparison.Files {
		if f.Path == "unrelated.txt" {
			t.Fatal("pre-existing untracked file attributed to this turn")
		}
	}
	patch, err := store.Diff(ctx, dir, comparison.Base, comparison.Current.Tree, "tracked.txt")
	if err != nil || !strings.Contains(string(patch), "-already dirty\r") || !strings.Contains(string(patch), "+this turn\r") || strings.Contains(string(patch), "-committed") {
		t.Fatalf("wrong turn baseline: %q: %v", patch, err)
	}
	patch, err = store.Diff(ctx, dir, comparison.Base, comparison.Current.Tree, odd)
	if err != nil || !strings.Contains(string(patch), "+new file") {
		t.Fatalf("literal path was not captured: %q: %v", patch, err)
	}
	// Diff requests refer to immutable captured objects, even if an agent has
	// edited again between listing files and opening the preview.
	write(t, filepath.Join(dir, "tracked.txt"), "later edit\n")
	patch, err = store.Diff(ctx, dir, comparison.Base, comparison.Current.Tree, "tracked.txt")
	if err != nil || strings.Contains(string(patch), "later edit") {
		t.Fatal("immutable comparison changed", string(patch), err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".git/index"))
	if string(after) != string(index) || refs != git("for-each-ref", "--format=%(refname) %(objectname)") {
		t.Fatal("snapshot changed the user's index or references")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("snapshot or diff executed the project's filter")
	}
}

func TestSnapshotHeadBranchUnbornAndFailedTurn(t *testing.T) {
	dir, git := gitFixture(t)
	store := SnapshotStore{Dir: t.TempDir()}
	ctx := context.Background()
	write(t, filepath.Join(dir, "initial"), "initial\n")
	comparison, err := store.Compare(ctx, dir, "pane", "head")
	if err != nil || !reflect.DeepEqual(comparison.Files, []Change{{Path: "initial", Status: "A"}}) {
		t.Fatal("unborn comparison", comparison, err)
	}
	git("add", ".")
	git("commit", "-qm", "main")
	git("checkout", "-qb", "task/fix")
	write(t, filepath.Join(dir, "committed on branch"), "branch change\n")
	git("add", ".")
	git("commit", "-qm", "branch")
	write(t, filepath.Join(dir, "uncommitted"), "current change\n")
	comparison, err = store.Compare(ctx, dir, "pane", "branch")
	if err != nil || len(comparison.Files) != 2 {
		t.Fatal("branch comparison", comparison, err)
	}
	comparison, err = store.Compare(ctx, dir, "pane", "head")
	if err != nil || !reflect.DeepEqual(comparison.Files, []Change{{Path: "uncommitted", Status: "A"}}) {
		t.Fatal("HEAD comparison", comparison, err)
	}
	if _, err := store.Capture(ctx, dir, "pane"); err != nil {
		t.Fatal(err)
	}
	if err := store.Invalidate(ctx, dir, "pane", "new turn capture failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compare(ctx, dir, "pane", "turn"); err == nil || !strings.Contains(err.Error(), "new turn capture failed") {
		t.Fatal("failed new turn silently reused the previous baseline", err)
	}
}

func TestSnapshotSkipsLargeFilesWithoutInventingDeletionsAndKeepsSymlinksLiteral(t *testing.T) {
	dir, git := gitFixture(t)
	write(t, filepath.Join(dir, "grows"), "small\n")
	git("add", ".")
	git("commit", "-qm", "fixture")
	store := SnapshotStore{Dir: t.TempDir()}
	ctx := context.Background()
	if _, err := store.Capture(ctx, dir, "pane"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "grows"), strings.Repeat("x", MaxSnapshotFile+1))
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, outside, "must not follow a symlink\n")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	comparison, err := store.Compare(ctx, dir, "pane", "turn")
	if err != nil || !comparison.Truncated || len(comparison.Files) != 1 || comparison.Files[0].Path != "link" {
		t.Fatalf("large file was shown as deleted: %+v: %v", comparison, err)
	}
	r, _ := Identify(ctx, dir)
	content, err := store.command(ctx, r, nil, "cat-file", "-p", comparison.Current.Tree+":link")
	if err != nil || string(content) != outside {
		t.Fatal("symlink content was followed", string(content), err)
	}
	for _, invalid := range []string{"../../escape", "/absolute"} {
		if _, err := store.Diff(ctx, dir, comparison.Base, comparison.Current.Tree, invalid); err == nil {
			t.Fatal("invalid path accepted", invalid)
		}
	}
	if _, err := store.Diff(ctx, dir, "HEAD;anything", comparison.Current.Tree, "link"); err == nil {
		t.Fatal("non-object revision accepted")
	}
}
