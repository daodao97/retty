// Package controlcli implements the same automation commands in the desktop
// executable and the headless CLI. It never starts or replaces a session server.
package controlcli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"retty/internal/desktopcontrol"
	"retty/internal/rex"
	"retty/internal/terminal/screen"
)

const Usage = `Retty session automation (never starts/restarts the server):
  retty list [--json]
  retty session status [ID] [--json]
  retty read [ID] [--lines 200] [--json | --ansi]
  retty new [--dir PATH] [--cols 80 --rows 24] [--json] [-- COMMAND ARG...]
  retty split [ID] [--direction right|down] [--dir PATH] [--json] [-- COMMAND ARG...]
  retty send [ID] (--text TEXT | --stdin | --key ctrl-c) [--enter] [--if-output N] [--json]
  retty wait [ID] [--until exit|idle|done|blocked] [--timeout 5m] [--exit] [--json]
  retty report [ID] --state working|blocked|done|error|idle|clear
       [--id build/tests] [--app builder] [--title TEXT] [--message TEXT]
       [--progress 0..100] [--kind permission|question|auth] [--json]
  retty actions [--json]
  retty action ACTION_ID [--target ID] [--json]
  retty browser help
  retty skill [--json]

IDs are stable server session IDs, or unique prefixes (at least 4 characters).
Omitting ID uses RETTY_SESSION; no command guesses the focused terminal.
Use --socket PATH or --data-dir PATH to select a server. Inside a Retty terminal,
the injected RETTY_AGENT_SOCKET selects its owning server. split/action require
the desktop GUI in that data directory. Flags may follow ID; -- preserves argv.
Read first. send writes once and never retries; on a connection error read again.
wait --until exit --exit returns the command's real exit code (timeout: 124).
new -- COMMAND starts that process directly; sending a shell line does not give
Retty its exit status. Existing bare 'retty status' keeps the gateway status.
`

//go:embed skills/retty-control/SKILL.md
var agentSkill string

type ExitError struct {
	Code  int
	Cause error
}

func (e *ExitError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return fmt.Sprintf("command exited with status %d", e.Code)
}
func (e *ExitError) Unwrap() error { return e.Cause }
func ExitCode(err error) int {
	var e *ExitError
	if errors.As(err, &e) {
		if e.Code < 0 || e.Code > 255 {
			return 1
		}
		return e.Code
	}
	return 1
}

type Options struct {
	// DefaultDir distinguishes the headless CLI's RettyServer from GUI Retty.
	DefaultDir string
	// Dial is for isolated transports/tests; nil connects to the selected socket.
	Dial func(context.Context, string) (net.Conn, error)
}

func Handles(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "session", "list", "read", "new", "split", "send", "wait", "report", "actions", "action", "skill":
		return true
	case "browser":
		return true
	case "status":
		// Preserve the gateway's original 'status [--data-dir PATH]' command.
		for i := 1; i < len(args); i++ {
			a := args[i]
			if a == "--data-dir" {
				i++
				continue
			}
			if strings.HasPrefix(a, "--data-dir=") {
				continue
			}
			return true
		}
	}
	return false
}

type arguments struct {
	command, target, socket, dataDir, dir, direction, text, key, ifOutput, until string
	json, ansi, stdin, enter, exit                                               bool
	lines, cols, rows                                                            int
	timeout                                                                      time.Duration
	status                                                                       rex.ProgramStatus
	progress                                                                     string
	argv                                                                         []string
}

