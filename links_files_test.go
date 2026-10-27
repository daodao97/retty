package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"retty/internal/files"
	"retty/internal/rex"
	"retty/internal/terminal"
)

func TestTerminalFileReferences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "中文 space #?.md")
	colon := filepath.Join(dir, "literal.md:12")
	if err := os.WriteFile(colon, []byte("literal"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		link   terminal.Link
		remote bool
		want   terminal.Link
	}{
		{"relative", terminal.Link{Path: "docs/automation-plan.md"}, false, terminal.Link{Path: filepath.Join(dir, "docs/automation-plan.md")}},
		{"home", terminal.Link{Path: "~/notes.md", Line: 3}, false, terminal.Link{Path: filepath.Join(dir, "notes.md"), Line: 3}},
		{"relative OSC8", terminal.Link{URL: "docs/automation-plan.md"}, false, terminal.Link{Path: filepath.Join(dir, "docs/automation-plan.md")}},
		{"escaped URL", terminal.Link{URL: (&url.URL{Scheme: "file", Path: path + ":12:3"}).String()}, false, terminal.Link{Path: path, Line: 12, Column: 3}},
		{"URL fragment", terminal.Link{URL: (&url.URL{Scheme: "file", Path: path, Fragment: "L8C2"}).String()}, false, terminal.Link{Path: path, Line: 8, Column: 2}},
		{"literal colon", terminal.Link{URL: (&url.URL{Scheme: "file", Path: colon}).String()}, false, terminal.Link{Path: colon}},
		{"relative literal colon", terminal.Link{URL: "./literal.md:12"}, false, terminal.Link{Path: colon}},
		{"remote ignores local file", terminal.Link{URL: (&url.URL{Scheme: "file", Path: colon}).String()}, true, terminal.Link{Path: filepath.Join(dir, "literal.md"), Line: 12}},
		{"remote relative", terminal.Link{Path: "src/main.go", Line: 4}, true, terminal.Link{Path: filepath.Join(dir, "src/main.go"), Line: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isFile, err := terminalFileReference(tc.link, dir, dir, tc.remote)
			if err != nil || !isFile || got != tc.want {
				t.Fatalf("got %+v, file=%t, err=%v; want %+v", got, isFile, err, tc.want)
			}
		})
	}
	for _, raw := range []string{"https://example.com/docs.md", "mailto:me@example.com", "vscode://file/tmp/a.go:12"} {
		if _, isFile, err := terminalFileReference(terminal.Link{URL: raw}, dir, dir, false); isFile || err != nil {
			t.Fatalf("external link misclassified: %s, %v", raw, err)
		}
	}
	for _, link := range []terminal.Link{{URL: "file://other-machine/tmp/a.md"}, {Path: "a.md\x00"}, {Path: ""}} {
		if _, _, err := terminalFileReference(link, dir, dir, false); err == nil {
			t.Fatalf("invalid file reference accepted: %+v", link)
		}
	}
	if _, _, err := terminalFileReference(terminal.Link{Path: "a.md"}, "", dir, false); err == nil {
		t.Fatal("relative file used application's working directory")
	}
}

