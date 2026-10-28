package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/push/apns"
	"retty/internal/agentusage"
	"retty/internal/rex"
)

type deviceActivitySender struct {
	provider *apns.Provider
	mu       sync.Mutex
	states   []string
	started  time.Time
}

func (s *deviceActivitySender) Send(ctx context.Context, n apns.Notification) (apns.Response, error) {
	res, err := s.provider.Send(ctx, n)
	if err == nil {
		var state ActivityState
		json.Unmarshal(n.Payload.LiveActivity.ContentState, &state)
		s.mu.Lock()
		s.states = append(s.states, state.Status)
		if s.started.IsZero() {
			s.started = time.Now()
		}
		s.mu.Unlock()
	}
	return res, err
}

func TestTailcatIOSMobileFeatures(t *testing.T) {
	fixture := os.Getenv("RETTY_IOS_FEATURES_FIXTURE")
	if fixture == "" {
		t.Skip("set RETTY_IOS_FEATURES_FIXTURE for the native iPhone runner")
	}
	provider, err := apns.OpenProvider(os.Getenv("RETTY_IOS_FEATURES_PROVIDER_DIR"))
	if err != nil {
		t.Fatal("existing test APNs provider unavailable", err)
	}
	sender := &deviceActivitySender{provider: provider}
	dir := t.TempDir()
	t.Setenv("RETTY_DIR", dir)
	go rex.Serve()
	until := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", rex.SocketPath())
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(until) {
			t.Fatal("fixture server unavailable")
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
	script := filepath.Join(dir, "agent.py")
	if err = os.WriteFile(script, []byte(`import os,sys,time,select,tty
tty.setraw(sys.stdin.fileno())
previous=''
while True:
    try: state=open('phase').read().strip()
    except FileNotFoundError: state='working'
    if state!=previous:
        os.write(1, ('\x1b]7501;state='+state+':app=codex\x1b\\').encode())
        previous=state
    if select.select([sys.stdin],[],[],.1)[0]:
        data=os.read(sys.stdin.fileno(),4096)
        if data: open('unexpected-input','ab').write(data)
`), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := client.Create(rex.CreateOptions{Dir: dir, Command: []string{python, script}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bridge, err := Start(ctx, rex.SocketPath(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	bridge.usageRead = func(context.Context) agentusage.Snapshot {
		count := 2
		return agentusage.Snapshot{At: time.Now(), Readings: []agentusage.PublicReading{{Agent: "codex", Plan: "Fixture Pro", ResetCount: &count, Windows: []agentusage.Window{{Name: "5 小时", Used: 31, Reset: "今天 19:00"}}}}}
	}
	publisher := &activityPublisher{ctx: ctx, socket: rex.SocketPath(), provider: sender, entries: map[string]activityEntry{}, wake: make(chan struct{}, 1)}
	bridge.mu.Lock()
	bridge.activity = publisher
	bridge.mu.Unlock()
	go publisher.run()
	var previews atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/preview" && r.URL.Query().Get("fixture") == "1" {
			previews.Add(1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Retty Preview Fixture</title><h1>手机开发预览已连接</h1><p>加密端口转发正常</p>`)
	}))
	defer web.Close()
	address := "http://localhost:" + strings.Split(web.Listener.Addr().String(), ":")[1] + "/preview?fixture=1"
	data, _ := json.Marshal(struct{ Link, Existing, Created, Directory string }{bridge.Link(), session.ID, address, dir})
	if err = os.WriteFile(fixture, data, 0600); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		sender.mu.Lock()
		started := sender.started
		states := append([]string(nil), sender.states...)
		sender.mu.Unlock()
		if !started.IsZero() {
			phase := "working"
			if time.Since(started) > 8*time.Second {
				phase = "blocked"
			}
			if time.Since(started) > 16*time.Second {
				phase = "done"
			}
			os.WriteFile(filepath.Join(dir, "phase"), []byte(phase), 0600)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture), "ios-finished")); err == nil {
			encoded, _ := json.Marshal(struct {
				PreviewRequests int32
				AcceptedStates  []string
			}{previews.Load(), states})
			os.WriteFile(filepath.Join(filepath.Dir(fixture), "features-receipt.json"), encoded, 0600)
			if previews.Load() == 0 || !containsActivityStates(states, "running", "waiting", "completed") {
				t.Fatal("native preview/background APNs transitions incomplete", string(encoded))
			}
			list, err := client.List()
			if err != nil || len(list) != 1 || list[0].ID != session.ID || list[0].PID != session.PID {
				t.Fatal("mobile features changed owned PTY identity")
			}
			if _, err := os.Stat(filepath.Join(dir, "unexpected-input")); err == nil {
				t.Fatal("feature controls sent terminal input")
			}
			t.Log("PASS: native preview, read-only usage, ActivityKit background APNs, unchanged owned PTY")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("mobile features fixture timed out")
}
func containsActivityStates(states []string, wanted ...string) bool {
	for _, want := range wanted {
		found := false
		for _, state := range states {
			if state == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
