package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/push/apns"
	"retty/internal/agentusage"
	"retty/internal/rex"
)

func TestUsageExtensionCoalescesReadsAndNeverTouchesSessionDaemon(t *testing.T) {
	client, b, _, _ := fileUploadFixture(t)
	var reads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	b.usageRead = func(context.Context) agentusage.Snapshot {
		reads.Add(1)
		close(started)
		<-release
		return agentusage.Snapshot{At: time.Now(), Readings: []agentusage.PublicReading{{Agent: "codex", Plan: "pro"}}}
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			value, err := ReadUsage(context.Background(), client, true)
			if err == nil && (len(value.Readings) != 1 || value.Readings[0].Plan != "pro") {
				err = errors.New("missing usage snapshot")
			}
			results <- err
		})
	}
	<-started
	close(release)
	wg.Wait()
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 1 {
		t.Fatal("concurrent refresh spawned duplicate CLI readers", reads.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadUsage(ctx, client, false); err == nil {
		t.Fatal("canceled query succeeded")
	}
	if _, err := ReadUsage(context.Background(), client, false); err != nil {
		t.Fatal("canceled extension closed shared control client", err)
	}
}

type recordingActivitySender struct {
	notices []apns.Notification
	err     error
}

func (s *recordingActivitySender) Send(_ context.Context, n apns.Notification) (apns.Response, error) {
	s.notices = append(s.notices, n)
	return apns.Response{}, s.err
}
func TestActivityPublisherDeduplicatesTransitionsAndEndsWithoutPTYOperations(t *testing.T) {
	sender := &recordingActivitySender{}
	s := rex.SessionInfo{ID: "session", Args: []string{"secret-command"}, Dir: "/secret/path", Agent: rex.AgentState{State: "running", Message: "private-prompt"}}
	p := &activityPublisher{ctx: context.Background(), provider: sender, wake: make(chan struct{}, 1), entries: map[string]activityEntry{}, read: func(context.Context) (rex.Hello, []rex.SessionInfo, error) {
		return rex.Hello{Host: rex.HostInfo{ID: "desktop", Name: "Mac"}}, []rex.SessionInfo{s}, nil
	}}
	req := ActivityRegistration{DeviceID: strings.Repeat("a", 32), Token: strings.Repeat("b", 64), ActivityID: "apple-id", SessionID: s.ID}
	if err := p.register(req); err != nil {
		t.Fatal(err)
	}
	p.publish()
	p.publish()
	if len(sender.notices) != 1 {
		t.Fatal("unchanged state was pushed repeatedly")
	}
	data, _ := json.Marshal(sender.notices[0].Payload)
	for _, secret := range []string{"secret-command", "/secret/path", "private-prompt", "alert", "sound"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("lockscreen payload leaked private data", secret)
		}
	}
	s.Agent.State = "waiting"
	p.publish()
	if len(sender.notices) != 2 || sender.notices[1].Payload.LiveActivity.Event != "update" {
		t.Fatal("waiting transition missing")
	}
	s.Agent.State = "completed"
	p.publish()
	p.publish()
	if len(sender.notices) != 3 || sender.notices[2].Payload.LiveActivity.Event != "end" || len(p.entries) != 0 {
		t.Fatal("completed activity kept publishing")
	}
	state := ActivityContent(rex.HostInfo{ID: "desktop", Name: "Mac"}, s)
	d, sid, ok := ParseActivityURL(state.URL)
	if !ok || d != "desktop" || sid != s.ID {
		t.Fatal("activity deep link cannot return to known session")
	}
}
func TestActivityTokenRotationAndUnregistration(t *testing.T) {
	p := &activityPublisher{entries: map[string]activityEntry{}, wake: make(chan struct{}, 1)}
	r := ActivityRegistration{DeviceID: strings.Repeat("a", 32), Token: strings.Repeat("b", 64), ActivityID: "id", SessionID: "sid"}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	p.register(r)
	r.Token = strings.Repeat("c", 64)
	p.register(r)
	if len(p.entries) != 1 {
		t.Fatal("rotation duplicated activity")
	}
	r.Disabled = true
	p.register(r)
	if len(p.entries) != 0 {
		t.Fatal("dismissed activity remained subscribed")
	}
	r.Disabled = false
	r.Token = "alert-token"
	if r.validate() == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestActivityDoesNotShowRunningAfterAgentReturnsToShell(t *testing.T) {
	state := ActivityContent(rex.HostInfo{Name: "Mac"}, rex.SessionInfo{ID: "shell", Idle: true})
	if !activityEnded(state) {
		t.Fatal("an idle shell kept a false running activity", state)
	}
	state = ActivityContent(rex.HostInfo{Name: "Mac"}, rex.SessionInfo{ID: "unreported"})
	if state.Status != "unknown" {
		t.Fatal("missing status presented as running")
	}
}
