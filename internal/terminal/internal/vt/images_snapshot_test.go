package vt

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestKittySnapshotRestoresPixelsPlacementsCursorAndReplacement(t *testing.T) {
	loadLib(t)
	source, err := NewTerminal(40, 10, Effects{})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Free()
	source.SetImageLimit(8 << 20)
	source.Resize(40, 10, 8, 16)
	pixels := []byte{200, 20, 80, 255, 20, 160, 210, 255}
	source.Write([]byte(fmt.Sprintf("\x1b[2;4H\x1b_Ga=T,f=32,s=2,v=1,i=42,p=1,c=8,r=3,z=-2,C=1,q=2;%s\x1b\\\x1b[7;12H", base64.StdEncoding.EncodeToString(pixels))))
	clone, err := NewTerminal(40, 10, Effects{})
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Free()
	clone.SetImageLimit(8 << 20)
	clone.Resize(40, 10, 8, 16)
	snap := source.VTGraphicsRecent(-1)
	if !bytes.Equal(snap, source.VTGraphicsRecent(-1)) {
		t.Fatal("unchanged image snapshot is not stable")
	}
	clone.Write(snap)
	img, ok := clone.Image(42)
	if !ok || !bytes.Equal(img.Pixels, pixels) {
		t.Fatal("snapshot lost pixels", img, ok)
	}
	var placements []Placement
	clone.Placements(func(p Placement) { placements = append(placements, p) })
	if len(placements) != 1 || placements[0].Col != 3 || placements[0].Row != 1 || placements[0].Z != -2 || placements[0].ParamCols != 8 || placements[0].ParamRows != 3 {
		t.Fatal("snapshot lost placement", placements)
	}
	x, y := clone.Cursor()
	if x != 11 || y != 6 {
		t.Fatal("placing images moved final cursor", x, y)
	}
	source.Write([]byte("\x1b[3;9r\x1b[?6h\x1b[2;5H"))
	wantX, wantY := source.Cursor()
	clone.Reset()
	clone.Write(source.VTGraphicsRecent(-1))
	if x, y := clone.Cursor(); !clone.Mode(6) || x != wantX || y != wantY {
		t.Fatal("graphics restore changed origin mode/cursor", x, y, wantX, wantY)
	}
	// Replacing an image ID must invalidate both transfer and bitmap generations.
	pixels[0] = 17
	source.Write([]byte(fmt.Sprintf("\x1b_Ga=T,f=32,s=2,v=1,i=42,p=1,c=8,r=3,C=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString(pixels))))
	clone.Reset()
	clone.Write(source.VTGraphicsRecent(-1))
	img, ok = clone.Image(42)
	if !ok || !bytes.Equal(img.Pixels, pixels) {
		t.Fatal("same-ID replacement reused old pixels", img)
	}
	source.Write([]byte("\x1b_Ga=d,d=A,q=2;\x1b\\"))
	clone.Reset()
	clone.Write(source.VTGraphicsRecent(-1))
	if _, ok := clone.Image(42); ok {
		t.Fatal("deleted image survived snapshot")
	}
}

func TestKittySnapshotMarginsSkipOpaqueURIs(t *testing.T) {
	top, left := snapshotMargins([]byte("\x1b[3;9r\x1b[4;30s\x1b]8;;https://example.test/\x1b[99;100r\x1b\\label\x1b]8;;\x1b\\"))
	if top != 2 || left != 3 {
		t.Fatal("opaque hyperlink changed margins", top, left)
	}
}

func TestKittyFileAndTemporaryFileMediaAreRefused(t *testing.T) {
	loadLib(t)
	term, err := NewTerminal(20, 5, Effects{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Free()
	term.SetImageLimit(8 << 20)
	path := filepath.Join(t.TempDir(), "pixels")
	os.WriteFile(path, []byte{90, 120, 180, 255}, 0600)
	for _, medium := range []string{"f", "t"} {
		term.Write([]byte(fmt.Sprintf("\x1b_Ga=T,t=%s,f=32,s=1,v=1,i=81,q=2;%s\x1b\\", medium, base64.StdEncoding.EncodeToString([]byte(path)))))
		if _, ok := term.Image(81); ok {
			t.Fatal("image read viewer filesystem", medium)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("image removed viewer file", medium, err)
		}
	}
}

func TestKittySnapshotCropsScrolledPlacements(t *testing.T) {
	loadLib(t)
	source, _ := NewTerminal(20, 10, Effects{})
	defer source.Free()
	source.SetImageLimit(8 << 20)
	source.Resize(20, 10, 8, 16)
	pixels := bytes.Repeat([]byte{50, 100, 150, 255}, 8*8)
	source.Write([]byte(fmt.Sprintf("\x1b[H\x1b_Ga=T,f=32,s=8,v=8,i=3,c=4,r=4,C=1,q=2;%s\x1b\\\x1b[10;1H\r\n", base64.StdEncoding.EncodeToString(pixels))))
	var before Placement
	source.Placements(func(p Placement) { before = p })
	if before.Row >= 0 || !before.Visible {
		t.Fatal("fixture did not scroll partially out of view", before)
	}
	clone, _ := NewTerminal(20, 10, Effects{})
	defer clone.Free()
	clone.SetImageLimit(8 << 20)
	clone.Resize(20, 10, 8, 16)
	clone.Write(source.VTGraphicsRecent(-1))
	var after Placement
	clone.Placements(func(p Placement) { after = p })
	if !after.Visible || after.Row != 0 || after.ParamSrcY <= 0 || after.ParamRows != 3 {
		t.Fatal("scrolled image was misplaced", before, after)
	}
}
