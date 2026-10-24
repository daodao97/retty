package remote

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"retty/internal/approval"
	"retty/internal/rex"
)

// This is a fake agent in an owned desktop PTY. Native choices must reach that PTY;
// neither the phone runner nor this fixture connects to a real agent session.
func TestTailcatIOSNativeApproval(t *testing.T) {
	fixture := os.Getenv("RETTY_IOS_APPROVAL_FIXTURE")
	headless := os.Getenv("RETTY_APPROVAL_E2E") == "1"
	if fixture == "" && !headless {
		t.Skip("set RETTY_IOS_APPROVAL_FIXTURE for the native iPhone runner")
	}
	dir := t.TempDir()
	if headless {
		fixture = filepath.Join(t.TempDir(), "fixture.json")
	}
	t.Setenv("RETTY_DIR", dir)
	go rex.Serve()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", rex.SocketPath())
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture service unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, err := rex.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer client.Shutdown()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "bin", "codex")
	if err := os.Mkdir(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(iosApprovalProgram), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session, err := client.Create(rex.CreateOptions{Dir: dir, Command: []string{python, script}, Env: []string{"FIXTURE_PYTHON=" + python}, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store := approval.NewStore()
	closeEndpoint, err := approval.Listen(ctx, dir, session.ID, store)
	if err != nil {
		t.Fatal(err)
	}
	defer closeEndpoint()
	for _, agent := range []string{"codex", "claude"} {
		expected := "decline"
		options := []approval.Option{{ID: "accept", Label: "允许本次"}, {ID: "decline", Label: "拒绝"}}
		if agent == "claude" {
			expected = "session-1"
			options = []approval.Option{{ID: "once", Label: "允许本次"}, {ID: "session-1", Label: "允许并应用建议 1（仅本会话）"}, {ID: "deny", Label: "拒绝"}}
		}
		_, err = store.Add(approval.Request{SID: session.ID, Agent: agent, ThreadID: "fixture-" + agent, Title: agent + " · 结构化授权验收", Detail: "echo disposable-fixture; terminal has no authorization menu", Options: options}, func(choice string) error {
			if choice != expected {
				return os.WriteFile(filepath.Join(dir, "wrong-choice"), []byte(choice), 0600)
			}
			return os.WriteFile(filepath.Join(dir, agent+"-decision"), []byte(choice), 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	bridge, err := Start(ctx, rex.SocketPath(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	data, _ := json.Marshal(struct{ Link, Existing, Created, Directory string }{Link: bridge.Link(), Existing: session.ID, Directory: dir})
	if err := os.WriteFile(fixture, data, 0600); err != nil {
		t.Fatal(err)
	}
	if headless {
		phone, closePhone, err := Connect(ctx, bridge.Link())
		if err != nil {
			t.Fatal(err)
		}
		defer closePhone()
		defer phone.Close()
		for _, agent := range []string{"codex", "claude"} {
			var requests []approval.Request
			until := time.Now().Add(15 * time.Second)
			for time.Now().Before(until) {
				requests, err = ListApprovals(ctx, phone, session.ID, "")
				if err == nil && len(requests) == 1 {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			if len(requests) != 1 {
				t.Fatal("encrypted structured request unavailable", agent, err)
			}
			choice := "decline"
			if agent == "claude" {
				choice = "session-1"
			}
			if err = AnswerApproval(ctx, phone, requests[0], choice); err != nil {
				t.Fatal(err)
			}
		}
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if content, err := os.ReadFile(filepath.Join(dir, "approval-result")); err == nil && string(content) == "codex=decline\nclaude=session-1\n" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(fixture), "ios-finished"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for ctx.Err() == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture), "ios-finished")); err == nil {
			result, err := os.ReadFile(filepath.Join(dir, "approval-result"))
			if err != nil || string(result) != "codex=decline\nclaude=session-1\n" {
				t.Fatalf("native choices reached the fixture as %q: %v", result, err)
			}
			list, err := client.List()
			if err != nil || len(list) != 1 || list[0].ID != session.ID || list[0].PID != session.PID {
				t.Fatal("native approval changed the owned session identity", list, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "unexpected-terminal-input")); err == nil {
				t.Fatal("authorization injected terminal input")
			}
			t.Log("PASS: structured native decisions, no terminal input, same owned PTY")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native approval fixture timed out")
}

const iosApprovalProgram = `import os, sys, tty, time, json, socket, select
tty.setraw(sys.stdin.fileno())
def emit(text):
    os.write(1, text.replace('\n', '\r\n').encode())
def event(agent, name):
    s = socket.socket(socket.AF_UNIX)
    s.connect(os.environ['RETTY_AGENT_SOCKET'])
    message = {'id': 1, 'op': 'agentEvent', 'sid': os.environ['RETTY_SESSION'], 'token': os.environ['RETTY_AGENT_TOKEN'], 'agentEvent': {'agent': agent, 'at': time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()), 'input': {'session_id': 'fixture-'+agent, 'hook_event_name': name, 'tool_name': 'Bash', 'cwd': os.getcwd()}}}
    s.sendall((json.dumps(message)+'\n').encode())
    s.recv(4096)
    s.close()
agent = os.path.basename(sys.argv[0])
event(agent, 'SessionStart')
event(agent, 'PermissionRequest')
if agent == 'codex': emit('\x1b]7501;state=blocked:kind=permission:app=codex\x1b\\')
emit('\x1b[H\x1b[2JWaiting for structured authorization. No numbered menu exists.\n')
path = agent+'-decision'
while not os.path.exists(path):
    if select.select([sys.stdin], [], [], .05)[0]:
        incoming = os.read(0, 4096)
        if incoming:
            with open('unexpected-terminal-input', 'wb') as f: f.write(incoming)
            sys.exit(2)
    if os.path.exists('wrong-choice'): sys.exit(3)
with open(path) as f: result = agent+'='+f.read()+'\n'
with open('approval-result', 'a') as f: f.write(result)
event(agent, 'PostToolUse')
if agent == 'codex':
    emit('\x1b]7501;state=clear\x1b\\')
    python = os.environ['FIXTURE_PYTHON']
    os.execv(python, [python, os.path.join(os.getcwd(), 'bin', 'claude')])
emit('\x1b[H\x1b[2JAPPROVAL_FIXTURE_PASSED\n')
time.sleep(120)
`
