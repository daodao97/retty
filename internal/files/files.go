// Package files provides bounded, read-only access to a host's files. It is
// shared by the local UI and the authenticated remote bridge, not the PTY daemon.
package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"retty/internal/gitstate"
)

const MaxRead = 4 << 20
const MaxEntries = 5000
const MaxPreviewRows = 20000

type Request struct {
	Base    string `json:"base,omitempty"`
	Session string `json:"session,omitempty"`
	Root    string `json:"root,omitempty"`
	OldTree string `json:"oldTree,omitempty"`
	NewTree string `json:"newTree,omitempty"`
	Turn    string `json:"turn,omitempty"`
	Op      string `json:"op"`
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Hidden  bool   `json:"hidden,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Length  int    `json:"length,omitempty"`
	Version string `json:"version,omitempty"`
}

type Entry struct {
	Name      string `json:"name"`
	Directory bool   `json:"directory,omitempty"`
	Link      bool   `json:"link,omitempty"`
	Regular   bool   `json:"regular,omitempty"`
	Size      int64  `json:"size"`
	Modified  int64  `json:"modified"`
	Version   string `json:"version"`
	Status    string `json:"status,omitempty"`
}

type Result struct {
	Comparison *gitstate.Comparison `json:"comparison,omitempty"`
	Git        *gitstate.Status     `json:"git,omitempty"`
	Version    int                  `json:"version,omitempty"`
	Path       string               `json:"path"`
	Entries    []Entry              `json:"entries,omitempty"`
	Entry      *Entry               `json:"entry,omitempty"`
	Data       []byte               `json:"data,omitempty"`
	Offset     int64                `json:"offset,omitempty"`
	Truncated  bool                 `json:"truncated,omitempty"`
}

func infoEntry(info os.FileInfo) Entry {
	return Entry{Name: info.Name(), Directory: info.IsDir(), Regular: info.Mode().IsRegular(), Size: info.Size(), Modified: info.ModTime().Unix(), Version: fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())}
}

// Keep keys aligned with entries while sorting. Computing Unicode lower-case
// names in Less would allocate again for every comparison in a large directory.
type entryOrder struct {
	entries []Entry
	folded  []string
}

func (s entryOrder) Len() int { return len(s.entries) }
func (s entryOrder) Less(i, j int) bool {
	a, b := s.entries[i], s.entries[j]
	if a.Directory != b.Directory {
		return a.Directory
	}
	if s.folded[i] == s.folded[j] {
		return a.Name < b.Name
	}
	return s.folded[i] < s.folded[j]
}
func (s entryOrder) Swap(i, j int) {
	s.entries[i], s.entries[j] = s.entries[j], s.entries[i]
	s.folded[i], s.folded[j] = s.folded[j], s.folded[i]
}

func sortEntries(entries []Entry) {
	keys := make([]string, len(entries))
	for i := range entries {
		keys[i] = strings.ToLower(entries[i].Name)
	}
	sort.Sort(entryOrder{entries, keys})
}

func absolute(name string) (string, error) {
	if name == "" || name == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(name, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		name = filepath.Join(home, name[2:])
	}
	if len(name) > 8192 || strings.ContainsRune(name, 0) || !filepath.IsAbs(name) {
		return "", errors.New("请输入绝对路径")
	}
	return filepath.Clean(name), nil
}

func Do(ctx context.Context, req Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if req.Kind == "capabilities" {
		return Result{Version: 1}, nil
	}
	path, err := absolute(req.Path)
	if err != nil {
		return Result{}, err
	}
	r := Result{Path: path, Version: 1}
	switch req.Kind {
	case "git-changes", "git-diff":
		return snapshotRequest(ctx, req, path)
	case "git-status":
		status, err := gitstate.Read(ctx, path)
		if errors.Is(err, gitstate.ErrNotRepository) {
			return r, nil
		}
		if err == nil {
			r.Git = &status
		}
		return r, err
	case "list":
		f, err := openRead(path)
		if err != nil {
			return r, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return r, err
		}
		if !st.IsDir() {
			return r, errors.New("此路径不是目录")
		}
		// Bound traversal as well as the wire response. Hidden entries count
		// toward the scan bound so a dot-file directory cannot exhaust memory.
		entries, err := f.ReadDir(MaxEntries + 1)
		if err != nil && err != io.EOF {
			return r, err
		}
		r.Truncated = len(entries) > MaxEntries
		for _, item := range entries[:min(len(entries), MaxEntries)] {
			if err := ctx.Err(); err != nil {
				return r, err
			}
			if !req.Hidden && strings.HasPrefix(item.Name(), ".") {
				continue
			}
			st, err := item.Info()
			if err != nil {
				continue
			}
			e := infoEntry(st)
			if st.Mode()&os.ModeSymlink != 0 {
				e.Link = true
				if target, err := os.Stat(filepath.Join(path, item.Name())); err == nil {
					e = infoEntry(target)
					e.Name = item.Name()
					e.Link = true
				}
			}
			r.Entries = append(r.Entries, e)
		}
		sortEntries(r.Entries)
		return r, nil
	case "stat":
		st, err := os.Stat(path)
		if err != nil {
			return r, err
		}
		e := infoEntry(st)
		r.Entry = &e
		return r, nil
	case "read":
		if req.Offset < 0 || req.Length <= 0 || req.Length > MaxRead {
			return r, errors.New("无效的文件读取范围")
		}
		// Open nonblocking on Unix: a file replaced with a FIFO between stat
		// and open must not hang the bridge. Verify the opened descriptor.
		f, err := openRead(path)
		if err != nil {
			return r, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return r, err
		}
		if !st.Mode().IsRegular() {
			return r, errors.New("仅支持读取普通文件")
		}
		e := infoEntry(st)
		r.Entry = &e
		if req.Version != "" && req.Version != e.Version {
			return r, errors.New("文件已变化，请刷新后重试")
		}
		if req.Offset > st.Size() {
			return r, errors.New("文件已缩短，请刷新")
		}
		r.Data = make([]byte, min(int64(req.Length), st.Size()-req.Offset))
		r.Offset = req.Offset
		n, err := f.ReadAt(r.Data, req.Offset)
		if err != nil && err != io.EOF {
			return r, err
		}
		r.Data = r.Data[:n]
		after, err := f.Stat()
		if err != nil {
			return r, err
		}
		if infoEntry(after).Version != e.Version {
			return r, errors.New("文件已变化，请刷新后重试")
		}
		r.Truncated = req.Offset+int64(n) < st.Size()
		return r, ctx.Err()
	case "diff":
		data, err := gitDiff(ctx, path)
		r.Data = data
		return r, err
	case "changes":
		return gitChanges(ctx, path)
	default:
		return r, errors.New("不支持的文件操作")
	}
}

// limitedBuffer stops an external tool before unbounded output is allocated.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, errors.New("Git 输出过大")
	}
	return b.Buffer.Write(data)
}
