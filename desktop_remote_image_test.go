package main

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"retty/internal/rex"
	"testing"
	"time"
)

func remoteClipboardPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestRemoteCodexImageUploadsBeforePastingRemotePath(t *testing.T) {
	for _, action := range []string{"success", "switch agent", "reconnect", "cancel"} {
		t.Run(action, func(t *testing.T) {
			a, p, tt, peer, received, release, _ := remoteUploadPaneFixture(t, "")
			p.info = rex.SessionInfo{ID: p.SID, Program: "codex", Agent: rex.AgentState{ID: "codex", SessionID: "thread", State: "ready"}}
			image := remoteClipboardPNG(t)
			a.uploadPaneClipboardImage(p, image)
			select {
			case got := <-received:
				if !bytes.Equal(got, image) {
					t.Fatal("PNG data changed")
				}
			case <-time.After(time.Second):
				t.Fatal("upload did not start")
			}
			assertNoUploadInput(t, peer)
			switch action {
			case "switch agent":
				p.info.Program = "bash"
				p.info.Agent = rex.AgentState{}
			case "reconnect":
				p.host.generation++
			case "cancel":
				p.cancelFileUpload()
			}
			close(release)
			waitFor(t, tt, "image upload response", func() bool { return p.upload == nil })
			if action != "success" {
				assertNoUploadInput(t, peer)
				return
			}
			want := "\x1b[200~/srv/Retty/uploads/batch-123/001/clipboard.png \x1b[201~"
			peer.SetReadDeadline(time.Now().Add(time.Second))
			got := make([]byte, len(want))
			if _, err := io.ReadFull(peer, got); err != nil || string(got) != want {
				t.Fatal("expected only remote path, no Ctrl+V or Enter", string(got), err)
			}
			assertNoUploadInput(t, peer)
			if a.err != "" {
				t.Fatal(a.err)
			}
		})
	}
}
func TestRemoteCodexImageUnsupportedNeverPastesLocalPath(t *testing.T) {
	a, p, tt, peer, _, _, _ := remoteUploadPaneFixture(t, `unknown op "upload-files"`)
	p.info.Program = "codex"
	a.uploadPaneClipboardImage(p, remoteClipboardPNG(t))
	waitFor(t, tt, "unsupported image upload", func() bool { return p.upload == nil })
	if a.err == "" {
		t.Fatal("missing unsupported gateway error")
	}
	assertNoUploadInput(t, peer)
}

func TestMobileCodexImagePastesOnlyConfirmedRemotePath(t *testing.T) {
	for _, action := range []string{"success", "background", "reconnect", "new thread", "cancel"} {
		t.Run(action, func(t *testing.T) {
			a, p, tt, peer, received, release, _ := remoteUploadPaneFixture(t, "")
			m := &mobileApp{client: p.host.client, term: p.term, selected: rex.SessionInfo{ID: p.SID, Program: "codex", Agent: rex.AgentState{ID: "codex", SessionID: "thread", State: "ready"}}, imagePastePost: a.post}
			m.pasteClipboardImage(remoteClipboardPNG(t))
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("mobile image upload did not start")
			}
			assertNoUploadInput(t, peer)
			switch action {
			case "background":
				m.background = true
			case "reconnect":
				m.generation++
			case "new thread":
				m.selected.Agent.SessionID = "new"
			case "cancel":
				m.cancelImagePaste()
			}
			close(release)
			waitFor(t, tt, "mobile image completion", func() bool { return !m.imagePasteBusy })
			if action != "success" {
				assertNoUploadInput(t, peer)
				return
			}
			want := "\x1b[200~/srv/Retty/uploads/batch-123/001/clipboard.png \x1b[201~"
			peer.SetReadDeadline(time.Now().Add(time.Second))
			got := make([]byte, len(want))
			if _, err := io.ReadFull(peer, got); err != nil || string(got) != want {
				t.Fatal("mobile did not paste remote image path", string(got), err)
			}
			assertNoUploadInput(t, peer)
			if m.error != "" {
				t.Fatal(m.error)
			}
		})
	}
}
