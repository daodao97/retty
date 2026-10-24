// Retty is a terminal of tabs and split panes, after Superlogical's Rex,
// in MyGo's native UI. Its shells run in a session server of their own,
// so that they outlive the app: quit it, open it again, and every tab and
// pane is back as it was, its programs still running.
//
//	go tool mygo dev    the app, rebuilt as the code changes
//	go run . -server    the session server alone, in the foreground
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"

	"retty/internal/agents"
	"retty/internal/controlcli"
	"retty/internal/push"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/runtimeinfo"
)

func main() {
	runtimeinfo.Version = mygo.App.Version()
	if runtime.GOOS == "ios" {
		mobileMain()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "-install-cli" {
		if err := installCurrentCLILink(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "-push-service" {
		if err := push.Run(os.Args[2], os.Args[3]); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 6 && os.Args[1] == "-push-import" {
		if err := push.Import(rex.Dir(), os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		fmt.Println("APNs 配置已保存")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "-push-status" {
		status, err := push.Query(rex.Dir())
		if err != nil {
			log.Print("无法读取任务通知服务状态")
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(status)
		return
	}
	// Hook helpers never initialize AppKit. Only an explicit Claude permission
	// request with a foreground phone viewer may wait for a structured reply.
	if len(os.Args) == 3 && os.Args[1] == "-agent-hook" {
		rex.RunAgentHook(os.Args[2], os.Stdin)
		return
	}
	if len(os.Args) == 5 && (os.Args[1] == "-codex-bridge" || os.Args[1] == "-codex-proxy") {
		pid, err := rex.ParseBridgePID(os.Args[3])
		if err == nil {
			if os.Args[1] == "-codex-bridge" {
				err = rex.StartCodexBridge(os.Args[2], pid, os.Args[4])
			} else {
				err = rex.RunCodexProxy(os.Args[2], pid, os.Args[4])
			}
		}
		if err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "-codex-watch" {
		pid, err := rex.ParseBridgePID(os.Args[3])
		if err == nil {
			err = rex.WatchCodexThread(os.Args[2], pid)
		}
		if err != nil {
			os.Exit(1)
		}
		return
	}
	// -server runs the session server; other arguments are AppKit's, as
	// -NSWindowResizeTime, which it reads itself.
	if len(os.Args) > 1 && os.Args[1] == "-server" {
		log.SetPrefix("[server] ")
		if err := rex.Serve(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if controlcli.Handles(os.Args[1:]) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := controlcli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, controlcli.Options{})
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, "retty:", err)
			os.Exit(controlcli.ExitCode(err))
		}
		return
	}
	// The state of the sessions lives in the app's data directory, which
	// the server it starts takes from RETTY_DIR: "Retty", or "Retty Dev"
	// under mygo dev, which keeps the installed app's sessions apart.
	performanceRun := configurePerformanceFixture()
	if os.Getenv("RETTY_DIR") == "" {
		if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
			os.Setenv("RETTY_DIR", dir)
		}
	}
	registerFonts()
	loadSettings()
	// Only the desktop GUI owns updates. Daemons, hooks and iOS return above.
	if !performanceRun {
		if err := agents.UpgradeApprovalHook(); err != nil {
			log.Print("无法更新 Retty Claude 授权 Hook：", err)
		}
		mygo.Updater.SetKeepPreviousBundles(true)
		mygo.Use(updatenative.Plugin)
	}
	a := &App{}
	mygo.App.SetMenu(a.menu())
	mygo.App.WhenReady(a.open)
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows && a.win == nil {
			a.open()
		}
	})
	mygo.App.OnWindowAllClosed(func() {
		if runtime.GOOS != "darwin" {
			mygo.App.Quit()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		a.saveDesktopHistory()
		a.flushDesktopHistory()
		a.saveNow()
		a.stopPhonePair()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// open opens the window, with the tabs the server kept.
func (a *App) open() {
	a.loadReadReceipts()
	a.openWindow = a.open
	client, err := rex.Connect()
	if err != nil {
		log.Print(err)
		mygo.Dialog.Error("Retty could not start its session server", err.Error())
		mygo.App.Quit()
		return
	}
	a.client = client
	go func() {
		if err := push.Ensure(rex.Dir(), rex.SocketPath()); err != nil {
			log.Print("无法启动任务通知服务")
		}
	}()
	a.hello, err = client.Hello()
	if err == nil && mygo.IsDev() && os.Getenv("MYGO_READY_SOCKET") != "" && staleServer(a.hello) {
		// Only explicit mygo dev hot reload may replace its development
		// server. A manually built or installed app attaches to existing
		// sessions instead of ending them when its executable changes.
		if client, err = client.Restart(a.hello.PID); err == nil {
			a.client = client
			a.hello, err = client.Hello()
		} else {
			log.Print(err)
			mygo.Dialog.Error("Retty could not restart its session server", err.Error())
			mygo.App.Quit()
			return
		}
	}
	if err == nil {
		a.err = serverVersionError(a.hello.Version)
	}
	a.reset()
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:                "Retty",
		Width:                1000,
		Height:               620,
		MinWidth:             560,
		MinHeight:            340,
		StateKey:             "main",
		TitleBarStyle:        mygo.TitleBarHidden,
		TrafficLightPosition: &mygo.Point{X: 12, Y: 9},
		BackgroundColor:      "light-dark(#efe1e6, #1b1d20)",
		Content:              ui.View(a.view),
	})
	a.win = win
	win.OnFocus(a.closeViewedPaneNotice)
	win.OnFocus(func() { a.focusedWin = true; a.syncRemotePaneActivity(); win.Invalidate() })
	win.OnBlur(func() { a.focusedWin = false; a.syncRemotePaneActivity(); win.Invalidate() })
	startDesktopPushPresence(a)
	if !a.restore() {
		home, _ := os.UserHomeDir()
		a.newTab(home)
	}
	a.changed()
	go a.poll(win, client)
	a.debugHook()
	a.startDesktopControl()
	a.performanceFixture()
	win.OnClosed(func() {
		if a.control != nil {
			a.control.Close()
			a.control = nil
		}
		if a.terminalHistory != nil {
			a.terminalHistory.close()
		}
		if a.files != nil {
			a.files.stop()
		}
		a.stopPhonePair()
		a.saveNow()
		a.saveDesktopHistory()
		a.quitting = true
		for _, p := range a.allPanes() {
			a.endPane(p, false) // detaches: the session goes on
		}
		a.waitRemoteSizeReleases()
		a.closeDesktopConnections()
		a.client.Close()
		a.win = nil
	})
	a.restorePhonePair()
	// Tunnels to other desktops then keep one key each across connections.
	if err := remote.LoadClientSecret(filepath.Join(rex.Dir(), "remote")); err != nil {
		log.Printf("remote: client secret: %v", err)
	}
	a.loadDesktopHistory()
}

func serverVersionError(version int) string {
	if rex.CompatibleProtocol(version) {
		return ""
	}
	return fmt.Sprintf("会话服务协议版本为 %d，当前支持 %d–%d。普通退出会保留后台服务；使用 Shell → Quit and End All Sessions 后重新打开可更新服务（会结束现有会话）。", version, rex.MinProtocolVersion, rex.ProtocolVersion)
}

// saveNow sends the layout to the server and waits for it to be kept.
func (a *App) saveNow() {
	if !a.filesWidthSaveAt.IsZero() {
		a.filesWidthSaveAt = time.Time{}
		saveSettings()
	}
	if a.client == nil || len(a.tabs) == 0 && !a.quitting {
		return
	}
	b, err := json.Marshal(a.snapshot())
	if err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		a.client.SetLayout(b)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// reset forgets the state of a window that closed, keeping the server's
// connection, for the next window.
func (a *App) reset() {
	a.pollSnapshots = nil
	a.tabs, a.active, a.focusReq = nil, 0, nil
	a.activeRemote = nil
	a.paletteOpen, a.renaming = false, nil
	a.settingsOpen = false
	a.saveDue, a.quitting, a.lastSnapshot, a.title = false, false, "", ""
}

// staleServer reports whether the server runs another build of the app
// than this one, as after mygo dev rebuilt it.
func staleServer(h rex.Hello) bool {
	exe, at := rex.Executable()
	return h.Exe != exe || !h.ExeTime.Equal(at)
}
