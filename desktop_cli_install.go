package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/clilink"
)

var cmdInstallCLI = command{ID: "cli.install", Title: "Install CLI…", Hidden: runtime.GOOS != "darwin", Run: func(a *App) { a.installCLI() }}

var errCLIInstallCancelled = errors.New("CLI installation cancelled")

type desktopCLIInstaller struct {
	busy    bool
	install func(context.Context) error // isolated installer fixture
	result  func(error)                 // isolated dialog fixture
}

func cliAppExecutable() (string, error) {
	if runtime.GOOS != "darwin" || mygo.IsDev() {
		return "", errors.New("请从正式安装的 macOS Retty 应用安装 CLI")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	contents := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(exe) != "Retty" || filepath.Base(filepath.Dir(exe)) != "MacOS" || filepath.Base(contents) != "Contents" || filepath.Ext(filepath.Dir(contents)) != ".app" {
		return "", errors.New("请先将 Retty.app 安装到应用程序目录")
	}
	return exe, nil
}

// This private entry point also runs after macOS administrator authorization.
// It has no configurable destination and never opens the GUI or session daemon.
func installCurrentCLILink() error {
	exe, err := cliAppExecutable()
	if err != nil {
		return err
	}
	return clilink.Install(exe, clilink.SystemPath)
}

func (a *App) installCLI() {
	if a.cliInstaller == nil {
		a.cliInstaller = &desktopCLIInstaller{}
	}
	s := a.cliInstaller
	if s.busy {
		return
	}
	s.busy = true
	install := s.install
	if install == nil {
		install = installDesktopCLI
	}
	show := s.result
	win, update := a.win, a.desktopDispatcher()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err := install(ctx)
		cancel()
		update(func() {
			s.busy = false
			if a.quitting || a.win != win || errors.Is(err, errCLIInstallCancelled) {
				return
			}
			if show != nil {
				show(err)
				return
			}
			message := "已安装 " + clilink.SystemPath + "，现在可在终端运行 retty session help。"
			title, kind := "CLI 安装完成", mygo.MessageInfo
			if err != nil {
				title, kind, message = "CLI 安装失败", mygo.MessageError, fmt.Sprint(err)
			}
			go mygo.Dialog.Message(mygo.MessageOptions{Parent: win, Type: kind, Title: title, Message: message})
		})
	}()
}