func parse(args []string, diagnostics io.Writer) (a arguments, err error) {
	if len(args) > 0 && args[0] == "session" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "help" {
		return a, flag.ErrHelp
	}
	a.command = args[0]
	f := flag.NewFlagSet(a.command, flag.ContinueOnError)
	f.SetOutput(diagnostics)
	f.StringVar(&a.target, "target", "", "stable session ID or unique prefix")
	f.StringVar(&a.socket, "socket", "", "session socket path")
	f.StringVar(&a.dataDir, "data-dir", "", "owning service/GUI data directory")
	f.BoolVar(&a.json, "json", false, "JSON output")
	switch a.command {
	case "list", "status", "actions", "action", "skill":
	case "read":
		f.IntVar(&a.lines, "lines", 200, "history rows, 1..8192")
		f.BoolVar(&a.ansi, "ansi", false, "serialized VT snapshot")
	case "new", "split":
		f.StringVar(&a.dir, "dir", "", "working directory")
		f.IntVar(&a.cols, "cols", 80, "initial columns")
		f.IntVar(&a.rows, "rows", 24, "initial rows")
		if a.command == "split" {
			f.StringVar(&a.direction, "direction", "right", "right or down")
		}
	case "send":
		f.StringVar(&a.text, "text", "", "exact input text")
		f.BoolVar(&a.stdin, "stdin", false, "read at most 64 KiB from stdin")
		f.StringVar(&a.key, "key", "", "enter, tab, escape, ctrl-c or ctrl-d")
		f.BoolVar(&a.enter, "enter", false, "append one carriage return")
		f.StringVar(&a.ifOutput, "if-output", "", "reject if output has changed since read --json")
	case "wait":
		f.DurationVar(&a.timeout, "timeout", 5*time.Minute, "deadline (maximum 24h)")
		f.StringVar(&a.until, "until", "exit", "exit, idle, done or blocked")
		f.BoolVar(&a.exit, "exit", false, "propagate the process exit status")
	case "report":
		f.StringVar(&a.status.State, "state", "", "program state")
		f.StringVar(&a.status.ID, "id", "", "hierarchical task ID")
		f.StringVar(&a.status.App, "app", "", "program name")
		f.StringVar(&a.status.Title, "title", "", "short label")
		f.StringVar(&a.status.Message, "message", "", "status message")
		f.StringVar(&a.status.Kind, "kind", "", "blocked reason")
		f.StringVar(&a.progress, "progress", "", "0..100")
	default:
		return a, fmt.Errorf("unknown session command %q", a.command)
	}
	// flag.FlagSet normally stops at ID. Move only recognized option/value pairs,
	// leaving everything beyond -- byte-for-byte as command argv.
	var flags, positionals []string
	for i := 1; i < len(args); i++ {
		v := args[i]
		if v == "--" {
			a.argv = append([]string(nil), args[i+1:]...)
			break
		}
		if !strings.HasPrefix(v, "-") || v == "-" {
			positionals = append(positionals, v)
			continue
		}
		if v == "-h" || v == "--help" {
			return a, flag.ErrHelp
		}
		name, _, equal := strings.Cut(strings.TrimLeft(v, "-"), "=")
		opt := f.Lookup(name)
		if opt == nil {
			return a, fmt.Errorf("unknown flag %s", v)
		}
		flags = append(flags, v)
		b, isBool := opt.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(isBool && b.IsBoolFlag()) {
			i++
			if i == len(args) {
				return a, fmt.Errorf("%s requires a value", v)
			}
			flags = append(flags, args[i])
		}
	}
	if err = f.Parse(flags); err != nil {
		return
	}
	if a.command == "action" {
		if len(positionals) != 1 {
			return a, errors.New("action requires one stable action ID")
		}
		a.argv = positionals
	} else if len(positionals) > 0 {
		if len(positionals) != 1 || a.target != "" || a.command == "list" || a.command == "new" || a.command == "actions" || a.command == "skill" {
			return a, errors.New("unexpected positional arguments")
		}
		a.target = positionals[0]
	}
	if len(a.argv) > 0 && a.command != "new" && a.command != "split" && a.command != "action" {
		return a, errors.New("-- COMMAND is only valid for new/split")
	}
	if a.json && a.ansi {
		return a, errors.New("choose --json or --ansi")
	}
	return
}

