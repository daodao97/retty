//go:build !darwin || ios

package main

import (
	"context"
	"errors"
)

func installDesktopCLI(context.Context) error {
	return errors.New("安装 CLI 目前仅支持 macOS 桌面应用")
}
