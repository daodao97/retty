//go:build darwin || linux

package agentusage

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// npm's Codex launcher spawns the native CLI. Own a separate process group,
// so cancellation cannot leave its child behind or touch a shared daemon.
func isolateReader(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
