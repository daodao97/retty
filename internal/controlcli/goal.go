package controlcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"retty/internal/automation"
	"retty/internal/rex"
)

// goal drives one agent session toward a goal that a check command can
// verify. The agent plans and edits on its own; the runner only decides
// whether the goal is met, pushes once more when it is not, and stops at a
// limit. It composes the public session commands, so it follows the same
// socket selection and never starts or replaces a server.
type goal struct {
	ctx     context.Context
	options Options
	common  []string
	out     io.Writer

	text, check, dir, title, progress string
	agent, self, stateFile            string
	argv                              []string
	rounds                            int
	turnTimeout, checkTimeout         time.Duration

	// waiting tells that the agent asks the person for something, as a
	// permission; waitSeen is the request already reported.
	waiting  bool
	waitSeen uint64
	// round and summary are the turn in progress and the agent's last
	// reply, which a finished automation run keeps as its report.
	round   int
	summary string
}

func runGoal(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, options Options) error {
	g := goal{ctx: ctx, options: options, out: output}
	var socket, dataDir string
	f := flag.NewFlagSet("goal", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	f.StringVar(&g.agent, "target", "", "drive this existing agent session")
	f.StringVar(&g.text, "goal", "", "the goal, sent as the agent's instruction")
	f.StringVar(&g.check, "check", "", "shell command whose exit status 0 means the goal is met")
	f.StringVar(&g.dir, "dir", "", "working directory of a new agent and of the check")
	f.IntVar(&g.rounds, "rounds", 8, "maximum agent turns, 1..100")
	f.DurationVar(&g.turnTimeout, "turn-timeout", time.Hour, "deadline of one agent turn")
	f.DurationVar(&g.checkTimeout, "check-timeout", 10*time.Minute, "deadline of one check")
	f.StringVar(&socket, "socket", "", "session socket path")
	f.StringVar(&dataDir, "data-dir", "", "owning service/GUI data directory")
	f.StringVar(&g.stateFile, "state-file", "", "automation run record to keep current")
	if i := slices.Index(args, "--"); i >= 0 {
		args, g.argv = args[:i], slices.Clone(args[i+1:])
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, Usage)
		}
		return err
	}
	switch {
	case f.NArg() > 0:
		return errors.New("unexpected positional arguments; pass the agent command after --")
	case g.agent == "" && g.text == "":
		return errors.New("goal requires --goal, or --target for an agent that already has its task")
	case g.agent != "" && len(g.argv) > 0:
		return errors.New("-- AGENT is only valid without --target")
	case g.rounds < 1 || g.rounds > 100:
		return errors.New("rounds must be between 1 and 100")
	case g.turnTimeout <= 0 || g.checkTimeout <= 0:
		return errors.New("timeouts must be positive")
	}
	if len(g.argv) == 0 {
		g.argv = []string{"claude"}
	}
	if socket != "" {
		g.common = append(g.common, "--socket", socket)
	}
	if dataDir != "" {
		g.common = append(g.common, "--data-dir", dataDir)
	}
	g.self = os.Getenv("RETTY_SESSION")
	g.title = shorten(g.text, 40)
	if g.title == "" {
		g.title = "直到检查通过"
	}
	return g.run()
}

