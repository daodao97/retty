//go:build !darwin && !linux

package agentusage

import "os/exec"

func isolateReader(cmd *exec.Cmd) {}
