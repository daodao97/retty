package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseResultKeepsWhatFits(t *testing.T) {
	r, err := ParseResult([]byte(`{"status":" Warning ","summary":"三个渠道\n一致",
		"metrics":[{"name":"未决差异","value":2763,"unit":"项"},{"name":"","value":1}],
		"findings":[{"level":"oops","title":"失败明细 403"},{"level":"ERROR","title":""}],
		"blockers":["需开通视图","  "],"unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "warning" || r.Summary != "三个渠道 一致" || len(r.Metrics) != 1 || r.Metrics[0].Value != 2763 ||
		len(r.Findings) != 1 || r.Findings[0].Level != "info" || len(r.Blockers) != 1 {
		t.Fatalf("cleaned result = %+v", r)
	}
	if r, _ := ParseResult([]byte(`{"status":"great"}`)); !r.Empty() {
		t.Fatal("an unknown status kept", r)
	}
	if _, err := ParseResult([]byte(`{"metrics":[{"name":"n","value":"2,763"}]}`)); err == nil {
		t.Fatal("a metric that is no number accepted")
	}
	if _, err := ParseResult([]byte(strings.Repeat(" ", MaxResult+1))); err == nil {
		t.Fatal("an oversized result accepted")
	}
}

func TestResultFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789ab"
	run := RunPath(dir, id)
	if !IsRunPath(run) || IsRunPath(filepath.Join(dir, "x.json")) || IsRunPath(filepath.Join(dir, "zz.run.json")) {
		t.Fatal("run record paths not recognized")
	}
	if ReadResult(run) != nil {
		t.Fatal("result read before any was left")
	}
	if err := SaveResult(run, Result{Status: "ok", Summary: "s"}); err != nil {
		t.Fatal(err)
	}
	if r := ReadResult(run); r == nil || r.Summary != "s" {
		t.Fatal("result not read back", r)
	}
	if err := Save(dir, Automation{ID: id, Goal: "g", Every: "1h", Dir: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ResultPath(run)); !os.IsNotExist(err) {
		t.Fatal("result kept after the automation was removed")
	}
}
