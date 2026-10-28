package remote

import (
	"context"
	"errors"
	"retty/internal/rex"
	"time"
)

// Relayed connections can briefly stall while TCP retransmits or changes paths.
// A single slow LIST must not force an otherwise usable tunnel to reconnect.
const ResumeCheckTimeout = 10 * time.Second
const ResumeRecoveryTimeout = 20 * time.Second

// OpenConnection is the common authenticated handshake for phones and desktops.
// The caller owns a successful transport; failed handshakes clean up themselves.
// Trace and quality are optional and may be nil.
func OpenConnection(ctx context.Context, link string, device rex.DeviceInfo, trace *Diagnostics, quality *Quality) (*rex.Client, func(), rex.Hello, []rex.SessionInfo, error) {
	client, tunnel, err := connect(ctx, link, true, trace, quality)
	var hello rex.Hello
	var sessions []rex.SessionInfo
	if err == nil {
		hello, sessions, err = ReadDesktop(ctx, client, device, trace)
	}
	if err != nil {
		CloseConnection(client, tunnel)
		return nil, nil, hello, nil, err
	}
	return client, tunnel, hello, sessions, nil
}

func ConnectionUsable(client *rex.Client) bool {
	if client == nil {
		return false
	}
	select {
	case <-client.Closed():
		return false
	default:
		return true
	}
}
func CloseConnection(client *rex.Client, tunnel func()) {
	if client != nil {
		client.Close()
	}
	if tunnel != nil {
		tunnel() // Returns at once; the tunnel drains in the background.
	}
}

// CheckConnection bounds requests to a retained socket, including after sleep.
func CheckConnection(ctx context.Context, client *rex.Client, info *rex.DeviceInfo) ([]rex.SessionInfo, error) {
	return checkConnection(ctx, context.Background(), client, info)
}

func checkConnection(ctx, active context.Context, client *rex.Client, info *rex.DeviceInfo) ([]rex.SessionInfo, error) {
	stop := context.AfterFunc(ctx, func() {
		// A phone suspended during a poll can resume after this timer expires.
		// Stopping foreground work must not turn that late timer into a disconnect.
		if active.Err() == nil {
			client.Close()
		}
	})
	var sessions []rex.SessionInfo
	var err error
	if info != nil {
		sessions, err = client.ListFrom(*info)
	} else {
		sessions, err = client.List()
	}
	stop()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return sessions, err
}

// A retained socket that has not answered within this delay is probably
// dead after a suspension or network change. Redial alongside the check
// rather than waiting out its timeout, and keep whichever answers first.
const resumeRedialDelay = 2 * time.Second

// ResumeConnection checks a retained control socket, and redials on the same
// authenticated tunnel when the check fails or is slow. It never restarts the
// session server or replays input.
func ResumeConnection(ctx context.Context, client *rex.Client, info *rex.DeviceInfo) (*rex.Client, rex.Hello, []rex.SessionInfo, error) {
	return resumeConnection(ctx, client, info, resumeRedialDelay)
}

func resumeConnection(ctx context.Context, client *rex.Client, info *rex.DeviceInfo, delay time.Duration) (*rex.Client, rex.Hello, []rex.SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, rex.Hello{}, nil, err
	}
	type result struct {
		client   *rex.Client
		hello    rex.Hello
		sessions []rex.SessionInfo
		err      error
	}
	results := make(chan result, 2)
	// A canceled dial can still return a connection after its network stack
	// wakes. Drain it off the recovery path and close any unclaimed client.
	discard := func(pending int) {
		if pending > 0 {
			go func() {
				for range pending {
					late := <-results
					if late.client != nil && late.client != client {
						late.client.Close()
					}
				}
			}()
		}
	}
	checkCtx, cancelCheck := context.WithTimeout(ctx, ResumeCheckTimeout)
	defer cancelCheck()
	go func() {
		sessions, err := CheckConnection(checkCtx, client, info)
		results <- result{client, rex.Hello{}, sessions, err}
	}()
	redialCtx, cancelRedial := context.WithCancel(ctx)
	defer cancelRedial()
	redialing := false
	redial := func() {
		redialing = true
		go func() {
			next, hello, sessions, err := redialConnection(redialCtx, client, info)
			results <- result{next, hello, sessions, err}
		}()
	}
	start := time.NewTimer(delay)
	defer start.Stop()
	var failure error
	for pending := 1; pending > 0; {
		select {
		case <-ctx.Done():
			discard(pending)
			return nil, rex.Hello{}, nil, ctx.Err()
		case <-start.C:
			if !redialing {
				redial()
				pending++
			}
			continue
		case r := <-results:
			pending--
			if err := ctx.Err(); err != nil {
				if r.client != nil && r.client != client {
					r.client.Close()
				}
				discard(pending)
				return nil, rex.Hello{}, nil, err
			}
			if r.err == nil {
				discard(pending)
				return r.client, r.hello, r.sessions, nil
			}
			// A redial's failure, such as a protocol mismatch, explains
			// more than the stale socket's.
			if r.client != client || failure == nil {
				failure = r.err
			}
			if !redialing && ctx.Err() == nil {
				redial()
				pending++
			}
		}
	}
	if ctx.Err() != nil {
		failure = ctx.Err()
	}
	return nil, rex.Hello{}, nil, failure
}

