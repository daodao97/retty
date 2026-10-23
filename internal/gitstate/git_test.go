package gitstate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSuffix(string(out), "\n")
	}
	git("init", "-qb", "main")
	return dir, git
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusPreservesUserIndexAndIgnoresGitOverrides(t *testing.T) {
	dir, git := gitFixture(t)
	name := "[literal] 中文\nname.txt"
	write(t, filepath.Join(dir, name), "old\nshared\n")
	git("add", ".")
	git("commit", "-qm", "fixture")
	write(t, filepath.Join(dir, name), "staged\nshared\n")
	git("add", ".")
	write(t, filepath.Join(dir, name), "current\nshared\nextra\n")
	write(t, filepath.Join(dir, "untracked.txt"), "new\nwithout trailing newline")
	write(t, filepath.Join(dir, "binary"), "binary\x00data\n")
	before, err := os.ReadFile(filepath.Join(dir, ".git/index"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "must-not-run")
	git("config", "core.fsmonitor", "touch "+marker)
	git("config", "diff.external", "touch "+marker)
	git("config", "filter.danger.clean", "touch "+marker)
	git("config", "filter.danger.process", "touch "+marker)
	git("config", "filter.danger.required", "true")
	write(t, filepath.Join(dir, ".git/info/attributes"), "*.txt filter=danger\n")
	t.Setenv("GIT_DIR", "/missing/git-dir")
	t.Setenv("GIT_WORK_TREE", "/missing/worktree")
	s, err := Read(context.Background(), dir)
	if err != nil || s.Branch != "main" || s.Files != 3 || s.Added != 4 || s.Removed != 1 || s.Default != "main" {
		t.Fatalf("status %+v: %v", s, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git/index"))
	if err != nil || string(after) != string(before) {
		t.Fatal("summary changed the real index", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("summary executed repository command")
	}
}

func TestWorktreesShareProjectButKeepTheirBranchAndDirectory(t *testing.T) {
	dir, git := gitFixture(t)
	write(t, filepath.Join(dir, "file"), "base\n")
	git("add", ".")
	git("commit", "-qm", "fixture")
	linked := filepath.Join(t.TempDir(), "outside worktree")
	git("worktree", "add", "-qb", "task/fix", linked)
	linked, _ = filepath.EvalSymlinks(linked)
	a, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Read(context.Background(), linked)
	if err != nil || a.Key() != b.Key() || a.Root == b.Root || b.Root != linked || b.ProjectDir != dir || b.Branch != "task/fix" {
		t.Fatalf("main %+v, linked %+v: %v", a, b, err)
	}
	write(t, filepath.Join(b.GitDir, "MERGE_HEAD"), a.Head+"\n")
	b, err = Read(context.Background(), linked)
	if err != nil || b.Operation != "merge" || a.Operation != "" {
		t.Fatalf("worktree operation leaked: %+v: %v", b, err)
	}
}

func TestUnbornDetachedAndSeparateGitDirectory(t *testing.T) {
	dir, git := gitFixture(t)
	write(t, filepath.Join(dir, "staged"), "one\n")
	git("add", ".")
	write(t, filepath.Join(dir, "untracked"), "two\nthree\n")
	s, err := Read(context.Background(), dir)
	if err != nil || s.Head != "" || s.Branch != "main" || s.Files != 2 || s.Added != 3 {
		t.Fatalf("unborn %+v: %v", s, err)
	}
	git("commit", "-qm", "fixture")
	git("checkout", "--detach", "-q")
	s, err = Read(context.Background(), dir)
	if err != nil || s.Branch != s.Head[:7] {
		t.Fatalf("detached %+v: %v", s, err)
	}
	meta := filepath.Join(t.TempDir(), "metadata")
	git("init", "--separate-git-dir", meta)
	r, err := Identify(context.Background(), dir)
	if err != nil || r.ProjectDir != dir || filepath.Base(r.CommonDir) != "metadata" {
		t.Fatalf("separate metadata %+v: %v", r, err)
	}
}

func TestStatusBoundsNewFileReadsAndCancellation(t *testing.T) {
	dir, _ := gitFixture(t)
	write(t, filepath.Join(dir, "huge"), strings.Repeat("x", (1<<20)+1))
	s, err := Read(context.Background(), dir)
	if err != nil || !s.Truncated || s.Files != 1 || s.Added != 0 {
		t.Fatalf("large file %+v: %v", s, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, dir); err != context.Canceled {
		t.Fatal("cancellation was ignored", err)
	}
	if _, err := Identify(context.Background(), t.TempDir()); err != ErrNotRepository {
		t.Fatal("non-repository was accepted", err)
	}
}
