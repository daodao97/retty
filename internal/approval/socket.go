package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Short, per-user/data-directory namespace also works with macOS Unix path
// limits. Only sockets and expiring viewer timestamps exist on disk.
func directory(dataDir string) string {
	p, _ := filepath.Abs(dataDir)
	if real, e := filepath.EvalSymlinks(p); e == nil {
		p = real
	}
	h := sha256.Sum256([]byte(p))
	return filepath.Join("/tmp", fmt.Sprintf("retty-approval-%d-%x", os.Getuid(), h[:8]))
}
func pane(sid string) string { h := sha256.Sum256([]byte(sid)); return hex.EncodeToString(h[:8]) }
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("unsafe approval directory")
	}
	return nil
}

// TouchViewer is called only by a foreground phone viewing this exact pane.
// Hooks without a recent viewer return immediately to ordinary CLI prompting.
func TouchViewer(dataDir, sid string) error {
	dir := directory(dataDir)
	if err := privateDir(dir); err != nil {
		return err
	}
	p := filepath.Join(dir, pane(sid)+".viewer")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	f.Close()
	return os.Chtimes(p, time.Now(), time.Now())
}
func ViewerActive(dataDir, sid string) bool {
	st, err := os.Lstat(filepath.Join(directory(dataDir), pane(sid)+".viewer"))
	return err == nil && st.Mode().IsRegular() && time.Since(st.ModTime()) >= 0 && time.Since(st.ModTime()) < 6*time.Second
}

type wireRequest struct {
	Op     string `json:"op"`
	SID    string `json:"sid"`
	Thread string `json:"thread"`
	ID     string `json:"id,omitempty"`
	Choice string `json:"choice,omitempty"`
}
type wireResponse struct {
	Requests []Request `json:"requests,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func Listen(ctx context.Context, dataDir, sid string, s *Store) (func(), error) {
	dir := directory(dataDir)
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, pane(sid)+"-"+s.prefix+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				var req wireRequest
				res := wireResponse{}
				if json.NewDecoder(io.LimitReader(conn, 16385)).Decode(&req) != nil || req.SID != sid {
					res.Error = "invalid approval request"
				} else {
					switch req.Op {
					case "list":
						res.Requests = s.List(sid, req.Thread)
					case "answer":
						if err := s.Answer(sid, req.Thread, req.ID, req.Choice); err != nil {
							res.Error = err.Error()
						}
					default:
						res.Error = "unsupported approval operation"
					}
				}
				json.NewEncoder(conn).Encode(res)
			}()
		}
	}()
	return func() { stop(); ln.Close() }, nil
}

func call(ctx context.Context, path string, req wireRequest) (wireResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return wireResponse{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	d, _ := ctx.Deadline()
	c.SetDeadline(d)
	if err = json.NewEncoder(c).Encode(req); err != nil {
		return wireResponse{}, err
	}
	var res wireResponse
	err = json.NewDecoder(io.LimitReader(c, 3<<20)).Decode(&res)
	if err == nil && res.Error != "" {
		err = errors.New(res.Error)
	}
	return res, err
}
func List(ctx context.Context, dataDir, sid, thread string) ([]Request, error) {
	if err := TouchViewer(dataDir, sid); err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(directory(dataDir), pane(sid)+"-*.sock"))
	if err != nil {
		return nil, err
	}
	if len(paths) > 64 {
		return nil, errors.New("too many approval endpoints")
	}
	var out []Request
	for _, p := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSocket == 0 {
			continue
		}
		res, err := call(ctx, p, wireRequest{Op: "list", SID: sid, Thread: thread})
		if err == nil {
			out = append(out, res.Requests...)
		}
	}
	return out, nil
}
func Answer(ctx context.Context, dataDir, sid, thread, id, choice string) error {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 32 {
		return ErrStale
	}
	for _, p := range parts {
		if _, err := hex.DecodeString(p); err != nil {
			return ErrStale
		}
	}
	_, err := call(ctx, filepath.Join(directory(dataDir), pane(sid)+"-"+parts[0]+".sock"), wireRequest{Op: "answer", SID: sid, Thread: thread, ID: id, Choice: choice})
	return err
}
