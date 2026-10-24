//go:build darwin && !ios

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"retty/internal/clilink"
)

func installDesktopCLI(ctx context.Context) error {
	exe, err := cliAppExecutable()
	if err != nil {
		return err
	}
	err = clilink.Install(exe, clilink.SystemPath)
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	// AppleScript quotes the executable argument itself; neither its path nor
	// any user-controlled text is interpolated into shell source here.
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv",
		"-e", `do shell script ((quoted form of item 1 of argv) & " -install-cli") with administrator privileges`,
		"-e", "end run", exe).CombinedOutput()
	if err == nil {
		// Verify the exact link even if authorization completed successfully.
		return clilink.Install(exe, clilink.SystemPath)
	}
	if strings.Contains(string(output), "(-128)") {
		return errCLIInstallCancelled
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("安装 %s 失败：%s（%w）", clilink.SystemPath, strings.TrimSpace(string(output)), err)
}
