package automation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestUpdateRunKeepsConcurrentChanges changes one record from many writers
// at once, as the timer, a runner and the window do: none loses another's
// change.
func TestUpdateRunKeepsConcurrentChanges(t *testing.T) {
	dir := t.TempDir()
	a, err := New(Automation{Goal: "g", Every: "1h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(dir, a); err != nil {
		t.Fatal(err)
	}
	path := RunPath(dir, a.ID)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := UpdateRun(path, func(r *Run) { r.Rounds++ }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := LastRun(dir, a.ID).Rounds; got != 32 {
		t.Fatalf("rounds = %d after 32 concurrent updates", got)
	}
	if err = Remove(dir, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("run lock kept after remove")
	}
}

func TestDueRunsOnceAfterMissedIntervals(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	a, err := New(Automation{Goal: "keep tests green", Every: "30m", Dir: "/tmp"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Due(Run{}, now) {
		t.Fatal("a new automation is not due at once")
	}
	last := Run{Started: now}
	if a.Due(last, now.Add(29*time.Minute)) || !a.Due(last, now.Add(30*time.Minute)) {
		t.Fatal("interval not honored")
	}
	// A computer asleep for a day starts one run on waking, then waits a
	// whole interval from that run.
	woke := now.Add(24 * time.Hour)
	if !a.Due(last, woke) || a.Due(Run{Started: woke}, woke.Add(time.Minute)) {
		t.Fatal("missed intervals are replayed")
	}
	a.Enabled = false
	if a.Due(last, woke) {
		t.Fatal("a paused automation is due")
	}
}

func TestNameFallsBackToGoal(t *testing.T) {
	for goal, want := range map[string]string{
		"检查线上错误请求日志，分析新增或持续发生的错误。若需要修复，定位原因": "检查线上错误请求日志，分析新增或持续发生的错误",
		"整理 issue":      "整理 issue",
		"每小时检查 CI\n并报告": "每小时检查 CI",
	} {
		if got := (Automation{Goal: goal}).Name(); got != want {
			t.Errorf("Name(%q) = %q, want %q", goal, got, want)
		}
	}
	if got := (Automation{Title: "线上错误日志巡检", Goal: "很长的说明"}).Name(); got != "线上错误日志巡检" {
		t.Errorf("title ignored: %q", got)
	}
}

func TestNewValidates(t *testing.T) {
	now := time.Now()
	for name, a := range map[string]Automation{
		"no goal":        {Every: "1h", Dir: "/tmp"},
		"short":          {Goal: "g", Every: "30s", Dir: "/tmp"},
		"relative dir":   {Goal: "g", Every: "1h", Dir: "project"},
		"too many turns": {Goal: "g", Every: "1h", Dir: "/tmp", Rounds: 101},
	} {
		if _, err := New(a, now); err == nil {
			t.Error(name, "accepted")
		}
	}
	a, err := New(Automation{Goal: "g", Every: "90m", Dir: "/tmp"}, now)
	if err != nil || a.Every != "1h30m0s" || a.Rounds != 8 || len(a.Agent) != 1 || a.Agent[0] != "claude" {
		t.Fatal("defaults not applied", a, err)
	}
}

func TestListSkipsDamagedRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	first, _ := New(Automation{Goal: "first", Every: "1h", Dir: "/tmp"}, now)
	second, _ := New(Automation{Goal: "second", Every: "2h", Dir: "/tmp"}, now.Add(time.Second))
	for _, a := range []Automation{second, first} {
		if err := Save(dir, a); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(folder(dir), "aaaaaaaaaaaa.json"), []byte("{"), 0o600)
	if err := UpdateRun(RunPath(dir, first.ID), func(r *Run) { r.State = "done" }); err != nil {
		t.Fatal(err)
	}
	all, err := List(dir)
	if err != nil || len(all) != 2 || all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatal("list", all, err)
	}
	if LastRun(dir, first.ID).State != "done" {
		t.Fatal("run record lost")
	}
	if found, err := Find(dir, second.ID[:6]); err != nil || found.ID != second.ID {
		t.Fatal("prefix", found, err)
	}
	if err := Remove(dir, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(RunPath(dir, first.ID)); !os.IsNotExist(err) {
		t.Fatal("run record kept after remove")
	}
}
