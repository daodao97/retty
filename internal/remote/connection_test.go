package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"retty/internal/rex"
	"sync"
	"testing"
	"time"
)

func TestWatchConnectionSurvivesRelayStall(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	client := rex.NewClient(conn, nil)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		decoder := json.NewDecoder(peer)
		for i := 0; ; i++ {
			var req rex.Request
			if decoder.Decode(&req) != nil {
				return
			}
			if i == 0 {
				// Reproduce a brief stall longer than the former two-second
				// deadline, followed by normal responses on the same socket.
				select {
				case <-time.After(5500 * time.Millisecond):
				case <-ctx.Done():
					return
				}
			}
			data, _ := json.Marshal([]rex.SessionInfo{{ID: "existing", PID: 123, Cols: 80, Rows: 24}})
			if json.NewEncoder(peer).Encode(rex.Response{ID: req.ID, Data: data}) != nil {
				return
			}
		}
	}()
	type result struct {
		sessions []rex.SessionInfo
		err      error
	}
	results := make(chan result, 2)
	go watchConnection(ctx, client, nil, func(s []rex.SessionInfo, err error) {
		select {
		case results <- result{s, err}:
		case <-ctx.Done():
		}
	}, 10*time.Millisecond, ResumeCheckTimeout, nil)
	for i := 0; i < 2; i++ {
		select {
		case got := <-results:
			if got.err != nil || len(got.sessions) != 1 || got.sessions[0].PID != 123 || !ConnectionUsable(client) {
				t.Fatalf("transient delay disconnected the original session: %+v", got)
			}
		case <-time.After(12 * time.Second):
			t.Fatal("poll did not recover after the delayed response")
		}
	}
}

func TestWatchConnectionClosesUnresponsiveControl(t *testing.T) {
	// Cover both a blocked request write and a server that reads but never replies.
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked write", true: "missing reply"}[read], func(t *testing.T) {
			conn, peer := net.Pipe()
			defer peer.Close()
			client := rex.NewClient(conn, nil)
			defer client.Close()
			if read {
				go func() {
					var req rex.Request
					json.NewDecoder(peer).Decode(&req)
				}()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			quality := &Quality{}
			go watchConnection(ctx, client, nil, func(_ []rex.SessionInfo, err error) { result <- err }, time.Millisecond, 50*time.Millisecond, quality)
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("unresponsive connection lost timeout diagnosis: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("unresponsive connection did not trigger bounded recovery")
			}
			if sample := quality.Snapshot(time.Now()); sample.Requests != 1 || sample.Timeouts != 1 || sample.Successes != 0 {
				t.Fatal("timed-out poll produced misleading request metrics", sample)
			}
			select {
			case <-client.Closed():
			case <-time.After(time.Second):
				t.Fatal("unresponsive control remained open")
			}
		})
	}
}

func TestWatchConnectionDetectsClosedTransportImmediately(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	client := rex.NewClient(conn, nil)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go watchConnection(ctx, client, nil, func(_ []rex.SessionInfo, err error) { result <- err }, time.Hour, ResumeCheckTimeout, nil)
	peer.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed transport was reported as healthy")
		}
	case <-time.After(time.Second):
		t.Fatal("closed transport waited for the polling deadline")
	}
}

func TestWatchConnectionStoppedDuringPollRetainsSocket(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	client := rex.NewClient(conn, nil)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := make(chan rex.Request, 1)
	go func() {
		var req rex.Request
		if json.NewDecoder(peer).Decode(&req) == nil {
			request <- req
		}
	}()
	delivered := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchConnection(ctx, client, func() *rex.DeviceInfo { return &rex.DeviceInfo{Name: "phone"} }, func(_ []rex.SessionInfo, err error) { delivered <- err }, time.Millisecond, 50*time.Millisecond, nil)
	}()
	var req rex.Request
	select {
	case req = <-request:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	if req.Device == nil || req.Device.Name != "phone" {
		t.Fatal("shared polling lost phone metadata")
	}
	cancel() // Background the phone while the server is still processing LIST.
	select {
	case <-client.Closed():
		t.Fatal("a late foreground timer disconnected the suspended phone")
	case <-time.After(150 * time.Millisecond):
	}
	if err := json.NewEncoder(peer).Encode(rex.Response{ID: req.ID, Data: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stopped poll did not finish")
	}
	select {
	case err := <-delivered:
		t.Fatalf("background poll delivered stale work: %v", err)
	default:
	}
	if !ConnectionUsable(client) {
		t.Fatal("stopped polling discarded the retained connection")
	}
}

func TestResumeCancellationDoesNotWaitForStalledRedial(t *testing.T) {
	testResumeAbandonsStalledRedial(t, false)
}

func TestResumeDeadlineDoesNotWaitForStalledRedial(t *testing.T) {
	testResumeAbandonsStalledRedial(t, true)
}

func testResumeAbandonsStalledRedial(t *testing.T, deadline bool) {
	t.Helper()
	conn, peer := net.Pipe()
	defer peer.Close()
	redialStarted, releaseRedial := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseRedial) })
	defer release()
	lateConn, latePeer := net.Pipe()
	defer latePeer.Close()
	defer lateConn.Close()
	client := rex.NewClient(conn, func(context.Context) (net.Conn, error) {
		close(redialStarted)
		// A transport may still be leaving a suspended network stack after
		// its caller's deadline. Recovery must be free to open a new tunnel.
		<-releaseRedial
		return lateConn, nil
	})
	defer client.Close()
	var ctx context.Context
	var cancel context.CancelFunc
	want := context.Canceled
	if deadline {
		ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
		want = context.DeadlineExceeded
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	done := make(chan error, 1)
	go func() {
		next, _, _, err := resumeConnection(ctx, client, nil, time.Millisecond)
		if next != nil {
			next.Close()
		}
		done <- err
	}()
	select {
	case <-redialStarted:
	case <-time.After(time.Second):
		t.Fatal("redial did not start")
	}
	if !deadline {
		cancel()
	}
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("recovery lost cancellation: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("canceled recovery is waiting for the old tunnel's redial")
	}
	release()
	latePeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.Copy(io.Discard, latePeer); err != nil {
		t.Fatalf("late canceled redial leaked its connection: %v", err)
	}
}
