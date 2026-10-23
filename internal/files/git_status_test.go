package files

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitStatusIsAvailableWithoutARepositoryAndPreservesIndex(t *testing.T) {
	dir := t.TempDir()
	r, err := Do(context.Background(), Request{Kind: "git-status", Path: dir})
	if err != nil || r.Git != nil {
		t.Fatal("non-Git directories should simply have no Git summary", r, err)
	}
	cmd := exec.Command("git", "init", "-qb", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = Do(context.Background(), Request{Kind: "git-status", Path: dir})
	if err != nil || r.Git == nil || r.Git.Branch != "main" || r.Git.Added != 2 {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git/index")); !os.IsNotExist(err) {
		t.Fatal("read-only summary created an index", err)
	}
}
