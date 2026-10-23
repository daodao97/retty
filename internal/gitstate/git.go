// Package gitstate reads repository identity and status without modifying the
// worktree or the user's index. Hosts run it locally; viewers never resolve a
// remote path against their own filesystem.
package gitstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxOutput = 4 << 20

var ErrNotRepository = errors.New("当前目录不在 Git 仓库中")

type Repository struct {
	Root       string `json:"root"`
	GitDir     string `json:"gitDir"`
	CommonDir  string `json:"commonDir"`
	ProjectDir string `json:"projectDir"`
}

// Key identifies a project on one host, including every linked worktree.
// Callers add their own host identity before sharing a cache between hosts.
func (r Repository) Key() string { return r.CommonDir }

// BranchName reads the worktree's own HEAD, never another checkout's branch.
func (r Repository) BranchName() string {
	f, err := os.Open(filepath.Join(r.GitDir, "HEAD"))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(b))
	if branch, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return branch
	}
	return head[:min(7, len(head))]
}

type Status struct {
	Repository
	Branch    string `json:"branch"`
	Head      string `json:"head,omitempty"`
	Default   string `json:"default,omitempty"`
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
	Files     int    `json:"files,omitempty"`
	Added     int    `json:"added,omitempty"`
	Removed   int    `json:"removed,omitempty"`
	Operation string `json:"operation,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

func Identify(ctx context.Context, dir string) (Repository, error) {
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "\x00\r\n") {
		return Repository{}, ErrNotRepository
	}
	out, err := run(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		if ctx.Err() != nil {
			return Repository{}, ctx.Err()
		}
		return Repository{}, ErrNotRepository
	}
	parts := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(parts) != 3 {
		return Repository{}, ErrNotRepository
	}
	for i, part := range parts {
		if !filepath.IsAbs(part) {
			return Repository{}, ErrNotRepository
		}
		parts[i] = filepath.Clean(part)
		if real, err := filepath.EvalSymlinks(parts[i]); err == nil {
			parts[i] = real
		}
	}
	r := Repository{Root: parts[0], GitDir: parts[1], CommonDir: parts[2], ProjectDir: parts[0]}
	if filepath.Base(r.CommonDir) == ".git" {
		r.ProjectDir = filepath.Dir(r.CommonDir)
	} else if r.GitDir != r.CommonDir {
		// --separate-git-dir repositories cannot derive their main checkout
		// from the metadata directory's name. The first entry is its main one.
		if out, err := run(ctx, dir, "worktree", "list", "--porcelain", "-z"); err == nil {
			if first, _, _ := strings.Cut(string(out), "\x00"); strings.HasPrefix(first, "worktree ") {
				r.ProjectDir = strings.TrimPrefix(first, "worktree ")
			}
		}
	}
	return r, nil
}

func Read(ctx context.Context, dir string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := Identify(ctx, dir)
	if err != nil {
		return Status{}, err
	}
	s := Status{Repository: r}
	out, err := run(ctx, r.Root, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return s, err
	}
	var untracked []string
	records := strings.Split(string(out), "\x00")
	for i := 0; i < len(records); i++ {
		line := records[i]
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			s.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			s.Head = strings.TrimPrefix(line, "# branch.oid ")
			if s.Head == "(initial)" {
				s.Head = ""
			}
		case strings.HasPrefix(line, "# branch.ab "):
			fmt.Sscanf(line, "# branch.ab +%d -%d", &s.Ahead, &s.Behind)
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "u "):
			s.Files++
		case strings.HasPrefix(line, "2 "):
			s.Files++
			i++ // the original filename is the next NUL-delimited record
		case strings.HasPrefix(line, "? "):
			s.Files++
			untracked = append(untracked, line[2:])
		}
	}
	if s.Branch == "(detached)" {
		s.Branch = s.Head[:min(7, len(s.Head))]
	}
	if s.Head != "" {
		out, err = run(ctx, r.Root, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--numstat", "-z", "HEAD", "--")
		if err != nil {
			return s, err
		}
		records := strings.Split(string(out), "\x00")
		for i := 0; i < len(records); i++ {
			fields := strings.SplitN(records[i], "\t", 3)
			if len(fields) != 3 {
				continue
			}
			added, _ := strconv.Atoi(fields[0])
			removed, _ := strconv.Atoi(fields[1])
			s.Added, s.Removed = s.Added+added, s.Removed+removed
			if fields[2] == "" {
				i += 2 // rename: two additional NUL-delimited paths
			}
		}
	} else {
		// Files staged before the first commit also have no HEAD baseline.
		out, err = run(ctx, r.Root, "ls-files", "-z")
		if err != nil {
			return s, err
		}
		for _, name := range strings.Split(string(out), "\x00") {
			if name != "" {
				untracked = append(untracked, name)
			}
		}
	}
	// Bound both the files and the combined bytes read for this summary.
	budget := int64(8 << 20)
	for i, name := range untracked {
		if i >= 200 || budget <= 0 {
			s.Truncated = true
			break
		}
		path := filepath.Join(r.Root, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > 1<<20 || fi.Size() > budget {
			s.Truncated = true
			continue
		}
		budget -= fi.Size()
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, fi.Size()+1))
		f.Close()
		if err != nil || int64(len(b)) != fi.Size() || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		s.Added += bytes.Count(b, []byte{'\n'})
		if len(b) > 0 && b[len(b)-1] != '\n' {
			s.Added++
		}
	}
	for _, op := range []struct{ path, name string }{{"rebase-merge", "rebase"}, {"rebase-apply", "rebase"}, {"MERGE_HEAD", "merge"}, {"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}} {
		if _, err := os.Stat(filepath.Join(r.GitDir, op.path)); err == nil {
			s.Operation = op.name
			break
		}
	}
	if b, err := os.ReadFile(filepath.Join(r.CommonDir, "refs/remotes/origin/HEAD")); err == nil {
		s.Default = strings.TrimPrefix(strings.TrimSpace(string(b)), "ref: refs/remotes/origin/")
	}
	if s.Default == "" {
		for _, branch := range []string{"main", "master"} {
			if _, err := run(ctx, r.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
				s.Default = branch
				break
			}
		}
	}
	return s, ctx.Err()
}

// run never inherits Git overrides or executes fsmonitor/diff/textconv hooks.
// Output caps also cover names supplied by a repository, not just file data.
func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runInput(ctx, dir, nil, nil, args...)
}

// ReadCommand shares the same bounded, filter-free Git reads with the file
// browser. Mutation commands are intentionally unavailable through this API.
func ReadCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("缺少 Git 读取命令")
	}
	switch args[0] {
	case "status", "diff", "rev-parse", "ls-files", "cat-file", "show-ref", "merge-base":
		return run(ctx, dir, args...)
	default:
		return nil, errors.New("不支持的 Git 读取命令")
	}
}

func runInput(ctx context.Context, dir string, env []string, input io.Reader, args ...string) ([]byte, error) {
	prefix := []string{"--no-pager", "--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "diff.external="}
	if len(args) > 0 && (args[0] == "status" || args[0] == "diff") {
		// --no-textconv alone does not stop clean/process filters when Git
		// compares a worktree file. Disable every configured filter driver.
		config, err := runInput(ctx, dir, nil, nil, "config", "--null", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			return nil, err
		}
		for _, entry := range strings.Split(string(config), "\x00") {
			key, _, found := strings.Cut(entry, "\n")
			if !found {
				continue
			}
			value := ""
			if strings.HasSuffix(key, ".required") {
				value = "false"
			}
			prefix = append(prefix, "-c", key+"="+value)
		}
	}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.Dir = dir
	cmd.Stdin = input
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	out := &boundedBuffer{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = out, &boundedBuffer{limit: 4096}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out.Bytes(), err
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("Git 输出过大")
	}
	return b.Buffer.Write(p)
}
