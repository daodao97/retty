//go:build darwin || linux

package rex

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"retty/internal/agents"
)

func TestExplicitProgramReportSharesOSCRecordsAndParserState(t *testing.T) {
	s := stateFixture(t)
	// A direct report must not corrupt a partially received terminal escape.
	s.programStatus.feed([]byte("\x1b]7501;state=working:id=wire"), time.Now())
	progress := 40
	if err := s.reportProgram(&ProgramStatus{ID: "build/tests", State: "blocked", App: "builder", Kind: "question", Message: "Choose target", Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	first := s.programStatus.agentState("")
	if first.State != agents.Waiting || first.Progress == nil || *first.Progress != 40 {
		t.Fatal(first)
	}
	s.reportProgram(&ProgramStatus{ID: "build/tests", State: "blocked", App: "builder", Kind: "question", Message: "Updated question"})
	if got := s.programStatus.agentState(""); got.WaitRevision != first.WaitRevision {
		t.Fatal("duplicate report generated new notice", first, got)
	}
	s.programStatus.feed([]byte("\a"), time.Now())
	if len(s.programStatus.snapshot()) != 2 {
		t.Fatal("direct report corrupted OSC parser", s.programStatus.snapshot())
	}
	if err := s.reportProgram(&ProgramStatus{ID: "build", State: "clear"}); err != nil {
		t.Fatal(err)
	}
	if got := s.programStatus.snapshot(); len(got) != 1 || got[0].ID != "wire" {
		t.Fatal("subtask clear failed", got)
	}
}

func TestExplicitProgramReportRejectsMalformedAtomically(t *testing.T) {
	s := stateFixture(t)
	s.reportProgram(&ProgramStatus{State: "working"})
	before := s.programStatus.snapshot()
	tooMuch := 101
	for _, bad := range []ProgramStatus{{State: "done:app=inject"}, {State: "unknown"}, {State: "working", App: "bad/name"}, {State: "working", ID: "a//b"}, {State: "done", Title: strings.Repeat("x", 193)}, {State: "done", Message: "bad\ntext"}, {State: "working", Progress: &tooMuch}, {State: "done", Progress: new(int)}} {
		if err := s.reportProgram(&bad); err == nil {
			t.Fatal("accepted malformed report", bad)
		}
		if !reflect.DeepEqual(before, s.programStatus.snapshot()) {
			t.Fatal("malformed report changed state", bad)
		}
	}
}

func TestControlInputWritesOnceWithoutAttachmentOrResize(t *testing.T) {
	server := recoveryServer(t)
	s, err := newSession("automation-cat", CreateOptions{Dir: t.TempDir(), Command: []string{"/bin/cat"}, Cols: 91, Rows: 29})
	if err != nil {
		t.Fatal(err)
	}
	server.sessions[s.id] = s
	before := s.info()
	if err := s.sendInput(&InputRequest{Data: []byte("literal-data\n")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(s.vt.Text(), "literal-data") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(s.vt.Text(), "literal-data") {
		t.Fatal("PTY did not receive input")
	}
	after := s.info()
	if before.ID != after.ID || before.PID != after.PID || after.Cols != 91 || after.Rows != 29 || after.Attached != 0 {
		t.Fatal("control changed session ownership", before, after)
	}
	stale := uint64(0)
	if err := s.sendInput(&InputRequest{Data: []byte("must-not-arrive\n"), IfOutput: &stale}); err == nil {
		t.Fatal("stale read accepted")
	}
	if strings.Contains(s.vt.Text(), "must-not-arrive") {
		t.Fatal("rejected input reached PTY")
	}
	if err := s.sendInput(&InputRequest{Data: []byte(strings.Repeat("x", MaxControlInput+1))}); err == nil {
		t.Fatal("unbounded input accepted")
	}
}
