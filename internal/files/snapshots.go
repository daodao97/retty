package files

import (
	"context"
	"errors"
	"path/filepath"

	"retty/internal/gitstate"
)

type snapshotStoreKey struct{}

// WithSnapshotStore is set only by the owning host, never by a wire request.
func WithSnapshotStore(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, snapshotStoreKey{}, dir)
}

func snapshotRequest(ctx context.Context, req Request, path string) (Result, error) {
	dir, _ := ctx.Value(snapshotStoreKey{}).(string)
	if !filepath.IsAbs(dir) {
		return Result{}, errors.New("此主机尚未提供轮次 Diff 存储")
	}
	store := gitstate.SnapshotStore{Dir: dir}
	r := Result{Version: 1, Path: path}
	if req.Kind == "git-diff" {
		root, err := absolute(req.Root)
		if err != nil || req.Root == "" {
			return r, errors.New("缺少差异所属的工作区")
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return r, err
		}
		// Canonicalize only existing ancestors. Deleted files must still be
		// diffable, and a symlink file itself must not be followed to its target.
		parent := existingGitDirectory(filepath.Dir(path))
		canonical, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return r, err
		}
		suffix, err := filepath.Rel(parent, path)
		if err != nil {
			return r, err
		}
		path = filepath.Join(canonical, suffix)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return r, err
		}
		r.Data, err = store.Diff(ctx, root, req.OldTree, req.NewTree, rel)
		return r, err
	}
	if req.Session == "" || len(req.Session) > 1024 {
		return r, errors.New("缺少 Git 变更所属的会话")
	}
	if req.Base == "turn" && req.Turn == "" {
		return r, errors.New("尚未收到可验证的轮次基线；旧服务需在任务结束后安全更新，新一轮开始后可用")
	}
	comparison, err := store.CompareTurn(ctx, path, req.Session, req.Base, req.Turn)
	if err != nil {
		return r, err
	}
	r.Path, r.Truncated = comparison.Root, comparison.Truncated
	for _, change := range comparison.Files {
		r.Entries = append(r.Entries, Entry{Name: change.Path, Status: change.Status, Regular: true})
	}
	// Skipped paths belong to the private snapshot record; viewers need only
	// the partial-result flag and the immutable revisions for a file preview.
	comparison.Current.Skipped = nil
	r.Comparison = &comparison
	return r, nil
}
