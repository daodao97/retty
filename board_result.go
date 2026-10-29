package main

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/automation"
)

// runVerdict is what a run came to: a finished run's own result can say it
// met trouble, which the runner alone cannot tell.
func runVerdict(k *colors, r automation.Run) (string, ui.Color) {
	if r.State == "done" && r.Result != nil {
		switch r.Result.Status {
		case "warning":
			return "有警告", stateColor(k, "skipped")
		case "blocked":
			return "受阻", stateColor(k, "failed")
		case "failed":
			return "未达成", stateColor(k, "failed")
		}
	}
	return runStateLabels[r.State], stateColor(k, r.State)
}

func verdictPill(c *ui.Context, k *colors, r automation.Run) {
	label, tone := runVerdict(k, r)
	statusPill(c, label, tone)
}

// formatMetric writes 2,763 or 0.805776036.
func formatMetric(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		s := strconv.FormatInt(int64(math.Abs(v)), 10)
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		if v < 0 {
			s = "-" + s
		}
		return s
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func metricText(m automation.Metric) string {
	if m.Unit == "" {
		return formatMetric(m.Value)
	}
	return formatMetric(m.Value) + " " + m.Unit
}

// metricSeries is a metric's values over the runs that reported it, oldest
// first, the latest n kept. history is newest first.
func metricSeries(history []automation.Run, name string, n int) []float64 {
	var out []float64
	for i := len(history) - 1; i >= 0; i-- {
		if r := history[i].Result; r != nil {
			for _, m := range r.Metrics {
				if m.Name == name {
					out = append(out, m.Value)
					break
				}
			}
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// metricDelta compares the latest value with the one before. Whether up is
// good depends on the metric, so the change is not colored.
func metricDelta(series []float64) string {
	if len(series) < 2 {
		return "首次记录"
	}
	switch d := series[len(series)-1] - series[len(series)-2]; {
	case d > 0:
		return "比上次 ↑ " + formatMetric(d)
	case d < 0:
		return "比上次 ↓ " + formatMetric(-d)
	}
	return "与上次持平"
}

func sparkline(c *ui.Context, series []float64, accent ui.Color) {
	if len(series) < 2 {
		return
	}
	low, high := slices.Min(series), slices.Max(series)
	ui.Row(c).Height(24).Gap(2).AlignItems(ui.End).Shrink(0).Children(func() {
		for i, v := range series {
			h := float32(12)
			if high > low {
				h = 3 + 21*float32((v-low)/(high-low))
			}
			col := accent.Alpha(0.35)
			if i == len(series)-1 {
				col = accent
			}
			ui.Box(c).Size(4, h).Radius(1).Background(col)
		}
	})
}

func metricCard(c *ui.Context, k *colors, key string, m automation.Metric, series []float64, accent ui.Color) {
	boardCard(c, k, key).Padding(12, 14).Gap(6).Grow(1).Basis(0).MinWidth(0).Children(func() {
		ui.Text(c, m.Name).FontSize(12).TextColor(k.textMuted).SingleLine().Ellipsis("…").MinWidth(0)
		ui.Row(c).Gap(4).AlignItems(ui.End).Children(func() {
			ui.Text(c, formatMetric(m.Value)).FontSize(22).FontWeight(600).TextColor(k.text)
			if m.Unit != "" {
				ui.Text(c, m.Unit).FontSize(12).TextColor(k.textFaint).Padding(0, 0, 3, 0)
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, metricDelta(series)).FontSize(11.5).TextColor(k.textFaint).Grow(1).MinWidth(0)
			sparkline(c, series, accent)
		})
	})
}

func findingTone(k *colors, level string) ui.Color {
	switch level {
	case "error":
		return stateColor(k, "failed")
	case "warning":
		return stateColor(k, "skipped")
	}
	return k.textFaint
}

// openArtifact opens a file a run produced, relative to the task's directory.
func (a *App) openArtifact(dir, path string) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if err := mygo.Shell.OpenPath(path); err != nil {
		a.automations.err = "无法打开 " + path + "：" + err.Error()
	}
}

// boardResult shows the latest structured result: its metrics against the
// runs before, then its findings, blockers, next steps and files.
func (a *App) boardResult(c *ui.Context, k *colors, b *boardPane, item automation.Automation) {
	at := slices.IndexFunc(b.history, func(r automation.Run) bool { return r.Result != nil })
	if at < 0 {
		return
	}
	run := b.history[at]
	res := *run.Result
	ui.Column(c.Key("board-result")).Gap(12).FillWidth().Children(func() {
		boardSection(c, k, "最新结果", run.Started.Local().Format("01-02 15:04"))
		if res.Summary != "" {
			ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
				verdictPill(c, k, run)
				ui.Text(c, res.Summary).FontSize(13.5).LineHeight(1.5).TextColor(k.text).Grow(1).MinWidth(0).Wrap()
			})
		}
		accent := boardAccent(c)
		for start := 0; start < len(res.Metrics); start += 4 {
			ui.Row(c.Key(fmt.Sprint("board-metrics-", start))).Gap(10).FillWidth().Children(func() {
				for i, m := range res.Metrics[start:min(start+4, len(res.Metrics))] {
					metricCard(c, k, fmt.Sprint("board-metric-", start+i), m, metricSeries(b.history[at:], m.Name, 12), accent)
				}
			})
		}
		if len(res.Findings)+len(res.Blockers)+len(res.Next)+len(res.Artifacts) == 0 {
			return
		}
		boardCard(c, k, "board-result-detail").Children(func() {
			first := true
			group := func(key, title string, body func()) {
				col := ui.Column(c.Key(key)).Gap(9).Padding(14, 18).FillWidth()
				if !first {
					col.BorderWidth(1, 0, 0, 0).BorderColor(k.panelBorder)
				}
				first = false
				col.Children(func() {
					ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(k.textMuted)
					body()
				})
			}
			entry := func(tone ui.Color, text, detail string) {
				ui.Row(c).Gap(10).AlignItems(ui.Start).FillWidth().Children(func() {
					ui.Box(c).Padding(6, 0, 0, 0).Shrink(0).Children(func() { statusDot(c, tone) })
					ui.Column(c).Gap(3).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, text).FontSize(13).LineHeight(1.5).TextColor(k.text).FillWidth().Wrap()
						if detail != "" {
							ui.Text(c, detail).FontSize(12).LineHeight(1.5).TextColor(k.textMuted).FillWidth().Wrap()
						}
					})
				})
			}
			if len(res.Findings) > 0 {
				group("board-findings", fmt.Sprintf("发现 %d", len(res.Findings)), func() {
					for _, f := range res.Findings {
						entry(findingTone(k, f.Level), f.Title, f.Detail)
					}
				})
			}
			if len(res.Blockers) > 0 {
				group("board-blockers", "阻塞", func() {
					for _, s := range res.Blockers {
						entry(stateColor(k, "skipped"), s, "")
					}
				})
			}
			if len(res.Next) > 0 {
				group("board-next", "下一步", func() {
					for _, s := range res.Next {
						entry(k.textFaint, s, "")
					}
				})
			}
			if len(res.Artifacts) > 0 {
				group("board-artifacts", "产物", func() {
					for i, path := range res.Artifacts {
						b := ui.ButtonBase(c.Key(fmt.Sprint("board-artifact-", i))).Label("打开 "+path).Tooltip(path).FillWidth().
							Height(28).Padding(0, 8).Gap(8).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer)
						if b.Hovered() || b.FocusVisible() {
							b.Background(k.hover)
						}
						b.Children(func() {
							ui.Icon(c, icon("file-code")).Size(14, 14).TextColor(k.iconMuted).Shrink(0)
							ui.Text(c, path).Font(termFont.Family).FontSize(12).TextColor(k.text).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
							ui.Icon(c, icon("arrow-up-right")).Size(12, 12).TextColor(k.iconMuted).Shrink(0)
						})
						if b.Clicked() {
							a.openArtifact(item.Dir, path)
						}
					}
				})
			}
		})
	})
}

// resultBrief is a history row's line of a run's result.
func resultBrief(r automation.Result) string {
	var parts []string
	for i, m := range r.Metrics {
		if i == 3 {
			break
		}
		parts = append(parts, m.Name+" "+metricText(m))
	}
	if n := len(r.Findings); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 项发现", n))
	}
	if n := len(r.Blockers); n > 0 {
		parts = append(parts, fmt.Sprintf("%d 个阻塞", n))
	}
	return strings.Join(parts, "  ·  ")
}

// mobileResult shows a run's result on the phone: its numbers, then what
// blocks the task and what it found.
func mobileResult(c *ui.Context, r automation.Result) {
	muted := c.Theme().TextMuted
	for _, m := range r.Metrics {
		ui.Text(c, m.Name+"："+metricText(m)).FontSize(13).FillWidth().Wrap()
	}
	for _, s := range r.Blockers {
		ui.Text(c, "阻塞："+s).FontSize(12).TextColor(muted).FillWidth().Wrap()
	}
	for _, f := range r.Findings {
		text := f.Title
		if f.Detail != "" {
			text += "：" + f.Detail
		}
		ui.Text(c, "· "+text).FontSize(12).TextColor(muted).FillWidth().Wrap()
	}
}
