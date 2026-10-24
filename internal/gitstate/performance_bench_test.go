package gitstate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func BenchmarkRepositoryStatus(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("files=%d", count), func(b *testing.B) {
			dir, err := filepath.EvalSymlinks(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			git := func(args ...string) {
				b.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
					"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test",
					"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
				if out, err := cmd.CombinedOutput(); err != nil {
					b.Fatalf("git %v: %s: %v", args, out, err)
				}
			}
			git("init", "-qb", "main")
			for i := range count {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i)), []byte("base\nshared\n"), 0600); err != nil {
					b.Fatal(err)
				}
			}
			git("add", ".")
			git("-c", "commit.gpgsign=false", "-c", "core.hooksPath="+os.DevNull, "commit", "-qm", "fixture")
			for i := range count / 10 {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i)), []byte("changed\nshared\nadded\n"), 0600); err != nil {
					b.Fatal(err)
				}
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s, err := Read(ctx, dir)
				if err != nil || s.Files != count/10 || s.Added != count/5 || s.Removed != count/10 {
					b.Fatalf("incorrect Git summary: %+v: %v", s, err)
				}
			}
		})
	}
}
