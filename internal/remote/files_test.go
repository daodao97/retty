package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"retty/internal/files"
	"retty/internal/gitstate"
	"retty/internal/rex"
	"strings"
	"testing"
	"time"
)

func TestBrowseTurnDiffUsesHostSnapshotWithoutDaemon(t *testing.T) {
	data := t.TempDir()
	t.Setenv("RETTY_DIR", data)
	c, _, socket, _ := fileUploadFixture(t)
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-qb", "main", dir).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("before\n"), 0600)
	store := gitstate.SnapshotStore{Dir: filepath.Join(data, "git-snapshots")}
	if _, err := store.CaptureTurn(context.Background(), dir, "pane", "turn"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("after\n"), 0600)
	r, err := BrowseFiles(context.Background(), c, files.Request{Kind: "git-changes", Path: dir, Session: "pane", Base: "turn", Turn: "turn"})
	if err != nil || r.Comparison == nil || len(r.Entries) != 1 {
		t.Fatal("remote snapshot request touched the unavailable daemon", r, err)
	}
	r, err = BrowseFiles(context.Background(), c, files.Request{Kind: "git-diff", Path: path, Root: r.Path, OldTree: r.Comparison.Base, NewTree: r.Comparison.Current.Tree})
	if err != nil || !strings.Contains(string(r.Data), "-before") || !strings.Contains(string(r.Data), "+after") {
		t.Fatal("remote snapshot patch did not use host bytes", string(r.Data), err)
	}
}

func TestBrowseFilesUsesBridgeWithoutDaemonOrInput(t *testing.T) {
	c, _, socket, _ := fileUploadFixture(t)
	// There is no daemon socket at all: browsing must be served by the bridge.
	// Removing this owned fixture socket leaves the fixture listener intact.
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	os.WriteFile(path, []byte("artifact"), 0600)
	r, err := BrowseFiles(context.Background(), c, files.Request{Kind: "list", Path: dir})
	if err != nil || len(r.Entries) != 1 {
		t.Fatal(r, err)
	}
	r, err = BrowseFiles(context.Background(), c, files.Request{Kind: "read", Path: path, Length: 3})
	if err != nil || string(r.Data) != "art" || !r.Truncated {
		t.Fatal(r, err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
		r, err = BrowseFiles(context.Background(), c, files.Request{Kind: "changes", Path: dir})
		if err != nil || len(r.Entries) != 1 || r.Entries[0].Name != "artifact.txt" || r.Entries[0].Status != "??" {
			t.Fatal(r, err)
		}
		r, err = BrowseFiles(context.Background(), c, files.Request{Kind: "git-status", Path: dir})
		if err != nil || r.Git == nil || r.Git.Files != 1 || r.Git.Added != 1 {
			t.Fatal("Git status must work without contacting the daemon", r, err)
		}
	}

}

func TestBrowseFilesLegacyAndCancellation(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		control, peer := net.Pipe()
		defer control.Close()
		defer peer.Close()
		c := rex.NewClient(control, func(context.Context) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				var req files.Request
				json.NewDecoder(server).Decode(&req)
				if legacy {
					json.NewEncoder(server).Encode(rex.Response{Error: `unknown op "files-browse"`})
				} else {
					var b [1]byte
					server.Read(b[:])
				}
			}()
			return client, nil
		})
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := BrowseFiles(ctx, c, files.Request{Kind: "list", Path: "/"})
		cancel()
		if legacy && !errors.Is(err, ErrFileBrowseUnsupported) {
			t.Fatal(err)
		}
		if !legacy && err == nil {
			t.Fatal("cancelled stream returned success")
		}
	}
}
