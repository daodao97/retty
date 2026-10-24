package rex

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestViewStreamReadStartsAttachmentWithoutResize(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	c := &Client{dial: func(context.Context) (net.Conn, error) { return conn, nil }}
	viewer := c.ViewStream("read-only").PreferScreenSync(false)
	defer viewer.Close()
	attached := make(chan Attach, 1)
	serverDone := make(chan struct{})
	payload := []byte("中文🙂 first viewport")
	go func() {
		defer close(serverDone)
		defer peer.Close()
		var req Attach
		if json.NewDecoder(peer).Decode(&req) != nil {
			return
		}
		attached <- req
		if _, err := peer.Write([]byte("{\"ok\":true,\"screen_frames\":true}\n")); err != nil {
			return
		}
		frame := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(frame, uint32(len(payload)))
		binary.BigEndian.PutUint16(frame[4:], 120)
		binary.BigEndian.PutUint16(frame[6:], 38)
		copy(frame[8:], payload)
		peer.Write(frame)
	}()
	done := make(chan error, 1)
	go func() {
		data, cols, rows, err := viewer.ReadSnapshot()
		if err == nil && (cols != 120 || rows != 38 || !bytes.Equal(data, payload)) {
			err = errors.New("read-only attachment changed geometry or first viewport")
		}
		done <- err
	}()
	select {
	case req := <-attached:
		if req.SID != "read-only" || req.Cols != 0 || req.Rows != 0 || !req.ScreenSync || !req.ScreenFrames {
			t.Fatalf("read-triggered attach lost protocol preferences or resized PTY: %+v", req)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reader waited for the fallback timer despite having no layout to collect")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first viewport did not complete")
	}
	<-serverDone
}

func TestViewStreamCloseUnblocksReadWhileDialing(t *testing.T) {
	started, release, dialDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	dialStarted := false
	defer func() {
		close(release)
		if dialStarted {
			select {
			case <-dialDone:
			case <-time.After(time.Second):
				t.Error("owned dial worker did not exit")
			}
		}
	}()
	c := &Client{dial: func(context.Context) (net.Conn, error) {
		defer close(dialDone)
		close(started)
		<-release
		return nil, io.ErrClosedPipe
	}}
	viewer := c.ViewStream("read-only")
	defer viewer.Close()
	done := make(chan error, 1)
	go func() { _, _, _, err := viewer.ReadSnapshot(); done <- err }()
	select {
	case <-started:
		dialStarted = true
	case <-time.After(200 * time.Millisecond):
		t.Fatal("viewer did not start dialing on read")
	}
	viewer.Close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("closed reader: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reader waited for an in-flight dial after Close")
	}
	// The attach worker is allowed to finish its transport attempt separately.
}
