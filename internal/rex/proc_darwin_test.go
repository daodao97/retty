package rex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentProcessInspectionOwnsDirectoryStrings(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{filepath.Join(root, strings.Repeat("long-", 40)), filepath.Join(root, "short")}
	var pids []int
	for _, dir := range dirs {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sleep", "60")
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		pids = append(pids, cmd.Process.Pid)
	}
	first := inspect(pids[0])
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			for i := range 40 {
				j := (worker + i) % len(pids)
				if got := inspect(pids[j]); got.dir != dirs[j] {
					t.Errorf("process %d: got directory %q, want %q", j, got.dir, dirs[j])
					return
				}
			}
		})
	}
	group.Wait()
	if first.dir != dirs[0] {
		t.Fatalf("later inspections changed an earlier result: %q", first.dir)
	}
}
