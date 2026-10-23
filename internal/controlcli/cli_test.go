package controlcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"retty/internal/rex"
)

func TestArgumentsPreserveTargetsAndArgv(t *testing.T) {
	a, err := parse([]string{"split", "abcd1234", "--json", "--direction", "down", "--dir", "/tmp/a b", "--", "/bin/sh", "-c", "printf '%s' \"$x\""}, io.Discard)
	if err != nil || a.target != "abcd1234" || !a.json || a.direction != "down" || a.dir != "/tmp/a b" || !reflect.DeepEqual(a.argv, []string{"/bin/sh", "-c", "printf '%s' \"$x\""}) {
		t.Fatal(a, err)
	}
	for _, args := range [][]string{{"list", "oops"}, {"send", "abcd", "--bogus"}, {"read", "--json", "--ansi"}, {"send", "a", "b"}, {"new", "--cols"}, {"read", "abcd", "--", "extra"}} {
		if _, err := parse(args, io.Discard); err == nil {
			t.Fatal("accepted invalid arguments", args)
		}
	}
	for _, args := range [][]string{{"status"}, {"status", "--data-dir", "/tmp/x"}, {"status", "--data-dir=/tmp/x"}} {
		if Handles(args) {
			t.Fatal("stole gateway status", args)
		}
	}
	for _, args := range [][]string{{"status", "abcd"}, {"session", "status"}, {"list"}, {"send", "--target", "abcd"}} {
		if !Handles(args) {
			t.Fatal("missing automation route", args)
		}
	}
}

func TestOldServerNeverReceivesAutomationInput(t *testing.T) {
	var mu sync.Mutex
	var ops []string
	options := Options{Dial: func(context.Context, string) (net.Conn, error) {
		server, client := net.Pipe()
		go func() {
			defer server.Close()
			dec := json.NewDecoder(server)
			enc := json.NewEncoder(server)
			for {
				var req rex.Request
				if dec.Decode(&req) != nil {
					return
				}
				mu.Lock()
				ops = append(ops, req.Op)
				mu.Unlock()
				var data any
				switch req.Op {
				case "list":
					data = []rex.SessionInfo{{ID: "test-session"}}
				case "hello":
					data = rex.Hello{Version: 6}
				}
				payload, _ := json.Marshal(data)
				if enc.Encode(rex.Response{ID: req.ID, Data: payload}) != nil {
					return
				}
			}
		}()
		return client, nil
	}}
	err := Run(context.Background(), []string{"send", "test-session", "--text", "never"}, strings.NewReader(""), io.Discard, io.Discard, options)
	if err == nil || !strings.Contains(err.Error(), "preserve running tasks") {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, op := range ops {
		if op != "list" && op != "hello" {
			t.Fatal("old server received a mutation", ops)
		}
	}
}

func TestWaitDeadlineDuringInitialReadReturns124(t *testing.T) {
	done := make(chan struct{})
	options := Options{Dial: func(context.Context, string) (net.Conn, error) {
		server, client := net.Pipe()
		go func() { defer close(done); defer server.Close(); io.Copy(io.Discard, server) }()
		return client, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Run(ctx, []string{"wait", "test-session", "--timeout", "20ms"}, strings.NewReader(""), io.Discard, io.Discard, options)
	if err == nil || ExitCode(err) != 124 {
		t.Fatal("initial read deadline lost timeout code", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled reader leaked")
	}
}

func TestResolveUsesStableIDsAndRejectsAmbiguity(t *testing.T) {
	all := []rex.SessionInfo{{ID: "abcd0001", Title: "name"}, {ID: "abcd0002", Title: "other"}, {ID: "efgh0003"}}
	for _, target := range []string{"", "abc", "abcd", "name", "0"} {
		if _, err := Resolve(all, target); err == nil {
			t.Fatal("guessed a target", target)
		}
	}
	for _, target := range []string{"abcd0001", "efgh"} {
		s, err := Resolve(all, target)
		if err != nil || !strings.HasPrefix(s.ID, target) {
			t.Fatal(s, err)
		}
	}
}

func TestAutomationReadDoesNotStartService(t *testing.T) {
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	count := 0
	err := Run(context.Background(), []string{"list", "--data-dir", t.TempDir()}, strings.NewReader(""), io.Discard, io.Discard, Options{Dial: func(context.Context, string) (net.Conn, error) { count++; return nil, net.ErrClosed }})
	if err == nil || count != 1 {
		t.Fatal("read retried or started a service", count, err)
	}
}

func TestSendPreservesLiteralBytesAndBoundsInput(t *testing.T) {
	a := arguments{text: "echo $(danger)\n", enter: true}
	data, err := a.input(strings.NewReader("ignored"))
	if err != nil || !bytes.Equal(data, []byte("echo $(danger)\n\r")) {
		t.Fatal(data, err)
	}
	for _, a := range []arguments{{stdin: true, text: "x"}, {key: "up"}, {stdin: true}, {}} {
		in := strings.NewReader(strings.Repeat("x", rex.MaxControlInput+1))
		if _, err := a.input(in); err == nil {
			t.Fatal("accepted invalid input", a)
		}
	}
}
