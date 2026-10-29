package automation

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"retty/internal/atomicfile"
)

// Result is what an automation's agent reports at the end of a run, beyond
// its last reply: a verdict, numbers to compare across runs, findings, what
// blocks the task, the files it produced and what to do next. Every field
// is optional.
type Result struct {
	// Status is ok, warning, blocked or failed.
	Status    string    `json:"status,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Metrics   []Metric  `json:"metrics,omitempty"`
	Findings  []Finding `json:"findings,omitempty"`
	Blockers  []string  `json:"blockers,omitempty"`
	Artifacts []string  `json:"artifacts,omitempty"`
	Next      []string  `json:"next,omitempty"`
}

// Metric is a number the task board compares with earlier runs by name.
type Metric struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
}

// Finding is one observation; Level is info, warning or error.
type Finding struct {
	Level  string `json:"level"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// MaxResult bounds a submitted result.
const MaxResult = 64 << 10

// ResultPath is where an agent leaves its result for the runner of the run
// record runPath. It is a file of its own, so the agent never writes the
// record the runner keeps current.
func ResultPath(runPath string) string {
	return strings.TrimSuffix(runPath, ".run.json") + ".result.json"
}

// IsRunPath tells that path names an automation's run record.
func IsRunPath(path string) bool {
	id, ok := strings.CutSuffix(filepath.Base(path), ".run.json")
	return ok && validID(id) && strings.ToLower(id) == id
}

// ParseResult reads a submitted result, keeping what fits its bounds:
// unknown levels and statuses fall back, empty and excess entries go.
func ParseResult(data []byte) (Result, error) {
	if len(data) > MaxResult {
		return Result{}, fmt.Errorf("结果超过 %d KiB", MaxResult>>10)
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("结果不是有效的 JSON：%w", err)
	}
	return r.clean(), nil
}

func (r Result) clean() Result {
	out := Result{Summary: clip(r.Summary, 600)}
	switch s := strings.ToLower(strings.TrimSpace(r.Status)); s {
	case "ok", "warning", "blocked", "failed":
		out.Status = s
	}
	for _, m := range r.Metrics {
		name := clip(m.Name, 40)
		if name == "" || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) || len(out.Metrics) == 12 {
			continue
		}
		out.Metrics = append(out.Metrics, Metric{Name: name, Value: m.Value, Unit: clip(m.Unit, 12)})
	}
	for _, f := range r.Findings {
		title := clip(f.Title, 120)
		if title == "" || len(out.Findings) == 30 {
			continue
		}
		level := strings.ToLower(strings.TrimSpace(f.Level))
		if level != "warning" && level != "error" {
			level = "info"
		}
		out.Findings = append(out.Findings, Finding{Level: level, Title: title, Detail: clip(f.Detail, 600)})
	}
	out.Blockers, out.Artifacts, out.Next = clipList(r.Blockers), clipList(r.Artifacts), clipList(r.Next)
	return out
}

// Empty tells that a result says nothing.
func (r Result) Empty() bool {
	return r.Status == "" && r.Summary == "" && len(r.Metrics) == 0 && len(r.Findings) == 0 &&
		len(r.Blockers) == 0 && len(r.Artifacts) == 0 && len(r.Next) == 0
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func clipList(items []string) []string {
	var out []string
	for _, item := range items {
		if item = clip(item, 300); item != "" && len(out) < 20 {
			out = append(out, item)
		}
	}
	return out
}

// SaveResult leaves a result for the runner of the run record runPath.
func SaveResult(runPath string, r Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.Write(ResultPath(runPath), data)
}

// ReadResult is the result left for the run record runPath, or nil.
func ReadResult(runPath string) *Result {
	f, err := os.Open(ResultPath(runPath))
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxResult+1))
	if err != nil {
		return nil
	}
	r, err := ParseResult(data)
	if err != nil || r.Empty() {
		return nil
	}
	return &r
}
