package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"retty/internal/automation"
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
	// A loop that sent the next instruction must not see the previous result.
	status, err := run("status", cat.ID, "--json")
	var finished rex.SessionInfo
	if err != nil || json.Unmarshal([]byte(status), &finished) != nil || finished.Agent.CompletionRevision == 0 {
		t.Fatal("completion revision missing", status, err)
	}
	after := strconv.FormatUint(finished.Agent.CompletionRevision, 10)
	if _, err := run("wait", cat.ID, "--until", "done", "--after", after, "--timeout", "50ms"); controlcli.ExitCode(err) != 124 {
		t.Fatal("wait --after accepted the result it was told to skip", err)
	}
	if _, err := run("wait", cat.ID, "--after", after, "--timeout", "50ms"); err == nil {
		t.Fatal("--after accepted without --until done or blocked")
	}
	for _, state := range []string{"working", "done"} {
		if _, err := run("report", cat.ID, "--state", state, "--id", "build/tests"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run("wait", cat.ID, "--until", "done", "--after", after, "--timeout", "2s"); err != nil {
		t.Fatal("wait --after missed the next completion", err)
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

func TestControlCLIGoalSendsCheckFailuresUntilPass(t *testing.T) {
	client, socket := desktopFixtureClient(t)
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	before, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		all, _ := client.List()
		for _, s := range all {
			if !slices.ContainsFunc(before, func(b rex.SessionInfo) bool { return b.ID == s.ID }) {
				client.Kill(s.ID)
			}
		}
	}()
	// A stand-in agent reports each turn over OSC 7501, as any program can,
	// and records every line it receives.
	agent := `st() { printf '\033]7501;state=%s\033\\' "$1"; }; st working; st done; while IFS= read -r line; do st working; printf '%s\n' "$line" >> turns; st done; done`
	dir := t.TempDir()
	run := func(check, rounds string) (string, error) {
		var out bytes.Buffer
		err := controlcli.Run(context.Background(), []string{"goal", "--socket", socket, "--dir", dir, "--goal", "write turns", "--check", check, "--rounds", rounds, "--turn-timeout", "10s", "--", "/bin/sh", "-c", agent, "agent"}, strings.NewReader(""), &out, io.Discard, controlcli.Options{})
		return out.String(), err
	}
	out, err := run("test -s turns", "3")
	if err != nil || !strings.Contains(out, "目标达成 · 2 轮") {
		t.Fatal(out, err)
	}
	turns, err := os.ReadFile(filepath.Join(dir, "turns"))
	if err != nil || !strings.Contains(string(turns), "test -s turns") {
		t.Fatal("follow-up lost the failed check", string(turns), err)
	}
	out, err = run("false", "1")
	if controlcli.ExitCode(err) != 1 || !strings.Contains(out, "1 轮后检查仍失败") {
		t.Fatal("goal did not stop at its round limit", out, err)
	}
}

func TestControlCLIGoalIgnoresStartupDialogInput(t *testing.T) {
	client, socket := desktopFixtureClient(t)
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	before, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() []rex.SessionInfo {
		all, _ := client.List()
		return slices.DeleteFunc(all, func(s rex.SessionInfo) bool {
			return slices.ContainsFunc(before, func(b rex.SessionInfo) bool { return b.ID == s.ID })
		})
	}
	defer func() {
		for _, s := range fresh() {
			client.Kill(s.ID)
		}
	}()
	// Like Codex's folder trust prompt, this agent waits for an answer before
	// its first lifecycle report, which comes some polls later.
	agent := `st() { printf '\033]7501;state=%s\033\\' "$1"; }; IFS= read -r answer; sleep 1.5; st working; st done; while IFS= read -r line; do st working; printf '%s\n' "$line" >> turns; st done; done`
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- controlcli.Run(ctx, []string{"goal", "--socket", socket, "--dir", dir, "--goal", "write turns", "--check", "test -s turns", "--rounds", "3", "--", "/bin/sh", "-c", agent, "agent"}, strings.NewReader(""), &out, io.Discard, controlcli.Options{})
	}()
	var sid string
	for deadline := time.Now().Add(5 * time.Second); sid == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if s := fresh(); len(s) == 1 {
			sid = s[0].ID
		}
	}
	if sid == "" {
		t.Fatal("goal did not start its agent")
	}
	// Answer after the 2-second grace period that follows the runner's send.
	time.Sleep(2500 * time.Millisecond)
	if err := client.SendInput(sid, rex.InputRequest{Data: []byte("\r")}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !strings.Contains(out.String(), "目标达成") || strings.Contains(out.String(), "已暂停") {
		t.Fatal("answering a startup dialog paused the goal", out.String(), err)
	}
}

func TestControlCLIAutomationStartsDueRuns(t *testing.T) {
	client, socket := desktopFixtureClient(t)
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	before, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() []rex.SessionInfo {
		all, _ := client.List()
		return slices.DeleteFunc(all, func(s rex.SessionInfo) bool {
			return slices.ContainsFunc(before, func(b rex.SessionInfo) bool { return b.ID == s.ID })
		})
	}
	defer func() {
		for _, s := range fresh() {
			client.Kill(s.ID)
		}
	}()
	// A stand-in runner records its arguments, one run per line, and exits.
	tmp := t.TempDir()
	// Commands default to RETTY_DIR; a mistaken argument must never reach
	// the developer's own data directory and its timer.
	t.Setenv("RETTY_DIR", filepath.Join(tmp, "default"))
	data, project, runs := filepath.Join(tmp, "data"), filepath.Join(tmp, "project"), filepath.Join(tmp, "runs")
	os.Mkdir(project, 0o700)
	runner := filepath.Join(tmp, "runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\necho \"$*\" >> "+runs+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	synced := 0
	options := controlcli.Options{Executable: runner, SyncAutomations: func(string) error { synced++; return nil }}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		// Options precede `-- AGENT`, which add keeps byte for byte.
		args = append([]string{"automation", args[0], "--data-dir", data}, args[1:]...)
		err := controlcli.Run(context.Background(), args, strings.NewReader(""), &out, io.Discard, options)
		return strings.TrimSpace(out.String()), err
	}
	lines := func(n int) []string {
		var got []string
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			content, _ := os.ReadFile(runs)
			if got = strings.Split(strings.TrimSpace(string(content)), "\n"); len(content) > 0 && len(got) >= n {
				break
			}
		}
		return got
	}
	id, err := run("add", "--goal", "keep tests green", "--every", "1h", "--dir", project, "--check", "go test ./...", "--", "codex", "--no-daemon")
	if err != nil || synced != 1 {
		t.Fatal(id, err, synced)
	}
	// A new automation runs at once, then waits for its interval.
	if _, err := run("tick", "--socket", socket); err != nil {
		t.Fatal(err)
	}
	got := lines(1)
	if len(got) != 1 || !strings.Contains(got[0], "goal --goal keep tests green --rounds 8 --state-file ") || !strings.HasSuffix(got[0], "--check go test ./... -- codex --no-daemon") {
		t.Fatal("runner arguments", got)
	}
	first := fresh()
	if len(first) != 1 {
		t.Fatal("tick did not start one runner session", first)
	}
	if _, err := run("tick", "--socket", socket); err != nil || len(fresh()) != 1 {
		t.Fatal("an automation ran before its interval", err, fresh())
	}
	// run starts one at once, replacing the finished runner's session.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if s := fresh(); len(s) == 1 && s[0].Exited {
			break
		}
	}
	if out, err := run("run", id[:6], "--socket", socket); err != nil || out != "ok" {
		t.Fatal(out, err)
	}
	if len(lines(2)) != 2 {
		t.Fatal("run did not start a run")
	}
	// The stand-in runner ended without recording its result, as a closed
	// one does; the history keeps that run as interrupted.
	if h := automation.History(data, id, 10); len(h) != 1 || h[0].State != "interrupted" {
		t.Fatal("interrupted run not kept", h)
	}
	second := fresh()
	if len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatal("finished runner session was not replaced", first, second)
	}
	if out, err := run("list"); err != nil || !strings.Contains(out, id) || !strings.Contains(out, "每 1h0m0s") || !strings.Contains(out, "running") {
		t.Fatal("list", out, err)
	}
	if _, err := run("pause", id); err != nil {
		t.Fatal(err)
	}
	if out, _ := run("list"); !strings.Contains(out, "已暂停") {
		t.Fatal("pause not shown", out)
	}
	if _, err := run("remove", id); err != nil || synced != 3 {
		t.Fatal("remove", err, synced)
	}
	if out, _ := run("list"); out != "" {
		t.Fatal("removed automation listed", out)
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
		if _, err := call(desktopcontrol.Request{Op: "action", Action: id, SID: parent.terminal().SID}); err == nil {
			t.Fatal("destructive action remotely allowed", id)
		}
	}
	if _, err := call(desktopcontrol.Request{Op: "action", Action: "pane.zoom"}); err == nil {
		t.Fatal("action guessed focused pane")
	}
	res, err = call(desktopcontrol.Request{Op: "split", SID: parent.terminal().SID, Direction: "down", Create: &rex.CreateOptions{Dir: filepath.Dir(rex.SocketPath()), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24}})
	if err != nil || res.SID == "" || res.SID == parent.terminal().SID {
		t.Fatal(res, err)
	}
	if len(parent.Tab.panes()) != 2 || !parent.Tab.Root.Vertical || parent.Tab.Focus.terminal().SID != res.SID || parent.Tab.Root.A.Pane != parent {
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
	if _, err := call(desktopcontrol.Request{Op: "action", Action: "pane.zoom", SID: parent.terminal().SID}); err != nil || parent.Tab.Zoom != parent {
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
	res, err := a.handleDesktopControl(context.Background(), desktopcontrol.Request{Op: "split", SID: parent.terminal().SID, Direction: "right"}, func(fn func()) error {
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
		if seen[p.terminal().SID] {
			t.Fatal("duplicate session attached", p.terminal().SID)
		}
		seen[p.terminal().SID] = true
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
