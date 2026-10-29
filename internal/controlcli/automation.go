package controlcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

// automationView is one automation as list --json reports it.
type automationView struct {
	automation.Automation
	Next time.Time      `json:"next,omitzero"`
	Last automation.Run `json:"last"`
}

// runAutomation edits the schedule and starts runs. Like the session
// commands it never starts a server; the desktop's minute timer does that
// only for a due run.
func runAutomation(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, options Options) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(output, Usage)
		return err
	}
	command, args := args[0], args[1:]
	var a automation.Automation
	var dataDir, socket string
	var asJSON bool
	var runFile string
	f := flag.NewFlagSet("automation "+command, flag.ContinueOnError)
	f.SetOutput(diagnostics)
	f.StringVar(&dataDir, "data-dir", "", "owning service/GUI data directory")
	switch command {
	case "list":
		f.BoolVar(&asJSON, "json", false, "JSON output")
	case "add":
		f.StringVar(&a.Title, "title", "", "short title the task board lists")
		f.StringVar(&a.Goal, "goal", "", "the goal, sent as the agent's instruction")
		f.StringVar(&a.Check, "check", "", "shell command whose exit status 0 means the goal is met")
		f.StringVar(&a.Dir, "dir", "", "working directory")
		f.StringVar(&a.Every, "every", "", "interval, as 30m, 2h or 24h")
		f.BoolVar(&a.Once, "once", false, "run a single time, at once, instead of every interval")
		f.IntVar(&a.Rounds, "rounds", 8, "maximum agent turns per run")
		f.BoolVar(&asJSON, "json", false, "JSON output")
	case "result":
		f.StringVar(&runFile, "run", "", "the run record the task's instruction names")
	case "pause", "resume", "remove":
	case "run", "tick":
		f.StringVar(&socket, "socket", "", "session socket path")
	default:
		return fmt.Errorf("unknown automation command %q", command)
	}
	if i := slices.Index(args, "--"); i >= 0 {
		if command != "add" {
			return errors.New("-- AGENT is only valid for add")
		}
		args, a.Agent = args[:i], slices.Clone(args[i+1:])
	}
	// Flags may come before or after the ID, as with the session commands;
	// flag.FlagSet alone would stop at the ID and take later flags as values.
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		v := args[i]
		if !strings.HasPrefix(v, "-") || v == "-" {
			positionals = append(positionals, v)
			continue
		}
		flags = append(flags, v)
		name, _, equal := strings.Cut(strings.TrimLeft(v, "-"), "=")
		opt := f.Lookup(name)
		if opt == nil {
			continue // Parse reports it.
		}
		b, isBool := opt.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(isBool && b.IsBoolFlag()) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := f.Parse(flags); err != nil {
		return err
	}
	if len(positionals) > 1 {
		return errors.New("unexpected positional arguments")
	}
	var id string
	if len(positionals) == 1 {
		id = positionals[0]
	}
	needsID := command == "pause" || command == "resume" || command == "remove" || command == "run"
	if needsID != (id != "") {
		if needsID {
			return fmt.Errorf("automation %s requires an automation ID (automation list)", command)
		}
		return errors.New("unexpected positional arguments")
	}
	if command == "result" {
		return submitResult(input, output, runFile)
	}
	dir := dataDir
	if dir == "" {
		dir = os.Getenv("RETTY_DIR")
	}
	if dir == "" {
		dir = options.DefaultDir
	}
	if dir == "" {
		dir = rex.Dir()
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	exe, err := Executable()
	if err != nil {
		return err
	}
	if options.Executable != "" {
		exe = options.Executable
	}
	sync := func() error {
		if options.SyncAutomations != nil {
			return options.SyncAutomations(dir)
		}
		all, err := automation.List(dir)
		if err == nil && slices.ContainsFunc(all, func(a automation.Automation) bool { return a.Enabled }) {
			fmt.Fprintln(diagnostics, automation.ErrNoTimer)
		}
		return err
	}
	switch command {
	case "list":
		all, err := automation.List(dir)
		if err != nil {
			return err
		}
		now := time.Now()
		views := make([]automationView, 0, len(all))
		for _, a := range all {
			v := automationView{Automation: a, Last: automation.LastRun(dir, a.ID)}
			if a.Enabled {
				v.Next = a.Next(v.Last, now)
			}
			views = append(views, v)
		}
		if asJSON {
			return json.NewEncoder(output).Encode(views)
		}
		w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		for _, v := range views {
			next := "已暂停"
			switch {
			case v.Enabled && v.Next.IsZero():
				next = "已运行"
			case v.Enabled:
				next = "下次 " + v.Next.Local().Format("01-02 15:04")
			}
			every := "每 " + v.Every
			if v.Once {
				every = "一次性"
			}
			last := v.Last.State
			if v.Last.Message != "" {
				last += "：" + v.Last.Message
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.ID, every, next, clean(shorten(v.Name(), 40)), clean(shorten(last, 60)))
		}
		return w.Flush()
	case "add":
		if a.Dir == "" {
			if a.Dir, err = os.Getwd(); err != nil {
				return err
			}
		}
		if a.Dir, err = filepath.Abs(a.Dir); err != nil {
			return err
		}
		if a, err = automation.New(a, time.Now()); err != nil {
			return err
		}
		if err = automation.Save(dir, a); err != nil {
			return err
		}
		if err = sync(); err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(output).Encode(a)
		}
		_, err = fmt.Fprintln(output, a.ID)
		return err
	case "pause", "resume", "remove":
		a, err := automation.Find(dir, id)
		if err != nil {
			return err
		}
		if command == "remove" {
			err = automation.Remove(dir, a.ID)
		} else {
			a.Enabled = command == "resume"
			err = automation.Save(dir, a)
		}
		if err != nil {
			return err
		}
		if err = sync(); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "ok")
		return err
	}
	// run and tick start runner sessions on an existing server.
	if id != "" {
		a, err := automation.Find(dir, id)
		if err != nil {
			return err
		}
		id = a.ID
	}
	if socket == "" && dataDir == "" {
		socket = os.Getenv("RETTY_AGENT_SOCKET")
	}
	if socket == "" {
		socket = rex.SocketPathForDir(dir)
	}
	dial := options.Dial
	if dial == nil {
		dial = func(ctx context.Context, path string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
		}
	}
	connect := func() (*rex.Client, error) {
		c, err := rex.ConnectDial(ctx, func(ctx context.Context) (net.Conn, error) { return dial(ctx, socket) })
		if err != nil {
			return nil, &ExitError{Code: ExitUnavailable, Cause: fmt.Errorf("session server unavailable (no service was started): %w", err)}
		}
		return c, nil
	}
	if err = automation.Tick(dir, exe, connect, id, time.Now()); err != nil {
		return err
	}
	if command == "run" {
		_, err = fmt.Fprintln(output, "ok")
	}
	return err
}

// submitResult takes a run's structured result from its agent, which runs
// in the run's own session. The runner keeps it with the run as it ends.
func submitResult(input io.Reader, output io.Writer, runFile string) error {
	if runFile == "" {
		return errors.New("automation result requires --run, the run record the task's instruction names")
	}
	if !automation.IsRunPath(runFile) {
		return fmt.Errorf("%s is not an automation run record", runFile)
	}
	if _, err := os.Stat(runFile); err != nil {
		return fmt.Errorf("no automation run record at %s", runFile)
	}
	data, err := io.ReadAll(io.LimitReader(input, automation.MaxResult+1))
	if err != nil {
		return err
	}
	r, err := automation.ParseResult(data)
	if err != nil {
		return err
	}
	if r.Empty() {
		return errors.New("结果为空")
	}
	if err = automation.SaveResult(runFile, r); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "ok · %d 个指标 · %d 项发现\n", len(r.Metrics), len(r.Findings))
	return err
}
