package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"retty/internal/terminal"
)

var fileURLLocation = regexp.MustCompile(`:([0-9]+)(?::([0-9]+))?$`)
var fileURLFragment = regexp.MustCompile(`^L([0-9]+)(?:C([0-9]+))?$`)

func editorForLinks(preferred string) string {
	if preferred != "" {
		return preferred
	}
	return "retty"
}

func localFileHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	name, _ := os.Hostname()
	return strings.EqualFold(host, name) || strings.EqualFold(host, strings.Split(name, ".")[0])
}

// File references belong to the pane's host and working directory. Resolving
// a remote reference must never check the same path on this computer.
func terminalFileReference(link terminal.Link, cwd, home string, remote bool) (terminal.Link, bool, error) {
	fromURL, fragment := link.URL != "", ""
	if fromURL {
		u, err := url.Parse(link.URL)
		if err != nil {
			return link, false, errors.New("链接格式无效")
		}
		if u.Scheme != "" && !strings.EqualFold(u.Scheme, "file") {
			return link, false, nil
		}
		if !localFileHost(u.Host) {
			return link, true, errors.New("文件链接指向其他主机")
		}
		link.Path, link.URL = u.Path, ""
		fragment = u.Fragment
	}
	path := link.Path
	if path == "" || strings.ContainsAny(path, "\x00\r\n") {
		return link, true, errors.New("文件路径无效")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home == "" || !filepath.IsAbs(home) {
			return link, true, errors.New("尚未获取此主机的主目录")
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		if cwd == "" || !filepath.IsAbs(cwd) {
			return link, true, errors.New("尚未获取此窗格的工作目录")
		}
		path = filepath.Join(cwd, path)
	}
	link.Path = filepath.Clean(path)
	if fromURL {
		if loc := fileURLLocation.FindStringSubmatchIndex(link.Path); loc != nil {
			literal := false
			if !remote {
				_, statErr := os.Stat(link.Path)
				literal = statErr == nil
			}
			if !literal {
				link.Line, _ = strconv.Atoi(link.Path[loc[2]:loc[3]])
				if loc[4] >= 0 {
					link.Column, _ = strconv.Atoi(link.Path[loc[4]:loc[5]])
				}
				link.Path = link.Path[:loc[0]]
			}
		}
		if loc := fileURLFragment.FindStringSubmatch(fragment); loc != nil {
			link.Line, _ = strconv.Atoi(loc[1])
			link.Column, _ = strconv.Atoi(loc[2])
		}
	}
	return link, true, nil
}

// resolveTerminalLink returns an escaped URL, never a shell command. Relative
// paths are anchored to the pane, not Retty's own launch directory.
func resolveTerminalLink(link terminal.Link, cwd, home, editor string, remote bool) (string, error) {
	if link.URL != "" {
		u, err := url.Parse(link.URL)
		if err != nil {
			return "", errors.New("链接格式无效")
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "ftp":
			if u.Host == "" {
				return "", errors.New("链接缺少主机名")
			}
			return u.String(), nil
		case "mailto":
			return u.String(), nil
		case "vscode", "vscode-insiders", "cursor":
			if u.Host != "file" {
				return "", errors.New("编辑器文件链接格式无效")
			}
			return u.String(), nil
		case "", "file":
		default:
			return "", errors.New("不支持此链接类型")
		}
	}
	if remote {
		return "", errors.New("远程会话的文件路径无法直接在本机打开")
	}
	link, _, err := terminalFileReference(link, cwd, home, false)
	if err != nil {
		return "", err
	}
	path := link.Path
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("无法打开文件：%s", path)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("此路径不是普通文件或目录")
	}
	if info.IsDir() || editor == "system" {
		return (&url.URL{Scheme: "file", Path: path}).String(), nil
	}
	if editor != "vscode" && editor != "cursor" {
		return "", errors.New("文件编辑器设置无效")
	}
	if link.Line > 0 {
		path += ":" + strconv.Itoa(link.Line)
		if link.Column > 0 {
			path += ":" + strconv.Itoa(link.Column)
		}
	}
	return (&url.URL{Scheme: editor, Host: "file", Path: path}).String(), nil
}

func (a *App) openTerminalLink(p *Pane, link terminal.Link) {
	cwd := p.terminal().info.Dir
	if cwd == "" {
		cwd = p.terminal().startDir
	}
	home, _ := os.UserHomeDir()
	remote := p.host != nil || p.terminal().info.Program == "ssh" || p.terminal().info.Program == "mosh"
	if p.host != nil {
		home = p.host.hello.Host.Home
	}
	if editorForLinks(prefs.LinkEditor) == "retty" {
		file, isFile, err := terminalFileReference(link, cwd, home, remote)
		if err != nil {
			a.err = err.Error()
			return
		}
		if isFile {
			if remote && p.host == nil {
				a.err = "请通过 Retty 远端连接查看此主机的文件"
				return
			}
			// Opening the workspace replaces the terminal being built this frame.
			a.post(func() { a.openTerminalFile(p, file) })
			if a.win != nil {
				a.win.Invalidate()
			}
			return
		}
	}
	target, err := resolveTerminalLink(link, cwd, home, editorForLinks(prefs.LinkEditor), remote)
	if err == nil {
		open := a.openLinkURL
		if open == nil {
			open = mygo.Shell.OpenExternal
		}
		err = open(target)
	}
	if err != nil {
		a.err = err.Error()
	}
}
