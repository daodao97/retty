package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/terminal"
)

func TestDroppedFilePathsRoundTripAsLiteralShellArguments(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("requires a shell with ANSI-C quoting")
	}
	marker := filepath.Join(t.TempDir(), "must-not-execute")
	paths := []string{
		"/tmp/simple.txt",
		"/tmp/中文 文件.txt",
		"/tmp/a'b\"c\\d.txt",
		"/tmp/$(touch " + marker + ")",
		"/tmp/line\nbreak\t'name\x1b.txt",
	}
	text := droppedFilePaths(paths)
	if strings.ContainsAny(text, "\n\r\t\x1b") {
		t.Fatal("file paths emitted terminal input controls")
	}
	got, err := exec.Command(bash, "--noprofile", "--norc", "-c", "set -- "+text+"; printf '%s\\0' \"$@\"").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(strings.Join(paths, "\x00") + "\x00")
	if !bytes.Equal(got, want) {
		t.Fatalf("shell changed dropped file paths: got %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("file path executed shell syntax", err)
	}
}

func TestFileDropPastesIntoTargetPaneWithoutSubmitting(t *testing.T) {
	for _, bracketed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "bracketed"}[bracketed], func(t *testing.T) {
			conn, peer := net.Pipe()
			defer peer.Close()
			term, err := terminal.New(terminal.Options{Conn: conn})
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			if bracketed {
				term.Feed([]byte("\x1b[?2004h"))
			}
			tab := &Tab{Focus: &Pane{ID: 1, content: &terminalPane{}}}
			p := &Pane{ID: 2, Tab: tab, content: &terminalPane{term: term}}
			a := &App{tabs: []*Tab{tab}}
			a.pasteDroppedFiles(tab, p, []string{"/tmp/first file.txt", "/tmp/second.txt"})
			want := "'/tmp/first file.txt' /tmp/second.txt "
			if bracketed {
				want = "\x1b[200~" + want + "\x1b[201~"
			}
			peer.SetReadDeadline(time.Now().Add(time.Second))
			got := make([]byte, len(want))
			if _, err := io.ReadFull(peer, got); err != nil || string(got) != want {
				t.Fatalf("drop sent %q, want %q: %v", got, want, err)
			}
			if tab.Focus != p || a.focusReq != p {
				t.Fatal("drop did not focus its target pane")
			}
			peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			if n, _ := peer.Read(make([]byte, 1)); n != 0 {
				t.Fatal("drop submitted extra input")
			}
		})
	}
}

func TestFileDropIgnoresEmptyDropsAndDisconnectedRemotePanes(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	original := &Pane{ID: 1, content: &terminalPane{}}
	tab := &Tab{Focus: original}
	p := &Pane{ID: 2, Tab: tab, content: &terminalPane{term: term}}
	a := &App{tabs: []*Tab{tab}}
	a.pasteDroppedFiles(tab, p, []string{""})
	p.host = &desktopHost{}
	a.pasteDroppedFiles(tab, p, []string{"/tmp/local.txt"})
	if tab.Focus != original || a.focusReq != nil {
		t.Fatal("ignored drop changed focus")
	}
	peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if n, _ := peer.Read(make([]byte, 1)); n != 0 {
		t.Fatal("ignored drop sent input")
	}
}
