package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"retty/internal/browsercontrol"
	"retty/internal/desktopcontrol"
	"retty/internal/diagnostics"
	"retty/internal/push"
	"retty/internal/remote"
	"retty/internal/rex"
	"tailscale.com/derp/derpserver"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"
)

// This opens a real production native UI with private GUI/service/push state.
// All process handles are created here; normal Retty processes are untouched.
func TestBrowserNativeAutomationAndRestore(t *testing.T) {
	if os.Getenv("RETTY_BROWSER_E2E") != "1" || runtime.GOOS != "darwin" {
		t.Skip("set RETTY_BROWSER_E2E=1 on macOS for an isolated real WebView")
	}
	binary := os.Getenv("RETTY_BROWSER_BINARY")
	if binary == "" {
		t.Fatal("RETTY_BROWSER_BINARY must point to an isolated production build")
	}
	binary, _ = filepath.Abs(binary)
	dir, err := os.MkdirTemp("/tmp", "retty-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	token := strings.Repeat("browser-owned-fixture-", 3)
	marker, _ := json.Marshal(map[string]string{"kind": "retty-performance-fixture", "token": token})
	os.WriteFile(filepath.Join(dir, "perf-fixture.json"), marker, 0600)
	perf := filepath.Join(dir, "fixture.sock")
	logPath := filepath.Join(dir, "native.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	baseEnv := []string{}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "RETTY_") || strings.HasPrefix(key, "MYGO_") {
			continue
		}
		baseEnv = append(baseEnv, kv)
	}
	profile := filepath.Join(dir, "codex-profile")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/missing/owned-fixture-hook"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	baseEnv = append(baseEnv, "RETTY_DIR="+dir, "SHELL=/bin/sh", "CODEX_HOME="+profile, "CLAUDE_CONFIG_DIR="+profile)
	type process struct {
		cmd  *exec.Cmd
		done chan error
	}
	start := func(args []string, gui bool, namespace ...string) *process {
		t.Helper()
		executable := binary
		if len(args) > 0 && args[0] == "-server" && os.Getenv("RETTY_BROWSER_SERVER_BINARY") != "" {
			executable = os.Getenv("RETTY_BROWSER_SERVER_BINARY")
		}
		cmd := exec.Command(executable, args...)
		cmd.Env = append([]string{}, baseEnv...)
		if len(namespace) > 0 {
			for i, kv := range cmd.Env {
				if strings.HasPrefix(kv, "RETTY_DIR=") {
					cmd.Env[i] = "RETTY_DIR=" + namespace[0]
				}
			}
			cmd.Env = append(cmd.Env, "PATH="+filepath.Join(namespace[0], "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		if gui {
			cmd.Env = append(cmd.Env, "RETTY_PERF_SOCKET="+perf)
		}
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		p := &process{cmd: cmd, done: make(chan error, 1)}
		go func() { p.done <- cmd.Wait() }()
		t.Cleanup(func() {
			select {
			case <-p.done:
				return
			default:
			}
			p.cmd.Process.Kill()
			<-p.done
		})
		return p
	}
	server := start([]string{"-server"}, false)
	_ = server
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var client *rex.Client
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		client, err = rex.ConnectDial(ctx, func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", rex.SocketPathForDir(dir))
		})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Shutdown(); client.Close() })
	hello, err := client.Hello()
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			conn.Write(r.Context(), websocket.MessageText, []byte("remote-hmr-ready"))
			conn.Read(r.Context())
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/remote" {
			io.WriteString(w, `<!doctype html><title>Remote dev fixture</title><h1>Remote dev page</h1><p id="remote-wire">connecting</p><script>const ws=new WebSocket('ws://'+location.host+'/ws');ws.onmessage=e=>document.querySelector('#remote-wire').textContent=e.data;</script>`)
			return
		}
		if r.URL.Path == "/next" {
			io.WriteString(w, `<!doctype html><title>Next fixture</title><h1 id="next">Restored browser page</h1>`)
			return
		}
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Retty Browser Fixture</title><style>body{font:20px system-ui;background:#eff5ff;padding:36px}input,button{font:inherit;padding:8px}#result{margin-top:24px;color:#1354a2}</style><h1>Retty browser fixture</h1><input id="name" placeholder="Name"><button id="save">Save</button><div id="result">Ready</div><a id="next-link" href="/next">Next</a><script>document.querySelector('#save').onclick=()=>document.querySelector('#result').textContent='Saved: '+document.querySelector('#name').value;</script>`)
	}))
	defer fixture.Close()
	session, err := client.Create(rex.CreateOptions{Dir: dir, Command: []string{"/bin/sh", "-c", fmt.Sprintf("printf 'owned browser fixture\\nLocal: %%s\\n' '%s'; exec sleep 120", fixture.URL)}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if session.HookExecutable == "" {
		t.Fatal("service did not publish the hook path injected into its fixture session")
	}
	// Own the push child too, so Ensure will reuse it instead of registering a job.
	start([]string{"-push-service", dir, rex.SocketPathForDir(dir)}, false)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = push.Query(dir); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	app := start(nil, true)
	cli := func(args ...string) []byte {
		t.Helper()
		args = append(args, "--data-dir", dir, "--json")
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = baseEnv
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		data, err := cmd.Output()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, diagnostics.String())
		}
		return data
	}
	call := func(req browsercontrol.Request) browsercontrol.Result {
		t.Helper()
		res, err := desktopcontrol.Call(ctx, dir, desktopcontrol.Request{Op: "browser", Browser: &req})
		if err != nil {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("browser %s: %v\n%s", req.Command, err, data)
		}
		if res.Browser == nil {
			t.Fatal("no browser response")
		}
		return *res.Browser
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err = desktopcontrol.Call(ctx, dir, desktopcontrol.Request{Op: "actions"})
		if err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil {
		data, _ := os.ReadFile(logPath)
		t.Fatalf("GUI unavailable: %v\n%s", err, data)
	}
	fixtureCall := func(op, value string) map[string]json.RawMessage {
		t.Helper()
		conn, err := net.DialTimeout("unix", perf, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := json.NewEncoder(conn).Encode(map[string]string{"token": token, "op": op, "value": value}); err != nil {
			t.Fatal(err)
		}
		var res map[string]json.RawMessage
		if err := json.NewDecoder(conn).Decode(&res); err != nil {
			t.Fatal(err)
		}
		if len(res["error"]) != 0 {
			t.Fatal("fixture operation", op, string(res["error"]))
		}
		return res
	}
	fixtureCall("diagnostics", "open")
	deadline = time.Now().Add(7 * time.Second)
	diagnosticReady := false
	for time.Now().Before(deadline) {
		raw := fixtureCall("diagnostics", "")["diagnostics"]
		var report diagnostics.Report
		if json.Unmarshal(raw, &report) == nil && len(report.Hooks) == 1 && report.Hooks[0].Executable.Code == "missing" && report.Build.Version != "" {
			diagnosticReady = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !diagnosticReady {
		t.Fatal("native GUI diagnostics did not read its isolated hook profile/build")
	}
	fixtureCall("diagnostics", "close")
	var opened browsercontrol.Result
	if err := json.Unmarshal(cli("browser", "open", fixture.URL, "--beside", session.ID), &opened); err != nil || opened.Browser == nil {
		t.Fatal("manual browser open failed", opened, err)
	}
	id := opened.Browser.ID
	var inspected browsercontrol.Result
	if err := json.Unmarshal(cli("browser", "status", id), &inspected); err != nil || inspected.Browser == nil || strings.TrimSuffix(inspected.Browser.URL, "/") != fixture.URL {
		t.Fatal(inspected, err)
	}
	call(browsercontrol.Request{Command: "wait", ID: id, Selector: "#name", TimeoutMS: 10000})
	text := "中文 'quoted' ${literal} `text`"
	cli("browser", "type", id, "--selector", "#name", "--text", text)
	call(browsercontrol.Request{Command: "click", ID: id, Selector: "#save"})
	call(browsercontrol.Request{Command: "wait", ID: id, Selector: "#result", Text: "Saved: " + text, TimeoutMS: 5000})
	read := call(browsercontrol.Request{Command: "read", ID: id})
	if read.Document == nil || !strings.Contains(read.Document.Text, "Saved: "+text) || len(read.Document.Elements) < 3 {
		t.Fatal("DOM read/type/click did not run", read)
	}
	shot := call(browsercontrol.Request{Command: "screenshot", ID: id})
	cfg, err := png.DecodeConfig(bytes.NewReader(shot.PNG))
	if err != nil || cfg.Width < 100 || cfg.Height < 100 {
		t.Fatal("native screenshot missing", cfg, err)
	}
	if output := os.Getenv("MYGO_TEST_IMAGES"); output != "" {
		os.WriteFile(filepath.Join(output, "browser-native.png"), shot.PNG, 0600)
	}
	screenshotFile := filepath.Join(dir, "cli-screenshot.png")
	cli("browser", "screenshot", id, "--output", screenshotFile)
	if data, err := os.ReadFile(screenshotFile); err != nil || len(data) < 100 {
		t.Fatal("CLI screenshot missing", err)
	}
	if info, err := os.Stat(screenshotFile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("CLI screenshot is not private", info, err)
	}
	if output := os.Getenv("MYGO_TEST_IMAGES"); output != "" {
		conn, err := net.DialTimeout("unix", perf, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(conn).Encode(map[string]string{"token": token, "op": "capture"})
		var answer map[string]any
		err = json.NewDecoder(conn).Decode(&answer)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if answer["captured"] != true {
			t.Fatal("whole-window capture failed", answer)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "capture.png"))
		os.WriteFile(filepath.Join(output, "browser-pane-layout.png"), data, 0600)
	}
	// Optional bounded rendezvous for compositor/AX inspection. Native child
	// views do not appear in the framework's root-surface PNG capture above.
	if gate := os.Getenv("RETTY_BROWSER_CAPTURE_GATE"); gate != "" {
		if !filepath.IsAbs(gate) {
			t.Fatal("capture gate must be absolute")
		}
		if err := os.WriteFile(gate, []byte(strconv.Itoa(app.cmd.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(45 * time.Second)
		for time.Now().Before(until) {
			if _, err := os.Stat(gate + ".release"); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	call(browsercontrol.Request{Command: "click", ID: id, Selector: "#next-link"})
	call(browsercontrol.Request{Command: "wait", ID: id, Selector: "#next", Text: "Restored browser page", TimeoutMS: 5000})
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := client.Layout()
		if bytes.Contains(raw, []byte("/next")) && bytes.Contains(raw, []byte(id)) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := client.Layout()
	if !bytes.Contains(raw, []byte(id)) || !bytes.Contains(raw, []byte("/next")) {
		t.Fatal("browser layout/URL not saved", string(raw))
	}
	beforeGUI, err := client.List()
	if err != nil || len(beforeGUI) != 1 || beforeGUI[0].ID != session.ID || beforeGUI[0].PID != session.PID || beforeGUI[0].Exited {
		t.Fatal("fixture terminal changed before GUI restart", beforeGUI, err)
	}
	t.Logf("before GUI restart: daemon=%d session=%s shell=%d size=%dx%d", hello.PID, beforeGUI[0].ID, beforeGUI[0].PID, beforeGUI[0].Cols, beforeGUI[0].Rows)
	quit := func(p *process) {
		t.Helper()
		_, err := desktopcontrol.Call(ctx, dir, desktopcontrol.Request{Op: "action", Action: "pane.equalize", SID: session.ID})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialTimeout("unix", perf, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(conn).Encode(map[string]string{"token": token, "op": "quit"})
		conn.Close()
		select {
		case err := <-p.done:
			p.done <- err
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("owned GUI did not quit normally")
		}
	}
	quit(app)
	// A second owned server and real encrypted bridge exercise the remote URL
	// path, including WebSocket traffic, in the restored native GUI.
	remoteDir, err := os.MkdirTemp("/tmp", "retty-browser-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(remoteDir) })
	// The fixture is another installation on this Mac. Give only its child
	// process a disposable machine-ID command, preserving self-pair rejection
	// in the production GUI rather than bypassing it with a runtime flag.
	if err = os.Mkdir(filepath.Join(remoteDir, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(remoteDir, "bin", "ioreg"), []byte("#!/bin/sh\nprintf '\"IOPlatformUUID\" = \"owned-browser-remote-fixture\"\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	start([]string{"-server"}, false, remoteDir)
	var remoteClient *rex.Client
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		remoteClient, err = rex.ConnectDial(ctx, func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", rex.SocketPathForDir(remoteDir))
		})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteClient.Shutdown(); remoteClient.Close() })
	remoteHello, err := remoteClient.Hello()
	if err != nil {
		t.Fatal(err)
	}
	relay := derpserver.New(key.NewNode(), logger.Discard)
	relayHTTP := httptest.NewUnstartedServer(derpserver.Handler(relay))
	relayHTTP.Config.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	relayHTTP.StartTLS()
	defer relayHTTP.Close()
	defer relay.Close()
	region := &tailcfg.DERPRegion{RegionID: 1, RegionCode: "browser-fixture", Nodes: []*tailcfg.DERPNode{{Name: "browser-fixture", RegionID: 1, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", STUNPort: -1, DERPPort: relayHTTP.Listener.Addr().(*net.TCPAddr).Port, InsecureForTests: true}}}
	bridge, err := remote.Start(ctx, rex.SocketPathForDir(remoteDir), remote.Options{BootstrapRegion: region})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	// Exercise diagnostics through a real encrypted extension as well as the
	// GUI's local collector, with an explicitly owned remote profile.
	remoteProfile := filepath.Join(remoteDir, "hook-profile")
	if err := os.Mkdir(remoteProfile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteProfile, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/missing/owned-remote-hook"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	diagClient, diagTunnel, _, _, err := remote.OpenConnection(ctx, bridge.Link(), rex.DeviceInfo{Name: "Owned diagnostic fixture", OS: "fixture"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	remoteDiagnostic, diagErr := remote.DiagnoseHost(ctx, diagClient, diagnostics.Request{Agent: "codex", ConfigDir: remoteProfile, CWD: filepath.Join(remoteDir, "missing-cwd")})
	remote.CloseConnection(diagClient, diagTunnel)
	if diagErr != nil || remoteDiagnostic.Config.Path != filepath.Join(remoteProfile, "hooks.json") || remoteDiagnostic.CWD.Code != "missing" || len(remoteDiagnostic.Hooks) != 1 || remoteDiagnostic.Hooks[0].Executable.Path != "/missing/owned-remote-hook" {
		t.Fatal("encrypted diagnostic did not use the remote profile", remoteDiagnostic, diagErr)
	}
	remoteID := "web-remote-fixture"
	_, portText, _ := net.SplitHostPort(strings.TrimPrefix(fixture.URL, "http://"))
	port, _ := strconv.Atoi(portText)
	remoteURL := fmt.Sprintf("http://localhost:%d/remote", port)
	records := map[string]json.RawMessage{}
	existing, _ := os.ReadFile(filepath.Join(dir, "desktop-connections.json"))
	json.Unmarshal(existing, &records)
	records["history"], _ = json.Marshal([]desktopRecent{{Link: bridge.Link(), ID: remoteHello.Host.ID, Name: "Browser remote fixture"}})
	records["desktop-layouts"], _ = json.Marshal(map[string]savedLayout{remoteHello.Host.ID: {Tabs: []savedTab{{Root: &savedNode{Browser: &savedBrowser{ID: remoteID, URL: remoteURL}}}}}})
	savedRecords, _ := json.Marshal(records)
	if err = os.WriteFile(filepath.Join(dir, "desktop-connections.json"), savedRecords, 0600); err != nil {
		t.Fatal(err)
	}
	again, err := client.Hello()
	if err != nil || again.PID != hello.PID {
		t.Fatal("GUI quit replaced service", again, err)
	}
	app = start(nil, true)
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, err = desktopcontrol.Call(ctx, dir, desktopcontrol.Request{Op: "actions"})
		if err == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	status := call(browsercontrol.Request{Command: "status", ID: id})
	if status.Browser == nil || !strings.HasSuffix(status.Browser.URL, "/next") {
		t.Fatal("browser identity/URL not restored", status)
	}
	call(browsercontrol.Request{Command: "wait", ID: id, Selector: "#next", Text: "Restored browser page", TimeoutMS: 10000})
	deadline = time.Now().Add(3 * time.Second)
	var afterGUI []rex.SessionInfo
	for time.Now().Before(deadline) {
		afterGUI, err = client.List()
		if err == nil && len(afterGUI) == 1 && afterGUI[0].Cols == beforeGUI[0].Cols && afterGUI[0].Rows == beforeGUI[0].Rows {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil || len(afterGUI) != 1 || afterGUI[0].ID != beforeGUI[0].ID || afterGUI[0].PID != beforeGUI[0].PID || afterGUI[0].Exited || afterGUI[0].Cols != beforeGUI[0].Cols || afterGUI[0].Rows != beforeGUI[0].Rows {
		t.Fatal("initialized GUI restore changed terminal identity or geometry", beforeGUI, afterGUI, err)
	}
	t.Logf("after GUI restart: daemon=%d session=%s shell=%d size=%dx%d", again.PID, afterGUI[0].ID, afterGUI[0].PID, afterGUI[0].Cols, afterGUI[0].Rows)
	deadline = time.Now().Add(45 * time.Second)
	remoteReady := false
	for time.Now().Before(deadline) {
		res := call(browsercontrol.Request{Command: "list"})
		for _, b := range res.Browsers {
			if b.ID == remoteID {
				remoteReady = true
			}
		}
		if remoteReady {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !remoteReady {
		data, _ := os.ReadFile(logPath)
		t.Fatal("native GUI did not restore remote browser", string(data))
	}
	call(browsercontrol.Request{Command: "wait", ID: remoteID, Selector: "#remote-wire", Text: "remote-hmr-ready", TimeoutMS: 10000})
	remoteRead := call(browsercontrol.Request{Command: "read", ID: remoteID})
	if remoteRead.Document == nil || remoteRead.Document.URL != remoteURL || remoteRead.Browser.Host == "" {
		t.Fatal("remote browser exposed local forward address", remoteRead)
	}
	call(browsercontrol.Request{Command: "close", ID: remoteID})
	call(browsercontrol.Request{Command: "close", ID: id})
	all, err := client.List()
	if err != nil || len(all) != 1 || all[0].ID != session.ID || all[0].PID != session.PID || all[0].Exited {
		t.Fatal("browser lifecycle ended terminal", all, err)
	}
	quit(app)
	fmt.Fprintln(log, "native browser automation and restore passed")
}