func redialConnection(ctx context.Context, client *rex.Client, info *rex.DeviceInfo) (*rex.Client, rex.Hello, []rex.SessionInfo, error) {
	next, err := client.Redial(ctx)
	if err != nil {
		return nil, rex.Hello{}, nil, err
	}
	stop := context.AfterFunc(ctx, func() { next.Close() })
	var hello rex.Hello
	var sessions []rex.SessionInfo
	if info != nil {
		hello, err = next.HelloFrom(*info)
	} else {
		hello, err = next.Hello()
	}
	if err == nil && !rex.CompatibleProtocol(hello.Version) {
		err = &ConnectionError{Kind: ProtocolMismatch}
	}
	if err == nil {
		sessions, err = CheckConnection(ctx, next, info)
	}
	stop()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		next.Close()
		return nil, rex.Hello{}, nil, err
	}
	return next, hello, sessions, nil
}

// WatchConnection is the shared foreground connection maintenance loop. Stopping
// it does not destroy a retained tunnel: an in-flight bounded request can finish.
// Delivery runs on this goroutine; consumers dispatch and reject stale UI work.
// A nil quality records nothing.
func WatchConnection(ctx context.Context, client *rex.Client, device func() *rex.DeviceInfo, deliver func([]rex.SessionInfo, error), quality *Quality) {
	watchConnection(ctx, client, device, deliver, 2*time.Second, ResumeCheckTimeout, quality)
}

func watchConnection(ctx context.Context, client *rex.Client, device func() *rex.DeviceInfo, deliver func([]rex.SessionInfo, error), interval, timeout time.Duration, quality *Quality) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.Closed():
			if ctx.Err() == nil {
				deliver(nil, errors.New("connection closed"))
			}
			return
		case <-tick.C:
		}
		if ctx.Err() != nil {
			return
		}
		check, cancel := context.WithTimeout(context.Background(), timeout)
		var info *rex.DeviceInfo
		if device != nil {
			info = device()
		}
		started := time.Now()
		sessions, err := checkConnection(check, ctx, client, info)
		elapsed := time.Since(started)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if quality != nil {
			quality.RecordRequest(time.Now(), elapsed, err)
		}
		deliver(sessions, err)
		if err != nil {
			return
		}
	}
}
func RetryDelay(attempt int) time.Duration {
	return min(time.Duration(1<<min(max(attempt, 0), 3))*time.Second, 5*time.Second)
}

func ReadDesktop(ctx context.Context, client *rex.Client, device rex.DeviceInfo, trace *Diagnostics) (rex.Hello, []rex.SessionInfo, error) {
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	var hello rex.Hello
	var sessions []rex.SessionInfo
	// Prefer the request's deadline over the EOF caused by closing the socket.
	request := func(stage ConnectionStage, fn func() error) error {
		return trace.Measure(stage, func() error {
			err := fn()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		})
	}
	if err := request(StageHello, func() error {
		var err error
		hello, err = client.HelloFrom(device)
		return err
	}); err != nil {
		return hello, nil, err
	}
	if err := request(StageProtocol, func() error {
		trace.ProtocolVersion(hello.Version)
		if !rex.CompatibleProtocol(hello.Version) {
			return &ConnectionError{Kind: ProtocolMismatch}
		}
		return nil
	}); err != nil {
		return hello, nil, err
	}
	err := request(StageSessions, func() error {
		var err error
		sessions, err = client.List()
		return err
	})
	return hello, sessions, err
}
