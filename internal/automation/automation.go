// Package automation keeps scheduled goals. Each automation runs `retty goal`
// in a terminal session of its own at an interval, so a run is visible and
// can be taken over like any other terminal. The session server knows nothing
// of them: a timer process starts due runs through the ordinary session API,
// which works with already-running servers of any compatible version.
package automation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"retty/internal/atomicfile"
	"retty/internal/rex"
)

// MinEvery bounds how often a run may start; the timer itself wakes once a
// minute.
const MinEvery = time.Minute

// Automation is what a person set up. Only explicit edits write it; runs are
// recorded separately, so a timer never overwrites a pause made meanwhile.
type Automation struct {
	ID string `json:"id"`
	// Title names the task in lists; Goal is the full instruction the
	// agent receives.
	Title string `json:"title,omitempty"`
	Goal  string `json:"goal"`
	Check string `json:"check,omitempty"`
	// Agent is the command that starts a new agent, as ["codex"].
	Agent   []string  `json:"agent"`
	Dir     string    `json:"dir"`
	Rounds  int       `json:"rounds"`
	Every   string    `json:"every"`
	Enabled bool      `json:"enabled"`
	Created time.Time `json:"created"`
	// Once runs a single time, at once, and then stays for its history.
	// One-off work goes here too, so it gets no tab of its own.
	Once bool `json:"once,omitempty"`
}

// Run is the latest run. The timer writes it when it starts a runner; the
// runner adds its agent and result, so a later run can reuse that agent.
type Run struct {
	Started time.Time `json:"started"`
	Runner  string    `json:"runner,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	// State is running, done, failed or skipped.
	State   string    `json:"state,omitempty"`
	Message string    `json:"message,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// Finished, Rounds and Summary describe a run that ended: when, the
	// agent turns it used, and the agent's last reply, which is its report.
	Finished time.Time `json:"finished,omitzero"`
	Rounds   int       `json:"rounds,omitempty"`
	Summary  string    `json:"summary,omitempty"`
	// Result is what the agent reported in structure, when it did.
	Result *Result `json:"result,omitempty"`
}

// historyLimit bounds the runs kept per automation, newest last.
const historyLimit = 200

func historyPath(runPath string) string {
	return strings.TrimSuffix(runPath, ".run.json") + ".history.jsonl"
}

// FinishRun records the end of a run and keeps it in the automation's
// history, which the task board lists.
func FinishRun(path string, change func(*Run)) error {
	var r Run
	readJSON(path, &r)
	change(&r)
	if err := writeRun(path, r); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var lines []string
	if data, err := os.ReadFile(historyPath(path)); err == nil {
		lines = strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
	}
	lines = append(lines, string(line))
	if len(lines) > historyLimit {
		lines = lines[len(lines)-historyLimit:]
	}
	return atomicfile.Write(historyPath(path), []byte(strings.Join(lines, "\n")+"\n"))
}

// History returns up to n past runs, newest first. A damaged line is
// skipped, not the whole history.
func History(dir, id string, n int) []Run {
	data, err := os.ReadFile(historyPath(RunPath(dir, id)))
	if err != nil {
		return nil
	}
	lines := strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
	var runs []Run
	for i := len(lines) - 1; i >= 0 && len(runs) < n; i-- {
		var r Run
		if json.Unmarshal([]byte(lines[i]), &r) == nil {
			runs = append(runs, r)
		}
	}
	return runs
}

// New validates a new automation. It is enabled and due at once.
func New(a Automation, now time.Time) (Automation, error) {
	var id [6]byte
	if _, err := rand.Read(id[:]); err != nil {
		return a, err
	}
	a.ID, a.Created, a.Enabled = hex.EncodeToString(id[:]), now, true
	return a, a.normalize()
}

func (a *Automation) normalize() error {
	if !validID(a.ID) {
		return errors.New("invalid automation ID")
	}
	if strings.TrimSpace(a.Goal) == "" {
		return errors.New("自动化需要目标")
	}
	a.Title = strings.Join(strings.Fields(a.Title), " ")
	if r := []rune(a.Title); len(r) > 40 {
		a.Title = string(r[:40])
	}
	if a.Once {
		a.Every = ""
	} else {
		every, err := time.ParseDuration(a.Every)
		if err != nil || every < MinEvery {
			return fmt.Errorf("间隔至少 %s，例如 30m、2h、24h；只运行一次请用 --once", MinEvery)
		}
		a.Every = every.String()
	}
	if a.Rounds == 0 {
		a.Rounds = 8
	}
	if a.Rounds < 1 || a.Rounds > 100 {
		return errors.New("轮数需在 1 到 100 之间")
	}
	if len(a.Agent) == 0 || a.Agent[0] == "" {
		a.Agent = []string{"claude"}
	}
	if !filepath.IsAbs(a.Dir) {
		return errors.New("自动化的目录必须是绝对路径")
	}
	return nil
}

