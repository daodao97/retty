package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"retty/internal/automation"
	"retty/internal/controlcli"
	"retty/internal/rex"
)

// An automation's agent submits a structured result; its run keeps it.
func TestAutomationRunKeepsAgentResult(t *testing.T) {
	client, socket := desktopFixtureClient(t)
	t.Setenv("RETTY_SESSION", "")
	t.Setenv("RETTY_AGENT_SOCKET", "")
	tmp := t.TempDir()
	t.Setenv("RETTY_DIR", filepath.Join(tmp, "default"))
	before, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		all, _ := client.List()
		for _, s := range all {
			if !slices.ContainsFunc(before, func(b rex.SessionInfo) bool { return b.ID == s.ID }) {
				client.Kill(s.ID)
			}
		}
	}()
	id := "0123456789ab"
	runFile := automation.RunPath(tmp, id)
	if err := os.MkdirAll(filepath.Dir(runFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runFile, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The command an agent runs: validated, kept beside the run record.
	submit := func(path, data string) (string, error) {
		var out bytes.Buffer
		err := controlcli.Run(context.Background(), []string{"automation", "result", "--run", path}, strings.NewReader(data), &out, io.Discard, controlcli.Options{})
		return out.String(), err
	}
	if _, err := submit(filepath.Join(tmp, "elsewhere.json"), `{"summary":"s"}`); err == nil {
		t.Fatal("result accepted for a path that is no run record")
	}
	if _, err := submit(runFile, `not json`); err == nil {
		t.Fatal("invalid result accepted")
	}
	if out, err := submit(runFile, `{"summary":"stale","metrics":[{"name":"n","value":1}]}`); err != nil || !strings.Contains(out, "1 个指标") {
		t.Fatal(out, err)
	}
	if r := automation.ReadResult(runFile); r == nil || r.Summary != "stale" {
		t.Fatal("submitted result not kept", r)
	}

	// A run clears an earlier result, then keeps the one its agent leaves.
	instruction := filepath.Join(tmp, "instruction")
	result := `{"status":"warning","summary":"三个渠道一致","metrics":[{"name":"未决差异","value":2763,"unit":"项"}],"blockers":["需开通失败明细视图"]}`
	agent := `st() { printf '\033]7501;state=%s\033\\' "$1"; }; printf '%s' "$1" > '` + instruction + `'; printf '%s' '` + result + `' > '` + automation.ResultPath(runFile) + `'; st working; st done; exec sleep 30`
	var out bytes.Buffer
	err = controlcli.Run(context.Background(), []string{"goal", "--socket", socket, "--dir", tmp, "--goal", "对账", "--state-file", runFile, "--turn-timeout", "10s", "--", "/bin/sh", "-c", agent, "agent"}, strings.NewReader(""), &out, io.Discard, controlcli.Options{})
	if err != nil {
		t.Fatal(out.String(), err)
	}
	history := automation.History(tmp, id, 5)
	if len(history) != 1 || history[0].Result == nil {
		t.Fatalf("run kept no result: %+v", history)
	}
	r := history[0]
	if r.Summary != "三个渠道一致" || r.Result.Status != "warning" || len(r.Result.Metrics) != 1 || r.Result.Metrics[0].Value != 2763 || len(r.Result.Blockers) != 1 {
		t.Fatalf("kept result = %+v", r.Result)
	}
	if _, err := os.Stat(automation.ResultPath(runFile)); !os.IsNotExist(err) {
		t.Fatal("result file left for the next run")
	}
	text, _ := os.ReadFile(instruction)
	if !strings.Contains(string(text), "automation result --run "+runFile) || !strings.Contains(string(text), "对账") {
		t.Fatalf("instruction does not ask for a result:\n%s", text)
	}
}

func TestTaskBoardShowsLatestResult(t *testing.T) {
	a, tt := newTestApp(t)
	dir := rex.Dir()
	item, err := automation.New(automation.Automation{Goal: "对账", Every: "1h", Dir: "/tmp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = automation.Save(dir, item); err != nil {
		t.Fatal(err)
	}
	for i, open := range []float64{2790, 2763} {
		start := time.Now().Add(-time.Duration(2-i) * time.Hour)
		if err := automation.FinishRun(automation.RunPath(dir, item.ID), func(r *automation.Run) {
			*r = automation.Run{Started: start, Finished: start.Add(time.Minute), State: "done", Result: &automation.Result{
				Status: "blocked", Metrics: []automation.Metric{{Name: "未决差异", Value: open, Unit: "项"}},
				Blockers: []string{"需开通失败明细视图"}, Artifacts: []string{"reports/差异明细.csv"},
			}}
		}); err != nil {
			t.Fatal(err)
		}
	}
	a.openBoard(item.ID)
	waitFor(t, tt, "latest result", func() bool { return tt.HasText("最新结果") })
	for _, want := range []string{"2,763", "比上次 ↓ 27", "需开通失败明细视图", "reports/差异明细.csv", "受阻"} {
		if !tt.HasText(want) {
			t.Errorf("board lacks %q", want)
		}
	}
}