func (g *goal) run() error {
	// A result left by an earlier run is not this run's.
	if g.stateFile != "" {
		os.Remove(automation.ResultPath(g.stateFile))
	}
	started := g.agent == ""
	if started {
		if err := g.start(); err != nil {
			return err
		}
	}
	s, err := g.status()
	if err != nil {
		return err
	}
	if g.dir == "" {
		g.dir = s.Dir
	}
	g.agent = s.ID
	fmt.Fprintf(g.out, "目标：%s\nagent 会话：%s\n", g.title, s.ID)
	g.record("running", "")
	// A new session has no earlier result, and its first turn may already
	// have finished before this first status.
	after := s.Agent.CompletionRevision
	if started {
		after = 0
	}
	g.progress = fmt.Sprintf("第 1/%d 轮 · agent 执行中", g.rounds)
	g.report("working", "", g.progress)
	// An existing agent that already finished its task goes straight to the
	// check; one given a goal receives it as its next instruction.
	skip := false
	if !started && g.text != "" {
		if err = g.send(g.instruction()); err != nil {
			return g.fail(err)
		}
	} else if !started {
		skip = s.Agent.State == "completed" || s.Agent.State == "failed"
	}
	for round := 1; ; round++ {
		if !skip {
			if s, err = g.await(after); err != nil {
				return g.fail(err)
			}
			after = s.Agent.CompletionRevision
		}
		skip = false
		g.round, g.summary = round, s.Agent.Summary
		if g.check == "" {
			g.report("done", "", "agent 已完成（未设置检查命令）")
			g.finish("done", "agent 已完成（未设置检查命令）")
			fmt.Fprintln(g.out, "agent 已完成。")
			return nil
		}
		output, code, err := g.runCheck()
		if err != nil {
			return g.fail(err)
		}
		if code == 0 {
			message := fmt.Sprintf("目标达成 · %d 轮 · 检查通过", round)
			g.report("done", "", message)
			g.finish("done", message)
			fmt.Fprintln(g.out, message)
			return nil
		}
		if round >= g.rounds {
			return g.fail(fmt.Errorf("%d 轮后检查仍失败（退出码 %d）", round, code))
		}
		g.progress = fmt.Sprintf("第 %d/%d 轮 · 检查失败（退出码 %d），agent 继续", round+1, g.rounds, code)
		g.report("working", "", g.progress)
		if err = g.send(g.followUp(code, output)); err != nil {
			return g.fail(err)
		}
	}
}

