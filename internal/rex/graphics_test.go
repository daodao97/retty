//go:build darwin || linux

package rex

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"retty/internal/terminal/screen"
)

func graphicsViewer(t *testing.T, ss *session, graphics bool) *Stream {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { ss.attachScreenSync(server, bufio.NewReader(server), 0, 0, false, graphics); close(done) }()
	stream := &Stream{conn: client, screenFrames: true, screenSync: true}
	stream.cond = sync.NewCond(&stream.mu)
	t.Cleanup(func() {
		stream.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("graphics viewer leaked")
		}
	})
	return stream
}

func TestKittyScreenFramesRetainImagesOnReconnectAndFinalExit(t *testing.T) {
	ss := stateFixture(t)
	pixels := base64.StdEncoding.EncodeToString([]byte{20, 110, 190, 255})
	ss.vt.Feed([]byte(fmt.Sprintf("\x1b[2;3H\x1b_Ga=T,f=32,s=1,v=1,i=9,p=1,c=4,r=3,C=1,q=2;%s\x1b\\", pixels)))
	beforeCols, beforeRows := ss.cols, ss.rows
	legacy := graphicsViewer(t, ss, false)
	data, _, _, err := legacy.ReadSnapshot()
	if err != nil || bytes.Contains(data, []byte("\x1b_G")) {
		t.Fatal("legacy viewer received new graphics state", err)
	}
	legacy.Close()
	for i := range 2 {
		viewer := graphicsViewer(t, ss, true)
		data, cols, rows, err := viewer.ReadSnapshot()
		if err != nil || !bytes.Contains(data, []byte("\x1b_Ga=t")) {
			t.Fatal("graphics lost on reconnect", i, err)
		}
		clone, err := screen.New(cols, rows, 1<<20, nil)
		if err != nil {
			t.Fatal(err)
		}
		clone.Feed(data)
		if !bytes.Contains(clone.SnapshotGraphicsRecent(-1), []byte("i=9")) {
			t.Fatal("viewer could not restore transferred graphics")
		}
		clone.Close()
		viewer.Close()
	}
	ss.mu.Lock()
	ss.finalGraphics = ss.graphicsSnapshotLocked(-1)
	ss.finalViewport = ss.vt.SnapshotRecent(-1)
	ss.exited = true
	ss.notifyScreensLocked()
	ss.mu.Unlock()
	last := graphicsViewer(t, ss, true)
	data, _, _, err = last.ReadSnapshot()
	if err != nil || !bytes.Contains(data, []byte("i=9")) {
		t.Fatal("exited image artifact was lost", err)
	}
	if ss.cols != beforeCols || ss.rows != beforeRows {
		t.Fatal("viewing graphics resized the source")
	}
}
