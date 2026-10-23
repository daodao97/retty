package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"retty/internal/gitstate"
)

// All Git operations are bounded and read-only. In particular status must not
// refresh/write the real index or invoke a repository's fsmonitor executable.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitstate.ReadCommand(ctx, dir, args...)
}

func existingGitDirectory(dir string) string {
	for {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func gitChanges(ctx context.Context, path string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	root, err := runGit(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Result{}, errors.New("当前目录不在 Git 仓库中")
	}
	dir := strings.TrimSuffix(string(root), "\n")
	data, err := runGit(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return Result{}, errors.New("无法读取 Git 变更列表，或列表过大")
	}
	r := Result{Version: 1, Path: dir}
	records := strings.Split(string(data), "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		status, name := record[:2], record[3:]
		// -z emits the destination first and the original name in a second record.
		if strings.ContainsAny(status, "RC") {
			i++
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue
		}
		if len(r.Entries) == MaxEntries {
			r.Truncated = true
			break
		}
		e := Entry{Name: clean, Regular: true, Status: strings.TrimSpace(status)}
		if st, err := os.Lstat(filepath.Join(dir, clean)); err == nil {
			e = infoEntry(st)
			e.Name = clean
			e.Status = strings.TrimSpace(status)
		}
		r.Entries = append(r.Entries, e)
	}
	sort.Slice(r.Entries, func(i, j int) bool { return r.Entries[i].Name < r.Entries[j].Name })
	return r, ctx.Err()
}