// start opens the agent beside the runner when the desktop can place it, so
// both share a tab, and otherwise as its own session.
func (g *goal) start() error {
	if g.dir == "" {
		var err error
		if g.dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs(g.dir)
	if err != nil {
		return err
	}
	g.dir = dir
	argv := append(slices.Clone(g.argv), g.instruction())
	// An automation run has no tab: the task board shows its agent, and
	// a split would place the agent in the desktop's tab tree.
	var id string
	split := g.self != "" && g.stateFile == ""
	if split {
		id, err = g.call(nil, []string{"split", g.self, "--direction", "right", "--dir", g.dir}, argv)
	}
	if !split || err != nil {
		if id, err = g.call(nil, []string{"new", "--dir", g.dir}, argv); err != nil {
			return err
		}
	}
	g.agent = strings.TrimSpace(id)
	return nil
}

func (g *goal) instruction() string {
	text := g.task()
	if g.stateFile != "" {
		text += "\n\n" + g.resultInstruction()
	}
	return strings.TrimSpace(text)
}

// resultInstruction asks an automation's agent for a structured result,
// which the task board shows and compares across runs.
func (g *goal) resultInstruction() string {
	exe, err := Executable()
	if err != nil {
		return ""
	}
	return fmt.Sprintf(`结束前，把本次结果以 JSON 提交给 Retty（可重复提交，以最后一次为准）：
%s automation result --run %s <<'EOF'
{"status":"ok","summary":"一句话结论","metrics":[{"name":"新增错误","value":0,"unit":"条"}],"findings":[{"level":"warning","title":"问题","detail":"说明"}],"blockers":["阻塞原因"],"artifacts":["reports/结果.csv"],"next":["下一步"]}
EOF
字段都可选，没有就省略，不要编造。status 取 ok、warning、blocked、failed；metrics 的 value 必须是数字，同一指标每次使用相同的 name 和 unit，便于跨次比较；findings 的 level 取 info、warning、error；artifacts 写相对任务目录的路径或绝对路径。`, ShellWord(exe), ShellWord(g.stateFile))
}

// ShellWord quotes a word for an agent to paste into a shell command.
func ShellWord(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+@:", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// task is the instruction of the goal itself.
func (g *goal) task() string {
	if g.check == "" {
		return g.text
	}
	criterion := fmt.Sprintf("完成标准：在 %s 中运行 `%s` 退出码为 0。结束前请自己运行它确认。", g.dir, g.check)
	if g.text == "" {
		return criterion
	}
	return g.text + "\n\n" + criterion
}

func (g *goal) followUp(code int, output string) string {
	text := fmt.Sprintf("目标还没有达成：`%s` 退出码 %d，输出末尾：\n\n%s\n\n请找出原因并继续，直到它通过。", g.check, code, tail(output, 60, 8<<10))
	if g.text != "" {
		text += "原目标：" + g.text
	}
	return text
}

// await returns the first finished turn after the given revision. Waiting
// for permission or an answer is the person's to resolve in the agent pane.
func (g *goal) await(after uint64) (rex.SessionInfo, error) {
	deadline := time.Now().Add(g.turnTimeout)
	for {
		s, err := g.status()
		if err != nil {
			return s, err
		}
		if s.Exited {
			return s, fmt.Errorf("agent 已退出（状态 %d）", s.ExitCode)
		}
		finished := s.Agent.State == "completed" || s.Agent.State == "failed"
		if finished && s.Agent.CompletionRevision > after {
			return s, nil
		}
		if s.Agent.State == "waiting" {
			if !g.waiting || s.Agent.WaitRevision != g.waitSeen {
				g.waiting, g.waitSeen = true, s.Agent.WaitRevision
				g.report("blocked", s.Agent.Reason, "agent 在等你处理，请切到 agent 会话")
			}
		} else if g.waiting {
			g.waiting = false
			g.report("working", "", g.progress)
		}
		if time.Now().After(deadline) {
			return s, &ExitError{Code: 124, Cause: errors.New("agent 本轮超时")}
		}
		if err = g.sleep(500 * time.Millisecond); err != nil {
			return s, err
		}
	}
}

// settle waits while a turn the runner did not start runs in the agent: a
// person who gave it a prompt is working with it, and the runner's next
// message waits for that turn to end instead of interleaving with it.
// Keystrokes are no signal of a person: a terminal view attaching to the
// session answers queries and reports focus through the same input.
func (g *goal) settle() error {
	deadline := time.Now().Add(g.turnTimeout)
	reported := false
	for {
		s, err := g.status()
		if err != nil {
			return err
		}
		if s.Exited {
			return fmt.Errorf("agent 已退出（状态 %d）", s.ExitCode)
		}
		if s.Agent.State != "running" {
			if reported {
				g.report("working", "", g.progress)
			}
			return nil
		}
		if !reported {
			reported = true
			g.report("working", "", "agent 正在执行其他指令，结束后继续目标")
			fmt.Fprintln(g.out, "agent 正在执行其他指令，等它结束后继续。")
		}
		if time.Now().After(deadline) {
			return &ExitError{Code: 124, Cause: errors.New("agent 本轮超时")}
		}
		if err = g.sleep(time.Second); err != nil {
			return err
		}
	}
}

// send pastes the text and then presses Enter separately, as a person would:
// bracketed paste keeps its newlines inside the agent's input box.
func (g *goal) send(text string) error {
	if err := g.settle(); err != nil {
		return err
	}
	paste := "\x1b[200~" + plain(text) + "\x1b[201~"
	if _, err := g.call(strings.NewReader(paste), []string{"send", g.agent, "--stdin"}, nil); err != nil {
		return err
	}
	if err := g.sleep(300 * time.Millisecond); err != nil {
		return err
	}
	_, err := g.call(nil, []string{"send", g.agent, "--key", "enter"}, nil)
	return err
}

func (g *goal) runCheck() (string, int, error) {
	fmt.Fprintf(g.out, "检查：%s\n", g.check)
	ctx, cancel := context.WithTimeout(g.ctx, g.checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", g.check)
	cmd.Dir = g.dir
	// Escape sequences would be pasted into the agent's prompt verbatim.
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	data, err := cmd.CombinedOutput()
	output := string(data)
	if text := tail(output, 20, 4<<10); text != "" {
		fmt.Fprintln(g.out, text)
	}
	if ctx.Err() != nil && g.ctx.Err() == nil {
		return output, 0, errors.New("检查超时")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, exit.ExitCode(), nil
	}
	return output, 0, err
}

func (g *goal) status() (rex.SessionInfo, error) {
	var s rex.SessionInfo
	data, err := g.call(nil, []string{"status", g.agent, "--json"}, nil)
	if err == nil {
		err = json.Unmarshal([]byte(data), &s)
	}
	return s, err
}

// report shows the goal on the runner's own pane. Reporting on the agent's
// session would replace its hook state, which the runner itself waits on.
func (g *goal) report(state, kind, message string) {
	if g.self == "" {
		return
	}
	args := []string{"report", g.self, "--state", state, "--id", "goal", "--app", "goal", "--title", g.title, "--message", shorten(message, 300)}
	if state == "blocked" && (kind == "permission" || kind == "question" || kind == "auth") {
		args = append(args, "--kind", kind)
	}
	// Status is presentation only; a failed report must not stop the goal.
	g.call(nil, args, nil)
}

// record keeps an automation's latest run current, so that its next run
// knows whether this one finished and can reuse its agent. Like report, it
// never stops the goal.
func (g *goal) record(state, message string) {
	if g.stateFile == "" {
		return
	}
	automation.UpdateRun(g.stateFile, func(r *automation.Run) {
		r.Agent, r.State, r.Message, r.Updated = g.agent, state, shorten(message, 300), time.Now()
		if r.Runner == "" {
			r.Runner = g.self
		}
	})
}

// finish ends an automation run's record and keeps it in the history.
func (g *goal) finish(state, message string) {
	if g.stateFile == "" {
		return
	}
	automation.FinishRun(g.stateFile, func(r *automation.Run) {
		now := time.Now()
		r.Agent, r.State, r.Message, r.Updated, r.Finished = g.agent, state, shorten(message, 300), now, now
		r.Rounds, r.Summary, r.Result = g.round, shorten(g.summary, 600), automation.ReadResult(g.stateFile)
		if r.Result != nil && r.Result.Summary != "" {
			r.Summary = r.Result.Summary
		}
		if r.Runner == "" {
			r.Runner = g.self
		}
	})
	os.Remove(automation.ResultPath(g.stateFile))
}

func (g *goal) fail(err error) error {
	g.report("error", "", err.Error())
	g.finish("failed", err.Error())
	fmt.Fprintln(g.out, "目标未达成："+err.Error())
	var exit *ExitError
	if errors.As(err, &exit) {
		return err
	}
	return &ExitError{Code: 1, Cause: err}
}

func (g *goal) call(input io.Reader, args, argv []string) (string, error) {
	if input == nil {
		input = strings.NewReader("")
	}
	args = append(slices.Clone(args), g.common...)
	if argv != nil {
		args = append(append(args, "--"), argv...)
	}
	var out bytes.Buffer
	err := Run(g.ctx, args, input, &out, io.Discard, g.options)
	return out.String(), err
}

func (g *goal) sleep(d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-g.ctx.Done():
		return g.ctx.Err()
	}
}

// tail keeps the end of a check's output, where failures are reported.
func tail(s string, lines, size int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	s = strings.Join(parts, "\n")
	if len(s) > size {
		s = strings.ToValidUTF8(s[len(s)-size:], "")
	}
	return s
}

// plain keeps line structure but no control characters, so pasted output
// cannot end the bracketed paste or drive the agent's terminal.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func shorten(s string, n int) string {
	runes := []rune(strings.Join(strings.Fields(s), " "))
	if len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return string(runes)
}

// Executable is this binary at its real path: the CLI link resolves to the
// app, which launchd and automation sessions start.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}
