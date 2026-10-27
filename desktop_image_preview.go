package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"retty/internal/files"
	"retty/internal/imagepreview"
	"retty/internal/rex"
	"retty/internal/terminal"
)

func (a *App) terminalImagePreview(p *Pane, link terminal.Link) *terminal.LinkPreviewSource {
	if p.closed || a.quitting {
		return nil
	}
	// Plain ssh/mosh has no authenticated file reader. Never read a same-named
	// local file for a path printed by an unknown remote host.
	if p.host == nil && (p.terminal().info.Program == "ssh" || p.terminal().info.Program == "mosh") {
		return nil
	}
	cwd := p.terminal().info.Dir
	if cwd == "" {
		cwd = p.terminal().startDir
	}
	home, _ := os.UserHomeDir()
	var client *rex.Client
	if p.host != nil {
		if !p.host.connected() {
			return nil
		}
		home, client = p.host.hello.Host.Home, p.host.client
	}
	file, isFile, err := terminalFileReference(link, cwd, home, true)
	if err != nil || !isFile || !imagepreview.Candidate(file.Path) {
		return nil
	}
	path := file.Path
	return &terminal.LinkPreviewSource{Key: fmt.Sprintf("%p:%p:%s", p.host, client, path), Label: filepath.Base(path), Load: func(ctx context.Context) (image.Image, error) {
		return imagepreview.Load(ctx, path, func(ctx context.Context, req files.Request) (files.Result, error) {
			return fileRequest(ctx, client, req)
		})
	}}
}

func (a *App) cancelHiddenImagePreviews(narrow bool) {
	t := a.tab()
	for _, p := range a.allPanes() {
		if p.terminalView() == nil {
			continue
		}
		if t == nil || p.Tab != t || t.Zoom != nil && t.Zoom != p || narrow && t.Focus.files() != nil && t.Focus != p {
			p.terminal().term.CancelLinkPreview()
		}
	}
}
