package terminal

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKittyImagesRenderAndReplacementDoesNotReuseBitmap(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Fill() }, 500, 240)
	transfer := func(red byte) {
		pixels := bytes.Repeat([]byte{red, 90, 180, 255}, 40*20)
		term.Feed([]byte(fmt.Sprintf("\x1b[2;3H\x1b_Ga=T,f=32,s=40,v=20,i=11,p=1,c=20,r=7,C=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString(pixels))))
		tt.Frame()
	}
	transfer(200)
	if len(term.v.images) != 1 || term.v.images[0].bmp == nil {
		t.Fatal("Kitty image not read by renderer", term.v.images)
	}
	first := term.v.images[0].bmp
	transfer(30)
	if term.v.images[0].bmp == first {
		t.Fatal("same-ID replacement reused bitmap")
	}
	if dir := os.Getenv("MYGO_TEST_IMAGES"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "kitty-terminal.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err = png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	term.Feed([]byte("\x1b_Ga=d,d=A,q=2;\x1b\\"))
	tt.Frame()
	if len(term.v.images) != 0 || len(term.v.bitmaps) != 0 {
		t.Fatal("deleted image retained bitmap")
	}
}
