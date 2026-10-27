package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"retty/internal/rex"
	"retty/internal/terminal"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func remoteUploadPaneFixture(t *testing.T, failure string) (*App, *Pane, *ui.Tester, net.Conn, <-chan []byte, chan struct{}, <-chan struct{}) {
	t.Helper()
	control, controlPeer := net.Pipe()
	t.Cleanup(func() { controlPeer.Close() })
	received := make(chan []byte, 1)
	release := make(chan struct{})
	serverDone := make(chan struct{})
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	client := rex.NewClient(control, func(context.Context) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer close(serverDone)
			defer remote.Close()
			reader := bufio.NewReader(remote)
			line, err := reader.ReadString('\n')
			var req rex.FileUploadRequest
			if err != nil || json.Unmarshal([]byte(line), &req) != nil {
				return
			}
			if req.SID != "remote-pane" {
				t.Error("wrong upload target", req.SID)
			}
			if failure != "" {
				json.NewEncoder(remote).Encode(rex.Response{Error: failure})
				return
			}
			json.NewEncoder(remote).Encode(rex.Response{Data: json.RawMessage(`{"ready":true}`)})
			total, err := rex.ValidateFileUpload(req)
			if err != nil {
				t.Error(err)
				return
			}
			body := make([]byte, total)
			if _, err := io.ReadFull(reader, body); err != nil {
				return
			}
			line, err = reader.ReadString('\n')
			if err != nil || !strings.Contains(line, `"complete":true`) {
				t.Error("no completion marker", err)
				return
			}
			received <- body
			select {
			case <-release:
			case <-stop:
				return
			}
			path := "/srv/Retty/uploads/batch-123/001/a'b.txt"
			if len(req.Roots) == 1 && strings.HasSuffix(req.Roots[0], "/clipboard.png") {
				path = "/srv/Retty/uploads/batch-123/001/clipboard.png"
			}
			data, _ := json.Marshal(rex.FileUploadReply{Paths: []string{path}})
			json.NewEncoder(remote).Encode(rex.Response{Data: data})
		}()
		return local, nil
	})
	t.Cleanup(func() { client.Close() })
	conn, peer := net.Pipe()
	t.Cleanup(func() { peer.Close() })
	view := newSessionViewStream(testMobileTransport{conn}, nil)
	term, err := terminal.New(terminal.Options{Conn: view})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	term.Feed([]byte("\x1b[?2004h"))
	h := &desktopHost{client: client, generation: 1}
	p := &Pane{ID: 1, host: h, content: &terminalPane{SID: "remote-pane", term: term, remoteView: view}}
	tab := &Tab{ID: 1, Host: h, Root: &Node{ID: 1, Pane: p}, Focus: p}
	p.Tab, p.Node = tab, tab.Root
	a := &App{tabs: []*Tab{tab}, activeRemote: p, focusedWin: true}
	registerFonts()
	tt := ui.NewTester(func(c *ui.Context) {
		a.runPosted()
		a.services = c.Services()
		a.paneCard(c, colorsOf(c), tab, p).Fill()
	}, 700, 300)
	return a, p, tt, peer, received, release, serverDone
}

func localUploadFixture(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "local ' file.txt")
	if err := os.WriteFile(name, []byte("file bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func assertNoUploadInput(t *testing.T, peer net.Conn) {
	t.Helper()
	peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if n, _ := peer.Read(make([]byte, 1)); n != 0 {
		t.Fatal("upload sent premature or cancelled terminal input")
	}
}

func TestRemoteFileDropWaitsForRemoteConfirmationThenPastesRemotePath(t *testing.T) {
	a, p, tt, peer, received, release, _ := remoteUploadPaneFixture(t, "")
	a.pasteDroppedFiles(p.Tab, p, []string{localUploadFixture(t)})
	select {
	case body := <-received:
		if string(body) != "file bytes" {
			t.Fatal("file bytes changed")
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not start")
	}
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), " "), "正在确认上传") {
		t.Fatal("upload status missing", tt.Texts())
	}
	assertNoUploadInput(t, peer)
	// An upload waiting for confirmation must not occupy the terminal stream.
	p.terminal().term.Paste("user input")
	manual := "\x1b[200~user input\x1b[201~"
	peer.SetReadDeadline(time.Now().Add(time.Second))
	input := make([]byte, len(manual))
	if _, err := io.ReadFull(peer, input); err != nil || string(input) != manual {
		t.Fatal("upload blocked ordinary terminal input", err)
	}
	close(release)
	waitFor(t, tt, "remote upload confirmation", func() bool { return p.terminal().upload == nil })
	want := "\x1b[200~" + droppedFilePaths([]string{"/srv/Retty/uploads/batch-123/001/a'b.txt"}) + "\x1b[201~"
	peer.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil || string(got) != want {
		t.Fatal("did not paste quoted remote path", string(got), err)
	}
	assertNoUploadInput(t, peer)
	if a.err != "" {
		t.Fatal(a.err)
	}
}

func TestRemoteFileDropCancelAndPaneSwitchDiscardLateConfirmation(t *testing.T) {
	for _, action := range []string{"cancel", "switch", "reconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			a, p, tt, peer, received, release, done := remoteUploadPaneFixture(t, "")
			a.pasteDroppedFiles(p.Tab, p, []string{localUploadFixture(t)})
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("upload did not start")
			}
			tt.Frame()
			switch action {
			case "cancel":
				if err := tt.Click("取消文件上传"); err != nil {
					t.Fatal(err)
				}
			case "switch":
				a.pauseRemotePane(p)
			case "reconnect":
				p.host.generation++
			case "close":
				p.closed = true
				a.closePaneTerminal(p)
			}
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancelled upload did not stop")
			}
			waitFor(t, tt, "upload cleared", func() bool { return p.terminal().upload == nil })
			assertNoUploadInput(t, peer)
			if a.err != "" {
				t.Fatal("cancellation surfaced an error", a.err)
			}
		})
	}
}

func TestRemoteFileDropUnsupportedShowsErrorWithoutPastingLocalPath(t *testing.T) {
	a, p, tt, peer, _, _, _ := remoteUploadPaneFixture(t, `unknown op "upload-files"`)
	a.pasteDroppedFiles(p.Tab, p, []string{localUploadFixture(t)})
	waitFor(t, tt, "unsupported upload result", func() bool { return p.terminal().upload == nil })
	if !strings.Contains(a.err, "更新远端") {
		t.Fatal("missing upgrade hint", a.err)
	}
	assertNoUploadInput(t, peer)
}
