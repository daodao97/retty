package imagepreview

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"retty/internal/files"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 12, 8))
	img.Set(2, 3, color.NRGBA{20, 100, 180, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPreviewLoadsPinnedBoundedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.png")
	data := testPNG(t)
	os.WriteFile(path, data, 0600)
	var requests []files.Request
	img, err := Load(context.Background(), path, func(ctx context.Context, req files.Request) (files.Result, error) {
		requests = append(requests, req)
		return files.Do(ctx, req)
	})
	if err != nil || img.Bounds() != image.Rect(0, 0, 12, 8) || len(requests) != 2 || requests[0].Kind != "stat" || requests[1].Version == "" || requests[1].Length != MaxBytes {
		t.Fatal(img, requests, err)
	}
	if _, err := Load(context.Background(), path, func(ctx context.Context, req files.Request) (files.Result, error) {
		if req.Kind == "stat" {
			return files.Result{Entry: &files.Entry{Regular: true, Size: MaxBytes + 1}}, nil
		}
		t.Fatal("oversized image was read")
		return files.Result{}, nil
	}); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestPreviewRejectsHeadersBeforeDecodingPixelsAndHonorsCancel(t *testing.T) {
	data := testPNG(t)
	binary.BigEndian.PutUint32(data[16:20], 20000)
	binary.BigEndian.PutUint32(data[20:24], 20000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if _, err := Decode(context.Background(), data); err == nil {
		t.Fatal("unbounded pixel claim decoded")
	}
	if _, err := Decode(context.Background(), bytes.Repeat([]byte{0}, MaxBytes+1)); err == nil {
		t.Fatal("unbounded bytes decoded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, "/not-read.png", func(context.Context, files.Request) (files.Result, error) {
		t.Fatal("canceled preview read a file")
		return files.Result{}, nil
	}); err != context.Canceled {
		t.Fatal(err)
	}
	for _, path := range []string{"a.PNG", "photo.jpeg", "a.webp", "a.bmp", "a.gif"} {
		if !Candidate(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{"image.svg", "file.txt", "image.png.exe"} {
		if Candidate(path) {
			t.Fatal(path)
		}
	}
}
