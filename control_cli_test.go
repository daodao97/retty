package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"retty/internal/controlcli"
	"retty/internal/desktopcontrol"
	"retty/internal/rex"
)

func TestControlCLIIsolatedSessionLifecycle(t *testing.T) {
	client, socket := desktopFixtureClient(t)
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := controlcli.Run(context.Background(), append(args, "--socket", socket), strings.NewReader(""), &out, io.Discard, controlcli.Options{})
		return out.String(), err
	}
	// '--' argv is last; options must be inserted before it.
	var out bytes.Buffer
	err := controlcli.Run(context.Background(), []string{"new", "--socket", socket, "--json", "--dir", t.TempDir(), "--cols", "93", "--rows", "31", "--", "/bin/cat"}, strings.NewReader(""), &out, io.Discard, controlcli.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var cat rex.SessionInfo
	if err = json.Unmarshal(out.Bytes(), &cat); err != nil {
		t.Fatal(out.String(), err)
	}
	defer client.Kill(cat.ID)
	if _, err := run("send", cat.ID, "--text", "literal $(not-a-command)", "--enter"); err != nil {
		t.Fatal(err)
	}
	var read struct {
		Text   string
		Output uint64
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := run("read", cat.ID, "--json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(data), &read); err != nil {
			t.Fatal(data, err)
		}
		if strings.Contains(read.Text, "literal $(not-a-command)") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(read.Text, "literal $(not-a-command)") || strings.Contains(read.Text, "\x1b") {
		t.Fatal("read lost or interpreted input", read)
	}
	all, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	current, err := controlcli.Resolve(all, cat.ID)
	if err != nil || current.Attached != 0 || current.Cols != 93 || current.Rows != 31 || current.PID != cat.PID {
		t.Fatal("CLI read/send attached or resized", current, err)
	}
	if _, err := run("send", cat.ID, "--text", "second", "--if-output", strconv.FormatUint(read.Output, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := run("send", cat.ID, "--text", "stale", "--if-output", "0"); err == nil {
		t.Fatal("CLI sent after stale read")
	}
	if _, err := run("report", cat.ID, "--state", "blocked", "--id", "build/tests", "--kind", "question", "--message", "Choose target"); err != nil {
		t.Fatal(err)
	}
	if data, err := run("wait", cat.ID, "--until", "blocked", "--timeout", "2s", "--json"); err != nil || !strings.Contains(data, "Choose target") {
		t.Fatal(data, err)
	}
	if _, err := run("report", cat.ID, "--state", "done", "--id", "build/tests"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("wait", cat.ID, "--until", "done", "--timeout", "2s"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("wait", cat.ID, "--timeout", "20ms"); controlcli.ExitCode(err) != 124 {
		t.Fatal("timeout lost exit code", err)
	}
	job, err := client.Create(rex.CreateOptions{Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "printf '\033[31mfinal-output\033[0m\\n'; exit 7"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Kill(job.ID)
	data, err := run("wait", job.ID, "--exit", "--timeout", "2s", "--json")
	if controlcli.ExitCode(err) != 7 || !strings.Contains(data, "\"exitCode\":7") {
		t.Fatal("real command exit status lost", data, err)
	}
	data, err = run("read", job.ID)
	if err != nil || !strings.Contains(data, "final-output") || strings.Contains(data, "\x1b") {
		t.Fatal("exited session cannot be read", data, err)
	}
}

func TestDesktopControlSharesActionsAndPlacesSplit(t *testing.T) {
	a, tt := newTestApp(t)
	parent := a.tab().Focus
	a.startDesktopControl()
	if a.control == nil {
		t.Fatal("control endpoint did not start")
	}
	t.Cleanup(func() { a.control.Close() })
	call := func(req desktopcontrol.Request) (desktopcontrol.Result, error) {
		t.Helper()
		type reply struct {
			res desktopcontrol.Result
			err error
		}
		done := make(chan reply, 1)
		go func() { res, err := desktopcontrol.Call(context.Background(), rex.Dir(), req); done <- reply{res, err} }()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case r := <-done:
				return r.res, r.err
			default:
				tt.Frame()
				time.Sleep(5 * time.Millisecond)
			}
		}
		t.Fatal("desktop control timed out")
		return desktopcontrol.Result{}, context.DeadlineExceeded
	}
	res, err := call(desktopcontrol.Request{Op: "actions"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, action := range res.Actions {
		if action.ID == "" || seen[action.ID] || actionByID(action.ID) == nil {
			t.Fatal("unstable/duplicate action", action)
		}
		seen[action.ID] = true
	}
	for _, id := range []string{"pane.close", "session.restart", "session.end-all", "host.update", "terminal.clear"} {
		if _, err := call(desktopcontrol.Request{Op: "action", Action: id, SID: parent.SID}); err == nil {
			t.Fatal("destructive action remotely allowed", id)
		}
	}
	if _, err := call(desktopcontrol.Request{Op: "action", Action: "pane.zoom"}); err == nil {
		t.Fatal("action guessed focused pane")
	}
	res, err = call(desktopcontrol.Request{Op: "split", SID: parent.SID, Direction: "down", Create: &rex.CreateOptions{Dir: filepath.Dir(rex.SocketPath()), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24}})
	if err != nil || res.SID == "" || res.SID == parent.SID {
		t.Fatal(res, err)
	}
	if len(parent.Tab.panes()) != 2 || !parent.Tab.Root.Vertical || parent.Tab.Focus.SID != res.SID || parent.Tab.Root.A.Pane != parent {
		t.Fatal("split did not preserve target", parent.Tab)
	}
	// Invoke the actual CLI through the socket, not just the GUI handler.
	done := make(chan error, 1)
	go func() {
		done <- controlcli.Run(context.Background(), []string{"split", res.SID, "--data-dir", rex.Dir(), "--direction", "right"}, strings.NewReader(""), io.Discard, io.Discard, controlcli.Options{})
	}()
	finished := false
	deadline := time.Now().Add(5 * time.Second)
	for !finished && time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			finished = true
		default:
			tt.Frame()
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !finished || len(parent.Tab.panes()) != 3 {
		t.Fatal("CLI split did not create third pane")
	}
	saveSettingsImage(t, tt, "cli-created-splits")
	if _, err := call(desktopcontrol.Request{Op: "action", Action: "pane.zoom", SID: parent.SID}); err != nil || parent.Tab.Zoom != parent {
		t.Fatal("action did not run registered command", err)
	}
	// The menu/palette identity no longer depends on translated labels.
	original := cmdZoom.Title
	cmdZoom.Title = "Renamed label"
	defer func() { cmdZoom.Title = original }()
	if actionByID("pane.zoom") != &cmdZoom {
		t.Fatal("action identity followed title")
	}
}

func TestDesktopControlSplitReusesPollDiscoveredSession(t *testing.T) {
	a, _ := newTestApp(t)
	parent := a.tab().Focus
	uiCalls := 0
	res, err := a.handleDesktopControl(context.Background(), desktopcontrol.Request{Op: "split", SID: parent.SID, Direction: "right"}, func(fn func()) error {
		uiCalls++
		if uiCalls == 2 {
			all, err := a.client.List()
			if err != nil {
				return err
			}
			byID := map[string]rex.SessionInfo{}
			for _, in := range all {
				byID[in.ID] = in
			}
			a.syncSessionTabs(byID)
		}
		fn()
		return nil
	})
	if err != nil || res.SID == "" || len(a.tabs) != 1 || len(parent.Tab.panes()) != 2 {
		t.Fatal("polling duplicated CLI split", res, err, len(a.tabs))
	}
	seen := map[string]bool{}
	for _, p := range parent.Tab.panes() {
		if seen[p.SID] {
			t.Fatal("duplicate session attached", p.SID)
		}
		seen[p.SID] = true
	}
}

func TestDesktopControlExpiredQueueCannotMutateUI(t *testing.T) {
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.startDesktopControl()
	if a.control == nil {
		t.Fatal("missing endpoint")
	}
	defer a.control.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := desktopcontrol.Call(ctx, rex.Dir(), desktopcontrol.Request{Op: "action", Action: "palette.open"})
	if err == nil {
		t.Fatal("unprocessed action unexpectedly succeeded")
	}
	// A frame now processes the queued callback after its caller's deadline.
	tt.Frame()
	if a.paletteOpen {
		t.Fatal("expired action opened palette after caller timed out")
	}
}