func TestTerminalFileLinksOpenInRetty(t *testing.T) {
	for _, kind := range []string{"relative", "relative OSC8", "file URL", "line and column", "directory"} {
		t.Run(kind, func(t *testing.T) {
			previous := prefs
			prefs = settings{FontSize: defaultFontSize, FilesDiff: true}
			t.Cleanup(func() { prefs = previous })
			t.Setenv("RETTY_DIR", t.TempDir())
			dir := t.TempDir()
			path := filepath.Join(dir, "docs", "automation-plan.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			content := "# 规划\n\n内容不应写入终端\n"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			a, tt := newStaticTestApp(t)
			p := a.tab().Focus
			p.terminal().info.Dir = dir
			root := a.tab().Root
			external := ""
			a.openLinkURL = func(target string) error { external = target; return nil }
			t.Cleanup(func() {
				if a.focusedFiles() != nil {
					a.focusedFiles().stop()
				}
			})
			text := "(docs/automation-plan.md)。"
			switch kind {
			case "relative OSC8", "file URL":
				target := "docs/automation-plan.md"
				if kind == "file URL" {
					target = (&url.URL{Scheme: "file", Path: path, Fragment: "L3C2"}).String()
				}
				text = "\x1b]8;;" + target + "\x1b\\docs/automation-plan.md\x1b]8;;\x1b\\"
			case "line and column":
				text = "docs/automation-plan.md:3:2"
			case "directory":
				text = "docs/"
			}
			p.terminal().term.Feed([]byte("\x1b[H\x1b[2J" + text))
			tt.Frame()
			r, ok := tt.Find("Terminal")
			if !ok {
				t.Fatal("terminal not visible")
			}
			tt.ClickAtWith(ui.Cmd, r.X+14, r.Y+7)
			waitFor(t, tt, "file viewer", func() bool {
				return a.focusedFiles() != nil && !a.focusedFiles().loading && (kind == "directory" || a.focusedFiles().previewCode != nil)
			})
			b := a.focusedFiles()
			if external != "" || a.err != "" || b.err != "" || a.tab().Root != root || len(a.tab().panes()) != 2 || b.diff {
				t.Fatalf("file opening changed the session or failed: external=%q, app=%q, browser=%q", external, a.err, b.err)
			}
			if kind == "directory" {
				waitFor(t, tt, "directory target", func() bool { return b.dir == filepath.Dir(path) && !b.loading })
			} else {
				if b.preview != path || string(b.previewData) != content || !tt.HasText("automation-plan.md") {
					t.Fatalf("wrong file preview: %q, %q", b.preview, b.previewData)
				}
				if kind == "line and column" || kind == "file URL" {
					pos := b.previewCode.CursorPos()
					if pos.Line != 2 || pos.Col != 1 {
						t.Fatalf("wrong file location: %+v", pos)
					}
				}
			}
			a.openTerminalLink(p, terminal.Link{URL: "https://example.com/docs.md"})
			if external != "https://example.com/docs.md" {
				t.Fatal("web URL did not use browser")
			}
		})
	}
}

func TestTerminalFileLinkFailuresStayInViewer(t *testing.T) {
	for _, kind := range []string{"missing", "offline remote", "raw ssh"} {
		t.Run(kind, func(t *testing.T) {
			previous := prefs
			prefs = settings{FontSize: defaultFontSize}
			t.Cleanup(func() { prefs = previous })
			t.Setenv("RETTY_DIR", t.TempDir())
			a, tt := newStaticTestApp(t)
			p := a.tab().Focus
			p.terminal().info.Dir = t.TempDir()
			a.openLinkURL = func(string) error { t.Fatal("file fell back to external opener"); return nil }
			if kind == "offline remote" {
				p.host = &desktopHost{}
			}
			if kind == "raw ssh" {
				p.terminal().info.Program = "ssh"
			}
			t.Cleanup(func() {
				if a.focusedFiles() != nil {
					a.focusedFiles().stop()
				}
			})
			a.openTerminalLink(p, terminal.Link{Path: "missing.md"})
			waitFor(t, tt, fmt.Sprintf("%s error", kind), func() bool {
				return a.err != "" || a.focusedFiles() != nil && a.focusedFiles().err != ""
			})
			if kind == "raw ssh" && !strings.Contains(a.err, "远端连接") {
				t.Fatal(a.err)
			}
		})
	}
}

func TestTerminalFileLinkUsesRemoteHost(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	control, peer := net.Pipe()
	t.Cleanup(func() { peer.Close() })
	requests := make(chan files.Request, 16)
	client := rex.NewClient(control, func(context.Context) (net.Conn, error) {
		local, server := net.Pipe()
		go func() {
			defer server.Close()
			var req files.Request
			if err := json.NewDecoder(server).Decode(&req); err != nil {
				return
			}
			requests <- req
			entry := files.Entry{Name: "notes.md", Regular: true}
			r := files.Result{Path: req.Path, Entry: &entry}
			if req.Kind == "list" {
				r.Entries = []files.Entry{entry}
			}
			if req.Kind == "read" {
				r.Data = []byte("# 来自远端\n")
			}
			data, _ := json.Marshal(r)
			_ = json.NewEncoder(server).Encode(rex.Response{Data: data})
		}()
		return local, nil
	})
	t.Cleanup(func() { client.Close() })
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.host = &desktopHost{client: client}
	p.host.hello.Host.Home = "/remote/home"
	p.terminal().info.Dir = "/remote/work"
	a.openLinkURL = func(string) error { t.Fatal("remote file used local opener"); return nil }
	t.Cleanup(func() {
		if a.focusedFiles() != nil {
			a.focusedFiles().stop()
		}
	})
	a.openTerminalLink(p, terminal.Link{Path: "~/notes.md"})
	waitFor(t, tt, "remote file content", func() bool { return a.focusedFiles() != nil && a.focusedFiles().previewCode != nil })
	if a.focusedFiles().preview != "/remote/home/notes.md" || string(a.focusedFiles().previewData) != "# 来自远端\n" {
		t.Fatalf("remote file used local home/content: %q, %q", a.focusedFiles().preview, a.focusedFiles().previewData)
	}
	read := false
	for len(requests) > 0 {
		req := <-requests
		if req.Op != "files-browse" {
			t.Fatalf("unexpected daemon request: %+v", req)
		}
		if req.Kind == "read" {
			read = true
			if req.Path != "/remote/home/notes.md" {
				t.Fatal(req.Path)
			}
		}
	}
	if !read {
		t.Fatal("remote file was not read over the file extension")
	}
}
