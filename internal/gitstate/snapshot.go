package gitstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"retty/internal/atomicfile"
)

const MaxSnapshotFiles = 5000
const MaxSnapshotBytes = 32 << 20
const MaxSnapshotFile = 4 << 20

// SnapshotStore keeps content in Retty's private data directory, never in the
// user's repository. Its bare Git store has no user index and imports raw bytes
// through fast-import: git add/clean filters and project hooks never run.
type SnapshotStore struct{ Dir string }

type Snapshot struct {
	Turn      string    `json:"turn,omitempty"`
	Root      string    `json:"root"`
	Tree      string    `json:"tree"`
	Reference string    `json:"reference,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	At        time.Time `json:"at"`
	Error     string    `json:"error,omitempty"`
	Skipped   []string  `json:"skipped,omitempty"`
}

type Change struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type Comparison struct {
	Root      string   `json:"root"`
	Base      string   `json:"base"`
	Current   Snapshot `json:"current"`
	Files     []Change `json:"files"`
	Truncated bool     `json:"truncated,omitempty"`
}

func snapshotKey(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func (s SnapshotStore) directory(r Repository) string {
	return filepath.Join(s.Dir, snapshotKey(r.CommonDir))
}

func validObject(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (s SnapshotStore) command(ctx context.Context, r Repository, input io.Reader, args ...string) ([]byte, error) {
	dir := s.directory(r)
	// HEAD/branch trees are read from the source object database. All new
	// objects and references are written only to the private store.
	env := []string{"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + quotedGitPath(filepath.Join(r.CommonDir, "objects")), "GIT_INDEX_FILE=" + filepath.Join(dir, "snapshot-index")}
	return runInput(ctx, r.Root, env, input, append([]string{"--git-dir=" + filepath.Join(dir, "repo.git")}, args...)...)
}

func (s SnapshotStore) initialize(ctx context.Context, r Repository) error {
	if !filepath.IsAbs(s.Dir) {
		return errors.New("快照目录必须为绝对路径")
	}
	dir := filepath.Join(s.directory(r), "repo.git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".init-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	format, err := run(ctx, r.Root, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	if _, err = run(ctx, r.Root, "init", "--bare", "-q", "--object-format="+strings.TrimSpace(string(format)), staging); err != nil {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		// Another request may have completed the same initialization. Only
		// a complete, atomically installed repository is reused.
		if _, statErr := os.Stat(filepath.Join(dir, "HEAD")); statErr != nil {
			return err
		}
	}
	return nil
}

// Capture replaces the baseline for key only after the complete tree exists.
// Call Invalidate at the next turn's boundary before attempting a new capture,
// so a failed capture can never leave a previous turn labelled as the new one.
func (s SnapshotStore) Capture(ctx context.Context, dir, key string) (Snapshot, error) {
	return s.CaptureTurn(ctx, dir, key, "")
}

func (s SnapshotStore) CaptureTurn(ctx context.Context, dir, key, turn string) (Snapshot, error) {
	if key == "" || len(key) > 1024 {
		return Snapshot{}, errors.New("无效的轮次会话身份")
	}
	r, err := Identify(ctx, dir)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.initialize(ctx, r); err != nil {
		return Snapshot{}, err
	}
	snap, err := s.capture(ctx, r, "refs/retty/turn/"+snapshotKey(key))
	if err != nil {
		return snap, err
	}
	snap.Turn = turn
	b, err := json.Marshal(snap)
	if err == nil {
		err = atomicfile.Write(filepath.Join(s.directory(r), snapshotKey(key)+".json"), b)
	}
	return snap, err
}

func (s SnapshotStore) Invalidate(ctx context.Context, dir, key, reason string) error {
	r, err := Identify(ctx, dir)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(Snapshot{Root: r.Root, At: time.Now(), Error: reason})
	return atomicfile.Write(filepath.Join(s.directory(r), snapshotKey(key)+".json"), b)
}

func (s SnapshotStore) Baseline(ctx context.Context, dir, key string) (Snapshot, error) {
	r, err := Identify(ctx, dir)
	if err != nil {
		return Snapshot{}, err
	}
	f, err := os.Open(filepath.Join(s.directory(r), snapshotKey(key)+".json"))
	if err != nil {
		return Snapshot{}, errors.New("尚未记录此会话的轮次基线；需由支持此功能的服务在新一轮开始时记录")
	}
	defer f.Close()
	var snap Snapshot
	if err := json.NewDecoder(io.LimitReader(f, MaxSnapshotBytes)).Decode(&snap); err != nil {
		return snap, err
	}
	if snap.Root != r.Root {
		return snap, errors.New("会话已切换工作目录，本轮基线属于另一个 Worktree")
	}
	if snap.Error != "" {
		return snap, errors.New(snap.Error)
	}
	if !validObject(snap.Tree) {
		return snap, errors.New("本轮快照无效")
	}
	return snap, nil
}

func quotedGitPath(path string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(path) {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 32 || c >= 127:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (s SnapshotStore) capture(ctx context.Context, r Repository, ref string) (Snapshot, error) {
	snap := Snapshot{Root: r.Root, Reference: ref, At: time.Now()}
	names, err := run(ctx, r.Root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return snap, err
	}
	paths := strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00")
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var input bytes.Buffer
	fmt.Fprintf(&input, "feature done\ncommit %s\nmark :1\ncommitter Retty <snapshot@local> 0 +0000\ndata 0\n\ndeleteall\n", ref)
	used := 0
	for i, name := range paths {
		if err := ctx.Err(); err != nil {
			return snap, err
		}
		if name == "" {
			continue
		}
		if i >= MaxSnapshotFiles {
			snap.Truncated = true
			snap.Skipped = append(snap.Skipped, paths[i:]...)
			break
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return snap, errors.New("Git 返回无效文件路径")
		}
		path := filepath.Join(r.Root, clean)
		fi, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue // a deleted file is absent from this tree
		}
		if err != nil {
			return snap, err
		}
		mode := "100644"
		var data []byte
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return snap, err
			}
			mode, data = "120000", []byte(target)
		} else if fi.Mode().IsRegular() {
			if fi.Size() > MaxSnapshotFile || fi.Size() > int64(MaxSnapshotBytes-used) {
				snap.Truncated = true
				snap.Skipped = append(snap.Skipped, name)
				continue
			}
			if fi.Mode()&0111 != 0 {
				mode = "100755"
			}
			f, err := os.Open(path)
			if err != nil {
				return snap, err
			}
			data, err = io.ReadAll(io.LimitReader(f, fi.Size()+1))
			after, statErr := f.Stat()
			f.Close()
			if err != nil || statErr != nil || int64(len(data)) != fi.Size() || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
				return snap, errors.New("文件在记录快照时发生变化，请在下一轮重新记录")
			}
		} else {
			// Submodule directories and non-regular devices are never traversed.
			snap.Skipped = append(snap.Skipped, name)
			continue
		}
		used += len(data)
		fmt.Fprintf(&input, "M %s inline %s\ndata %d\n", mode, quotedGitPath(filepath.ToSlash(clean)), len(data))
		input.Write(data)
		input.WriteByte('\n')
	}
	input.WriteString("\nget-mark :1\ndone\n")
	commit, err := s.command(ctx, r, &input, "fast-import", "--quiet", "--force", "--date-format=raw")
	if err != nil {
		return snap, err
	}
	object := strings.TrimSpace(string(commit))
	if !validObject(object) {
		return snap, errors.New("无法确认快照对象")
	}
	// Read this command's own result, rather than a ref another viewer may
	// already have advanced by the time this command finishes.
	tree, err := s.command(ctx, r, nil, "rev-parse", "--verify", object+"^{tree}")
	if err != nil {
		return snap, err
	}
	snap.Tree = strings.TrimSpace(string(tree))
	return snap, nil
}

// Compare captures a current tree once; callers can use its immutable object
// IDs for individual file diffs without rescanning the whole repository.
func (s SnapshotStore) Compare(ctx context.Context, dir, key, base string) (Comparison, error) {
	return s.CompareTurn(ctx, dir, key, base, "")
}

func (s SnapshotStore) CompareTurn(ctx context.Context, dir, key, base, expectedTurn string) (Comparison, error) {
	r, err := Identify(ctx, dir)
	if err != nil {
		return Comparison{}, err
	}
	if err := s.initialize(ctx, r); err != nil {
		return Comparison{}, err
	}
	result := Comparison{Root: r.Root}
	skipped := make(map[string]bool)
	switch base {
	case "turn":
		baseline, err := s.Baseline(ctx, dir, key)
		if err != nil {
			return result, err
		}
		if expectedTurn != "" && baseline.Turn != expectedTurn {
			return result, errors.New("本轮基线尚未就绪，或记录失败；不会使用上一轮快照")
		}
		result.Base, result.Truncated = baseline.Tree, baseline.Truncated
		for _, path := range baseline.Skipped {
			skipped[path] = true
		}
	case "", "head":
		out, err := run(ctx, r.Root, "rev-parse", "--verify", "HEAD^{tree}")
		if err != nil {
			// Before the first commit, compare against an empty private tree.
			out, err = s.command(ctx, r, strings.NewReader(""), "hash-object", "-w", "-t", "tree", "--stdin")
		}
		if err != nil {
			return result, err
		}
		result.Base = strings.TrimSpace(string(out))
	case "branch":
		status, err := Read(ctx, dir)
		if err != nil {
			return result, err
		}
		if status.Default == "" {
			return result, errors.New("无法确定默认分支")
		}
		ref := "refs/heads/" + status.Default
		if _, err := run(ctx, r.Root, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+status.Default); err == nil {
			ref = "refs/remotes/origin/" + status.Default
		}
		out, err := run(ctx, r.Root, "merge-base", "HEAD", ref)
		if err != nil {
			return result, errors.New("无法确定分支的共同祖先")
		}
		result.Base = strings.TrimSpace(string(out))
	default:
		return result, errors.New("不支持的 Git 比较基线")
	}
	// Keep one current reference per pane/baseline/worktree, not one per
	// refresh. Old previews keep immutable object IDs and Git's normal grace
	// period; the private store does not accumulate live refs on every click.
	result.Current, err = s.capture(ctx, r, "refs/retty/current/"+snapshotKey(key+"\x00"+base+"\x00"+r.Root))
	if err != nil {
		return result, err
	}
	result.Truncated = result.Truncated || result.Current.Truncated
	for _, path := range result.Current.Skipped {
		skipped[path] = true
	}
	out, err := s.command(ctx, r, nil, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--name-status", "-z", result.Base, result.Current.Tree, "--")
	if err != nil {
		return result, err
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != "" && !skipped[fields[i+1]] {
			result.Files = append(result.Files, Change{Path: fields[i+1], Status: fields[i]})
		}
	}
	return result, nil
}

func (s SnapshotStore) Diff(ctx context.Context, dir, base, current, path string) ([]byte, error) {
	if !validObject(base) || !validObject(current) || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
		return nil, errors.New("无效的 Git 差异请求")
	}
	r, err := Identify(ctx, dir)
	if err != nil {
		return nil, err
	}
	return s.command(ctx, r, nil, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--unified=3", base, current, "--", path)
}