// Name is the task's short title. One saved without a title is named by
// the first sentence of its goal.
func (a Automation) Name() string {
	if a.Title != "" {
		return a.Title
	}
	name := strings.TrimSpace(a.Goal)
	if i := strings.IndexAny(name, "。；！？\n"); i > 0 {
		name = name[:i]
	}
	name = strings.TrimRight(strings.TrimSpace(name), "，,")
	if r := []rune(name); len(r) > 24 {
		name = string(r[:23]) + "…"
	}
	return name
}

// Interval is the validated interval; zero for an invalid record.
func (a Automation) Interval() time.Duration {
	every, err := time.ParseDuration(a.Every)
	if err != nil || every < MinEvery {
		return 0
	}
	return every
}

// Next is when the next run starts, at once for one that never ran. Runs a
// sleeping or switched-off computer missed start once on waking, not once
// for every interval missed.
// A one-off automation that ran has no next run: zero.
func (a Automation) Next(last Run, now time.Time) time.Time {
	if last.Started.IsZero() {
		return now
	}
	if a.Once {
		return time.Time{}
	}
	return last.Started.Add(a.Interval())
}

func (a Automation) Due(last Run, now time.Time) bool {
	if a.Once {
		return a.Enabled && last.Started.IsZero()
	}
	return a.Enabled && a.Interval() > 0 && !now.Before(a.Next(last, now))
}

func validID(id string) bool {
	_, err := hex.DecodeString(id)
	return err == nil && len(id) == 12
}

func folder(dir string) string { return filepath.Join(dir, "automations") }

func specPath(dir, id string) string { return filepath.Join(folder(dir), id+".json") }

// RunPath is the run record that `retty goal --state-file` keeps current.
func RunPath(dir, id string) string { return filepath.Join(folder(dir), id+".run.json") }

func Save(dir string, a Automation) error {
	if err := a.normalize(); err != nil {
		return err
	}
	if err := os.MkdirAll(folder(dir), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(specPath(dir, a.ID), data)
}

// List returns the valid automations, oldest first. A damaged record is
// skipped rather than stopping every other automation.
func List(dir string) ([]Automation, error) {
	names, err := filepath.Glob(filepath.Join(folder(dir), "*.json"))
	if err != nil {
		return nil, err
	}
	var all []Automation
	for _, name := range names {
		if strings.HasSuffix(name, ".run.json") {
			continue
		}
		var a Automation
		if readJSON(name, &a) != nil || a.normalize() != nil || specPath(dir, a.ID) != name {
			continue
		}
		all = append(all, a)
	}
	slices.SortFunc(all, func(a, b Automation) int { return a.Created.Compare(b.Created) })
	return all, nil
}

// Find accepts a full ID or a unique prefix of at least four characters.
func Find(dir, id string) (Automation, error) {
	all, err := List(dir)
	if err != nil {
		return Automation{}, err
	}
	var found []Automation
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
		if len(id) >= 4 && strings.HasPrefix(a.ID, id) {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		return Automation{}, fmt.Errorf("no single automation matches %q", id)
	}
	return found[0], nil
}

// Remove forgets an automation and its record. Its sessions stay open.
func Remove(dir, id string) error {
	if !validID(id) {
		return errors.New("invalid automation ID")
	}
	if err := os.Remove(specPath(dir, id)); err != nil {
		return err
	}
	for _, path := range []string{RunPath(dir, id), historyPath(RunPath(dir, id)), ResultPath(RunPath(dir, id))} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func LastRun(dir, id string) Run {
	var r Run
	readJSON(RunPath(dir, id), &r)
	return r
}

// UpdateRun changes one field set of a run record, keeping the others.
func UpdateRun(path string, change func(*Run)) error {
	var r Run
	readJSON(path, &r)
	change(&r)
	return writeRun(path, r)
}

func writeRun(path string, r Run) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data)
}

