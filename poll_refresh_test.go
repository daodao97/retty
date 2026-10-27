package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"retty/internal/agents"
	"retty/internal/rex"
)

func TestIdlePollFramesAndLifecycleMaintenance(t *testing.T) {
	a, _ := newStaticTestApp(t)
	p := a.tab().Focus
	p.terminal().SID = "fixture"
	in := rex.SessionInfo{ID: p.terminal().SID, Program: "sh", Idle: true}
	infos := map[string]rex.SessionInfo{in.ID: in}
	frames := 0
	for i := 0; i < 20; i++ {
		if a.applyPolled(nil, infos) {
			frames++
		}
	}
	if frames != 1 {
		t.Fatal("unchanged polls rebuilt the UI", frames)
	}
	// New lifecycle state and excerpts, not just terminal bytes, need a frame.
	next := in
	next.Agent = rex.AgentState{ID: "codex", State: agents.Completed, CompletionRevision: 1, Summary: "已完成。"}
	changed := map[string]rex.SessionInfo{in.ID: next}
	if !a.applyPolled(nil, changed) || a.applyPolled(nil, changed) {
		t.Fatal("lifecycle changed frame behavior")
	}
	a.post(func() { a.err = "fixture" })
	if !a.applyPolled(nil, changed) {
		t.Fatal("posted UI work stopped without terminal output")
	}
	a.runPosted()
	p.terminal().streamEnded = true
	if !a.applyPolled(nil, changed) {
		t.Fatal("dropped viewer stopped waking maintenance")
	}
	p.terminal().streamEnded = false
	if !a.applyPolled(nil, map[string]rex.SessionInfo{}) {
		t.Fatal("missing session did not refresh")
	}
	t.Logf("20 identical local snapshots: previous 20 frame requests, now %d", frames)
}

func TestPollSnapshotComparesDecodedProgressAndReceipts(t *testing.T) {
	progress := 35
	in := resultFixture("fixture", agents.Running)
	in.Args = []string{"codex", "resume"}
	in.Agent.Progress = &progress
	in.ProgramStatuses = []rex.ProgramStatus{{ID: "build", State: "working", Progress: &progress}}
	data, _ := json.Marshal(in)
	var decoded rex.SessionInfo
	json.Unmarshal(data, &decoded)
	// JSON drops monotonic time; server timestamps arrive decoded on both polls.
	in = decoded
	json.Unmarshal(data, &decoded)
	a := map[string]rex.SessionInfo{in.ID: in}
	b := map[string]rex.SessionInfo{decoded.ID: decoded}
	if !sessionSnapshotsEqual(a, b) {
		t.Fatal("equal progress values forced a frame")
	}
	decoded.AttentionSeen = "receipt"
	b[decoded.ID] = decoded
	if sessionSnapshotsEqual(a, b) {
		t.Fatal("cross-client read receipt did not trigger a frame")
	}
	decoded = in
	decoded.Agent.Summary = "不同结果。"
	b[decoded.ID] = decoded
	if sessionSnapshotsEqual(a, b) {
		t.Fatal("new summary did not trigger a frame")
	}
	decoded = in
	decoded.LastOutput = time.Now()
	b[decoded.ID] = decoded
	if sessionSnapshotsEqual(a, b) {
		t.Fatal("new output did not trigger a frame")
	}
}

func BenchmarkIdleSnapshotComparison(b *testing.B) {
	for _, count := range []int{1, 32, 100} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			a, c := map[string]rex.SessionInfo{}, map[string]rex.SessionInfo{}
			for i := 0; i < count; i++ {
				in := rex.SessionInfo{ID: fmt.Sprint(i), Program: "sh", Args: []string{"-l"}, Idle: true, Cols: 100, Rows: 40}
				a[in.ID], c[in.ID] = in, in
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if !sessionSnapshotsEqual(a, c) {
					b.Fatal("unexpected snapshot difference")
				}
			}
		})
	}
}