// Resolve accepts exact IDs first, then unique prefixes, never fuzzy titles or
// array indexes. Current means the caller's injected session, not GUI focus.
func Resolve(all []rex.SessionInfo, target string) (rex.SessionInfo, error) {
	if target == "" {
		return rex.SessionInfo{}, errors.New("specify a session ID (list --json); RETTY_SESSION is not set")
	}
	for _, s := range all {
		if s.ID == target {
			return s, nil
		}
	}
	if len(target) < 4 {
		return rex.SessionInfo{}, errors.New("session prefixes must contain at least 4 characters")
	}
	var matches []rex.SessionInfo
	for _, s := range all {
		if strings.HasPrefix(s.ID, target) {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		return rex.SessionInfo{}, fmt.Errorf("no session matches %q", target)
	}
	if len(matches) != 1 {
		return rex.SessionInfo{}, fmt.Errorf("ambiguous session prefix %q; use the full ID", target)
	}
	return matches[0], nil
}

func Run(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, options Options) (runErr error) {
	if len(args) > 0 && args[0] == "browser" {
		return runBrowser(ctx, args[1:], output, diagnostics, options)
	}
	a, err := parse(args, diagnostics)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(output, Usage)
		return err
	}
	if err != nil {
		return err
	}
	if a.command == "skill" {
		if a.json {
			return json.NewEncoder(output).Encode(struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			}{"retty-control", agentSkill})
		}
		_, err = io.WriteString(output, agentSkill)
		return err
	}
	if a.target == "" {
		a.target = os.Getenv("RETTY_SESSION")
	}
	dir := a.dataDir
	if dir == "" {
		dir = os.Getenv("RETTY_DIR")
	}
	if dir == "" {
		dir = options.DefaultDir
	}
	if dir == "" {
		dir = rex.Dir()
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	socket := a.socket
	if socket == "" && a.dataDir == "" {
		socket = os.Getenv("RETTY_AGENT_SOCKET")
	}
	if socket == "" {
		socket = rex.SocketPathForDir(dir)
	}
	if a.dataDir == "" && (a.socket != "" || os.Getenv("RETTY_AGENT_SOCKET") != "") && filepath.Base(socket) == "server.sock" {
		// An explicit ordinary socket carries its desktop namespace too.
		dir = filepath.Dir(socket)
	}
	if (a.command == "split" || a.command == "actions" || a.command == "action") && a.dataDir == "" && filepath.Base(socket) != "server.sock" && (a.socket != "" || os.Getenv("RETTY_DIR") == "") {
		return errors.New("GUI operations with a hashed session socket require --data-dir for its owning desktop")
	}
	if a.command == "actions" || a.command == "action" {
		req := desktopcontrol.Request{Op: a.command, SID: a.target}
		if a.command == "action" {
			req.Action = a.argv[0]
		}
		result, err := desktopcontrol.Call(ctx, dir, req)
		if err != nil {
			return err
		}
		if a.json {
			return json.NewEncoder(output).Encode(result)
		}
		if a.command == "actions" {
			for _, action := range result.Actions {
				fmt.Fprintf(output, "%s\t%s\tcli=%t\n", action.ID, clean(action.Title), action.Allowed)
			}
			return nil
		}
		_, err = fmt.Fprintln(output, "ok")
		return err
	}
	if a.command == "wait" {
		if a.timeout <= 0 || a.timeout > 24*time.Hour {
			return errors.New("timeout must be >0 and <=24h")
		}
		if a.until != "exit" && a.until != "idle" && a.until != "done" && a.until != "blocked" {
			return errors.New("until must be exit, idle, done or blocked")
		}
		if a.exit && a.until != "exit" {
			return errors.New("--exit requires --until exit")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
		defer func() {
			if runErr != nil && ctx.Err() != nil {
				runErr = waitContext(ctx.Err())
			}
		}()
	}
	dial := options.Dial
	if dial == nil {
		dial = func(ctx context.Context, path string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
		}
	}
	c, err := rex.ConnectDial(ctx, func(ctx context.Context) (net.Conn, error) { return dial(ctx, socket) })
	if err != nil {
		return fmt.Errorf("session server unavailable (no service was started): %w", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	encode := func(v any) error { return json.NewEncoder(output).Encode(v) }
	if a.command == "new" {
		if a.dir == "" {
			a.dir, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		o, err := a.createOptions()
		if err != nil {
			return err
		}
		s, err := c.Create(o)
		if err != nil {
			return err
		}
		if a.json {
			return encode(s)
		}
		_, err = fmt.Fprintln(output, s.ID)
		return err
	}
	all, err := c.List()
	if err != nil {
		return err
	}
	if a.command == "list" || a.command == "status" && a.target == "" {
		if all == nil {
			all = []rex.SessionInfo{}
		}
		if a.json {
			return encode(all)
		}
		w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		for _, s := range all {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, state(s), clean(s.Program), clean(s.Dir))
		}
		return w.Flush()
	}
	s, err := Resolve(all, a.target)
	if err != nil {
		return err
	}
	switch a.command {
	case "status":
		if a.json {
			return encode(s)
		}
		_, err = fmt.Fprintf(output, "%s  %s  %dx%d  %s\n", s.ID, state(s), s.Cols, s.Rows, clean(s.Dir))
		return err
	case "read":
		snap, err := c.History(ctx, s.ID, a.lines)
		if err != nil {
			return err
		}
		if a.ansi {
			_, err = output.Write(snap.Data)
			return err
		}
		text, err := snapshotText(snap)
		if err != nil {
			return err
		}
		if a.json {
			return encode(struct {
				ID       string `json:"id"`
				Text     string `json:"text"`
				Output   uint64 `json:"output"`
				Revision uint64 `json:"revision"`
				Cols     int    `json:"cols"`
				Rows     int    `json:"rows"`
			}{s.ID, text, snap.Output, snap.Revision, snap.Cols, snap.Rows})
		}
		_, err = fmt.Fprintln(output, text)
		return err
	case "split":
		if a.direction != "right" && a.direction != "down" {
			return errors.New("direction must be right or down")
		}
		o, err := a.createOptions()
		if err != nil {
			return err
		}
		result, err := desktopcontrol.Call(ctx, dir, desktopcontrol.Request{Op: "split", SID: s.ID, Direction: a.direction, Create: &o})
		if err != nil {
			return err
		}
		if a.json {
			return encode(result)
		}
		_, err = fmt.Fprintln(output, result.SID)
		return err
	case "send", "report":
		hello, err := c.Hello()
		if err != nil {
			return err
		}
		if !hello.SessionControl {
			return errors.New("existing session server lacks input/status automation; preserve running tasks and update the server after they finish")
		}
		if a.command == "send" {
			data, err := a.input(input)
			if err != nil {
				return err
			}
			req := rex.InputRequest{Data: data}
			if a.ifOutput != "" {
				n, err := strconv.ParseUint(a.ifOutput, 10, 64)
				if err != nil {
					return err
				}
				req.IfOutput = &n
			}
			if err = c.SendInput(s.ID, req); err != nil {
				return fmt.Errorf("send was not retried; read the session before another attempt: %w", err)
			}
		} else {
			if a.progress != "" {
				n, err := strconv.Atoi(a.progress)
				if err != nil {
					return err
				}
				a.status.Progress = &n
			}
			if err = c.ReportProgram(s.ID, a.status); err != nil {
				return err
			}
		}
		if a.json {
			return encode(struct {
				ID string `json:"id"`
				OK bool   `json:"ok"`
			}{s.ID, true})
		}
		_, err = fmt.Fprintln(output, "ok")
		return err
	case "wait":
		return wait(ctx, c, s.ID, a, output)
	}
	return fmt.Errorf("unknown command %q", a.command)
}

func (a arguments) createOptions() (rex.CreateOptions, error) {
	if a.cols < 2 || a.cols > 1000 || a.rows < 2 || a.rows > 1000 {
		return rex.CreateOptions{}, errors.New("columns/rows must be between 2 and 1000")
	}
	dir := a.dir
	if dir != "" {
		var err error
		dir, err = filepath.Abs(dir)
		if err != nil {
			return rex.CreateOptions{}, err
		}
	}
	return rex.CreateOptions{Dir: dir, Command: a.argv, Cols: a.cols, Rows: a.rows}, nil
}

func (a arguments) input(in io.Reader) ([]byte, error) {
	n := 0
	if a.text != "" {
		n++
	}
	if a.stdin {
		n++
	}
	if a.key != "" {
		n++
	}
	if n > 1 {
		return nil, errors.New("choose --text, --stdin or --key")
	}
	data := []byte(a.text)
	if a.stdin {
		var err error
		data, err = io.ReadAll(io.LimitReader(in, rex.MaxControlInput+1))
		if err != nil {
			return nil, err
		}
	}
	if a.key != "" {
		keys := map[string]string{"enter": "\r", "tab": "\t", "escape": "\x1b", "ctrl-c": "\x03", "ctrl-d": "\x04"}
		value, ok := keys[a.key]
		if !ok {
			return nil, errors.New("unsupported key; use enter, tab, escape, ctrl-c or ctrl-d")
		}
		data = []byte(value)
	}
	if a.enter {
		data = append(data, '\r')
	}
	if len(data) == 0 || len(data) > rex.MaxControlInput {
		return nil, errors.New("input must contain 1..65536 bytes")
	}
	return data, nil
}

func snapshotText(s rex.HistorySnapshot) (string, error) {
	if s.Cols < 1 || s.Cols > 1000 || s.Rows < 1 || s.Rows > 1000 {
		return "", errors.New("invalid terminal dimensions")
	}
	vt, err := screen.New(s.Cols, s.Rows, 8<<20, nil)
	if err != nil {
		return "", err
	}
	defer vt.Close()
	vt.Feed(s.Data)
	return strings.TrimRight(vt.Text(), "\n"), nil
}

func state(s rex.SessionInfo) string {
	if s.Exited {
		return "exited:" + strconv.Itoa(s.ExitCode)
	}
	if s.Agent.State != "" {
		return s.Agent.State
	}
	if s.Idle {
		return "idle"
	}
	return "running"
}
func clean(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, v)
}

func wait(ctx context.Context, c *rex.Client, sid string, a arguments, out io.Writer) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return waitContext(err)
		}
		all, err := c.List()
		if err != nil {
			if ctx.Err() != nil {
				return waitContext(ctx.Err())
			}
			return err
		}
		s, err := Resolve(all, sid)
		if err != nil {
			return err
		}
		ready := s.Exited
		switch a.until {
		case "idle":
			ready = ready || s.Idle
		case "done":
			ready = ready || s.Agent.State == "completed" || s.Agent.State == "failed"
		case "blocked":
			ready = ready || s.Agent.State == "waiting"
		}
		if ready {
			if a.json {
				err = json.NewEncoder(out).Encode(s)
			} else {
				_, err = fmt.Fprintf(out, "%s  %s\n", s.ID, state(s))
			}
			if err != nil {
				return err
			}
			if a.exit && s.ExitCode != 0 {
				return &ExitError{Code: s.ExitCode}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return waitContext(ctx.Err())
		case <-ticker.C:
		}
	}
}
func waitContext(err error) error {
	code := 130
	if errors.Is(err, context.DeadlineExceeded) {
		code = 124
	}
	return &ExitError{Code: code, Cause: err}
}
