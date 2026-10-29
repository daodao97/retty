package main

import (
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/automation"
	"retty/internal/rex"
)

// TestRenderTaskBoard writes the task board with realistic data to
// MYGO_TEST_IMAGES, to review its design; it does nothing otherwise.
func TestRenderTaskBoard(t *testing.T) {
	if os.Getenv("MYGO_TEST_IMAGES") == "" {
		t.Skip("set MYGO_TEST_IMAGES to render the task board")
	}
	previous := prefs
	prefs = settings{FontSize: defaultFontSize, DesktopLayout: "sidebar"}
	t.Cleanup(func() { prefs = previous })
	a, _ := newTestApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	dir := rex.Dir()
	now := time.Now()
	check, err := automation.New(automation.Automation{Goal: "检查线上错误请求日志，分析新增或持续发生的错误", Every: "1h", Dir: os.Getenv("HOME"), Agent: []string{"codex"}}, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	recon, err := automation.New(automation.Automation{Goal: "对线上 pomo、zdxjl、bigsnake 三个上游渠道进行对账", Every: "1h", Dir: "/tmp", Agent: []string{"codex"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []automation.Automation{check, recon} {
		if err := automation.Save(dir, item); err != nil {
			t.Fatal(err)
		}
	}
	for i, summary := range []string{"本次无需新增代码修改；发现一项已有修复尚未部署。", "已修复一处日志误分类并提交 Git，尚未部署。", "没有发现新增错误。"} {
		start := now.Add(-time.Duration(3-i) * time.Hour)
		state := "done"
		if i == 1 {
			state = "failed"
		}
		if err := automation.FinishRun(automation.RunPath(dir, check.ID), func(r *automation.Run) {
			*r = automation.Run{Started: start, Finished: start.Add(4 * time.Minute), State: state, Rounds: 1, Summary: summary, Message: "agent 已完成（未设置检查命令）"}
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The reconciliation reports in structure, twice.
	for i, open := range []float64{2790, 2763} {
		start := now.Add(-time.Duration(2-i) * time.Hour)
		if err := automation.FinishRun(automation.RunPath(dir, recon.ID), func(r *automation.Run) {
			*r = automation.Run{Started: start, Finished: start.Add(9 * time.Minute), State: "done", Rounds: 1, Summary: "三个渠道逐笔一致，历史窗口差异仍未决", Result: &automation.Result{
				Status: "warning", Summary: "三个渠道逐笔一致，历史窗口差异仍未决",
				Metrics:   []automation.Metric{{Name: "未决差异", Value: open, Unit: "项"}, {Name: "新增未决", Value: 0, Unit: "项"}, {Name: "已解决", Value: 2790 - open, Unit: "项"}},
				Findings:  []automation.Finding{{Level: "warning", Title: "失败明细接口返回 403", Detail: "zdxjl、bigsnake 的失败收费缺少请求 ID 桥接"}, {Level: "info", Title: "归档通过 969 项校验"}},
				Blockers:  []string{"需开通失败明细视图", "缺少历史价表和充值对价"},
				Artifacts: []string{"reports/upstream-reconciliation/2026-10-29T1134/差异明细.csv"},
				Next:      []string{"权限开通后补核失败记录"},
			}}
		}); err != nil {
			t.Fatal(err)
		}
	}
	live, err := a.client.Create(rex.CreateOptions{Dir: "/tmp", Cols: 100, Rows: 30, Command: []string{"/bin/sh", "-c", "printf '› 检查线上错误请求日志\\n\\n• Ran rg -n error logs/\\n  └ 12 matches\\n\\n• 正在分析新增错误…\\n'; exec sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.Kill(live.ID)
	runner, err := a.client.Create(rex.CreateOptions{Dir: "/tmp", Command: []string{"/bin/sh", "-c", "exec sleep 60"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.Kill(runner.ID)
	if err := automation.UpdateRun(automation.RunPath(dir, check.ID), func(r *automation.Run) {
		*r = automation.Run{Started: now.Add(-2 * time.Minute), Runner: runner.ID, Agent: live.ID, State: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	a.openBoard(check.ID)
	waitFor(t, tt, "board preview", func() bool { return a.taskBoard.term != nil })
	tt.Frame()
	saveSettingsImage(t, tt, "board-light")
	tt.SetDark(true)
	tt.Frame()
	saveSettingsImage(t, tt, "board-dark")
	a.taskBoard.choose(recon.ID)
	waitFor(t, tt, "board result", func() bool { return tt.HasText("最新结果") })
	saveSettingsImage(t, tt, "board-result-dark")
	tt.SetDark(false)
	tt.Frame()
	saveSettingsImage(t, tt, "board-result-light")
}
