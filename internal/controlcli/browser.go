package controlcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"retty/internal/atomicfile"
	"retty/internal/browsercontrol"
	"retty/internal/desktopcontrol"
	"retty/internal/rex"
)

const browserUsage = `Retty browser panes (desktop GUI required):
  retty browser list [--json]
  retty browser open URL [--beside SESSION_OR_BROWSER_ID] [--host HOST_ID] [--direction right|down] [--json]
  retty browser status ID [--json]
  retty browser navigate ID URL [--json]
  retty browser read ID [--json]
  retty browser click ID --selector CSS [--json]
  retty browser type ID --selector CSS --text TEXT [--json]
  retty browser wait ID [--selector CSS] [--text TEXT] [--timeout 10s] [--json]
  retty browser screenshot ID --output PATH [--json]
  retty browser close ID [--json]

Use --data-dir PATH for the owning desktop. list includes remote host IDs.
Browser IDs persist with the layout. Read before selecting a unique CSS target.
click/type run once, with no retry on errors. close only closes a browser pane.
Remote browser loopback URLs use the selected host's forwarded dev port.
`

func runBrowser(ctx context.Context, args []string, out, diag io.Writer, options Options) (runErr error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := io.WriteString(out, browserUsage)
		return err
	}
	req := browsercontrol.Request{Command: args[0]}
	f := flag.NewFlagSet("browser "+req.Command, flag.ContinueOnError)
	f.SetOutput(diag)
	dir, output := "", ""
	asJSON := false
	timeout := 10 * time.Second
	f.StringVar(&dir, "data-dir", "", "owning desktop data directory")
	f.StringVar(&req.ID, "target", "", "browser ID")
	f.StringVar(&req.Beside, "beside", "", "split beside a stable terminal/browser ID")
	f.StringVar(&req.Host, "host", "", "remote host ID")
	f.StringVar(&req.URL, "url", "", "URL")
	f.StringVar(&req.Direction, "direction", "right", "right or down")
	f.StringVar(&req.Selector, "selector", "", "unique CSS selector")
	f.StringVar(&req.Text, "text", "", "input or expected text")
	f.StringVar(&output, "output", "", "screenshot file")
	f.DurationVar(&timeout, "timeout", 10*time.Second, "wait timeout, maximum 2m")
	f.BoolVar(&asJSON, "json", false, "JSON output")
	var flags, operands []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			_, err := io.WriteString(out, browserUsage)
			return err
		}
		if !strings.HasPrefix(arg, "-") {
			operands = append(operands, arg)
			continue
		}
		name, _, equal := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		opt := f.Lookup(name)
		if opt == nil {
			return fmt.Errorf("unknown browser flag %s", arg)
		}
		flags = append(flags, arg)
		b, isBool := opt.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(isBool && b.IsBoolFlag()) {
			i++
			if i == len(args) {
				return fmt.Errorf("%s requires a value", arg)
			}
			flags = append(flags, args[i])
		}
	}
	if err := f.Parse(flags); err != nil {
		return err
	}
	if req.Command == "open" {
		if len(operands) > 1 || len(operands) == 1 && req.URL != "" {
			return errors.New("open takes one URL")
		}
		if len(operands) == 1 {
			req.URL = operands[0]
		}
	}
	if req.Command != "list" && req.Command != "open" {
		if len(operands) > 0 && req.ID == "" {
			req.ID = operands[0]
			operands = operands[1:]
		}
		if req.Command == "navigate" && len(operands) == 1 && req.URL == "" {
			req.URL = operands[0]
			operands = nil
		}
		if len(operands) > 0 {
			return errors.New("unexpected browser arguments")
		}
	}
	if req.Command == "list" && len(operands) > 0 {
		return errors.New("list takes no target")
	}
	if timeout < time.Millisecond || timeout > 2*time.Minute {
		return errors.New("browser timeout must be >=1ms and <=2m")
	}
	req.TimeoutMS = timeout.Milliseconds()
	if err := req.Validate(); err != nil {
		return err
	}
	if req.Command == "screenshot" && output == "" {
		return errors.New("screenshot requires --output PATH")
	}
	explicitDir := dir != ""
	if dir == "" {
		dir = os.Getenv("RETTY_DIR")
	}
	if dir == "" {
		dir = options.DefaultDir
	}
	if dir == "" {
		dir = rex.Dir()
	}
	if os.Getenv("RETTY_DIR") == "" && !explicitDir {
		socket := os.Getenv("RETTY_AGENT_SOCKET")
		if filepath.Base(socket) == "server.sock" {
			dir = filepath.Dir(socket)
		} else if socket != "" {
			return errors.New("hashed agent socket requires --data-dir or RETTY_DIR for the owning desktop")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if req.Command == "wait" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		defer func() {
			if runErr != nil && ctx.Err() != nil {
				runErr = waitContext(ctx.Err())
			}
		}()
	}
	res, err := desktopcontrol.Call(ctx, abs, desktopcontrol.Request{Op: "browser", Browser: &req})
	if err != nil {
		return err
	}
	if res.Browser == nil {
		return errors.New("desktop did not return a browser response")
	}
	result := res.Browser
	if req.Command == "screenshot" {
		path, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		if len(result.PNG) == 0 {
			return errors.New("empty browser screenshot")
		}
		if err = atomicfile.Write(path, result.PNG); err != nil {
			return err
		}
		result.PNG = nil
		if asJSON {
			return json.NewEncoder(out).Encode(struct {
				Path    string               `json:"path"`
				Browser *browsercontrol.Info `json:"browser"`
			}{path, result.Browser})
		}
		_, err = fmt.Fprintln(out, path)
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	switch req.Command {
	case "list":
		for _, b := range result.Browsers {
			fmt.Fprintf(out, "%s\t%s\t%s\n", b.ID, clean(b.URL), clean(b.Title))
		}
		for _, h := range result.Hosts {
			fmt.Fprintf(out, "host:%s\t%s\tconnected=%t\n", h.ID, clean(h.Name), h.Connected)
		}
		return nil
	case "read":
		if result.Document != nil {
			_, err = fmt.Fprintln(out, strings.Map(func(r rune) rune {
				if unicode.IsControl(r) && r != '\n' && r != '\t' {
					return -1
				}
				return r
			}, result.Document.Text))
			return err
		}
	case "open", "status", "navigate":
		if result.Browser != nil {
			_, err = fmt.Fprintf(out, "%s\t%s\n", result.Browser.ID, clean(result.Browser.URL))
			return err
		}
	}
	_, err = fmt.Fprintln(out, "ok")
	return err
}