func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(v)
}

// ErrNoTimer tells that this system has no built-in timer for automations.
var ErrNoTimer = errors.New("此系统不会自动定时运行；请用 cron 每分钟运行 retty automation tick")

// Sync installs the timer while any automation is enabled and removes it
// when none is, so Retty adds nothing to launchd until a schedule exists.
func Sync(dir, exe string) error {
	all, err := List(dir)
	if err != nil {
		return err
	}
	// A one-off automation that already ran needs no timer.
	if slices.ContainsFunc(all, func(a Automation) bool { return a.Enabled && !(a.Once && !LastRun(dir, a.ID).Started.IsZero()) }) {
		return Install(dir, exe)
	}
	return Uninstall(dir)
}

// ErrBusy tells that another timer pass is starting runs right now.
var ErrBusy = errors.New("另一次自动化检查正在进行，请稍后重试")

// Tick starts every due run, or only the given automation, at once, when
// only is set. It connects only when something is due, so the minute timer
// costs nothing more than reading a directory. exe handles `goal`.
func Tick(dir, exe string, connect func() (*rex.Client, error), only string, now time.Time) error {
	if err := os.MkdirAll(folder(dir), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(folder(dir), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if only == "" {
			return nil
		}
		return ErrBusy
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	all, err := List(dir)
	if err != nil {
		return err
	}
	var due []Automation
	for _, a := range all {
		if only != "" && a.ID == only || only == "" && a.Due(LastRun(dir, a.ID), now) {
			due = append(due, a)
		}
	}
	if only != "" && len(due) == 0 {
		return fmt.Errorf("no automation %q", only)
	}
	if len(due) == 0 {
		return nil
	}
	c, err := connect()
	if err != nil {
		return err
	}
	defer c.Close()
	sessions, err := c.List()
	if err != nil {
		return err
	}
	find := func(id string) (rex.SessionInfo, bool) {
		i := slices.IndexFunc(sessions, func(s rex.SessionInfo) bool { return id != "" && s.ID == id })
		if i < 0 {
			return rex.SessionInfo{}, false
		}
		return sessions[i], true
	}
	var errs []error
	for _, a := range due {
		last := LastRun(dir, a.ID)
		runner, ok := find(last.Runner)
		// A run that has not finished, for example one waiting for a
		// permission, is not doubled; the next pass looks again.
		if ok && !runner.Exited {
			continue
		}
		// A runner records its own end. One gone while its run is still
		// recorded as running was closed or killed: keep that in the history.
		if last.State == "running" {
			errs = append(errs, FinishRun(RunPath(dir, a.ID), func(r *Run) {
				r.State, r.Message, r.Updated, r.Finished = "interrupted", "运行在结束前中断（runner 会话已关闭）", now, now
			}))
		}
		// The finished runner's session is the automation's own.
		if ok {
			c.Kill(runner.ID)
		}
		argv := []string{exe, "goal", "--goal", a.Goal, "--rounds", strconv.Itoa(a.Rounds), "--state-file", RunPath(dir, a.ID)}
		if a.Check != "" {
			argv = append(argv, "--check", a.Check)
		}
		// Reuse the previous agent while it runs, keeping its context and
		// not adding another tab every interval. One that is busy is in a
		// person's hands; this run waits for the next interval.
		agent := ""
		if s, ok := find(last.Agent); ok && !s.Exited && !s.Idle {
			if s.Agent.State == "running" || s.Agent.State == "waiting" {
				errs = append(errs, FinishRun(RunPath(dir, a.ID), func(r *Run) {
					*r = Run{Started: now, Agent: s.ID, State: "skipped", Message: "agent 正忙，本次跳过", Updated: now, Finished: now}
				}))
				continue
			}
			agent = s.ID
			argv = append(argv, "--target", agent)
		} else {
			argv = append(append(argv, "--"), a.Agent...)
		}
		info, err := c.Create(rex.CreateOptions{Dir: a.Dir, Command: argv, Cols: 100, Rows: 24})
		if err != nil {
			errs = append(errs, fmt.Errorf("automation %s: %w", a.ID, err))
			continue
		}
		errs = append(errs, writeRun(RunPath(dir, a.ID), Run{Started: now, Runner: info.ID, Agent: agent, State: "running", Updated: now}))
	}
	return errors.Join(errs...)
}
