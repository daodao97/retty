package screen

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"testing"
)

func TestKittyScreenSnapshotRestoresWithoutReplayingEffects(t *testing.T) {
	s, err := New(40, 10, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pixels := []byte{190, 20, 70, 255}
	s.Feed([]byte(fmt.Sprintf("\x1b[3;4H\x1b_Ga=T,f=32,s=1,v=1,i=71,c=4,r=2,C=1,q=2;%s\x1b\\", base64.StdEncoding.EncodeToString(pixels))))
	if bytes.Contains(s.SnapshotRecent(-1), []byte("\x1b_G")) {
		t.Fatal("text-only history unexpectedly transmits graphics")
	}
	clone, err := New(40, 10, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	clone.Feed(s.SnapshotGraphicsRecent(-1))
	img, ok := clone.term.Image(71)
	if !ok || !bytes.Equal(img.Pixels, pixels) {
		t.Fatal("headless snapshot lost pixels", img)
	}
	_, effects := clone.DrainEffects()
	if len(effects) != 0 {
		t.Fatal("restoring image emitted one-time notification effects", effects)
	}
}
