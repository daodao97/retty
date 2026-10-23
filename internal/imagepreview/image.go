// Package imagepreview decodes bounded previews using the owning host's reader.
package imagepreview

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	"retty/internal/files"
)

const MaxBytes = files.MaxRead
const MaxPixels = 16 << 20
const MaxSide = 8192

func Candidate(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

type Fetch func(context.Context, files.Request) (files.Result, error)

func Load(ctx context.Context, path string, fetch Fetch) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stat, err := fetch(ctx, files.Request{Kind: "stat", Path: path})
	if err != nil {
		return nil, err
	}
	if stat.Entry == nil || !stat.Entry.Regular {
		return nil, errors.New("仅预览普通图片文件")
	}
	if stat.Entry.Size <= 0 || stat.Entry.Size > MaxBytes {
		return nil, errors.New("图片超过 4 MiB 预览限制")
	}
	part, err := fetch(ctx, files.Request{Kind: "read", Path: path, Length: MaxBytes, Version: stat.Entry.Version})
	if err != nil {
		return nil, err
	}
	if part.Truncated || len(part.Data) > MaxBytes {
		return nil, errors.New("图片超过 4 MiB 预览限制")
	}
	if int64(len(part.Data)) != stat.Entry.Size {
		return nil, errors.New("图片已变化，请重新悬停")
	}
	return Decode(ctx, part.Data)
}

func Fits(w, h int) bool {
	return w > 0 && h > 0 && w <= MaxSide && h <= MaxSide && int64(w)*int64(h) <= MaxPixels
}

func Decode(ctx context.Context, data []byte) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, errors.New("图片超过 4 MiB 预览限制")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("无法预览此图片")
	}
	if !Fits(cfg.Width, cfg.Height) {
		return nil, errors.New("图片尺寸超过预览限制")
	}
	img, _, err := image.Decode(contextReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !Fits(img.Bounds().Dx(), img.Bounds().Dy()) {
		return nil, errors.New("图片尺寸超过预览限制")
	}
	return img, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
