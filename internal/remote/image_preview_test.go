package remote

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"retty/internal/files"
	"retty/internal/imagepreview"
)

func TestImagePreviewUsesIndependentHostFileStream(t *testing.T) {
	c, _, socket, _ := fileUploadFixture(t)
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 16, 9))
	img.Set(1, 1, color.NRGBA{23, 150, 201, 255})
	var data bytes.Buffer
	png.Encode(&data, img)
	path := filepath.Join(t.TempDir(), "remote-image.png")
	os.WriteFile(path, data.Bytes(), 0600)
	got, err := imagepreview.Load(context.Background(), path, func(ctx context.Context, req files.Request) (files.Result, error) { return BrowseFiles(ctx, c, req) })
	if err != nil || got.Bounds() != img.Bounds() {
		t.Fatal("preview contacted unavailable PTY daemon", got, err)
	}
	if got.At(1, 1) != img.At(1, 1) {
		t.Fatal("preview did not decode host pixels")
	}
}
