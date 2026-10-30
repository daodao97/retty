package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// closedLid keeps a Mac serving with its lid shut, as Reminal's closed-lid
// mode does: sleep disabled, so shutting the lid neither sleeps nor locks,
// and a virtual display in place of the real ones while there are none, so
// windows still draw and the phone still sees them. Sleep is pmset's
// system-wide SleepDisabled, which outlives Retty; the switch shows what
// pmset says, read at start and after each change.
type closedLid struct {
	on, known, busy bool
	err             string
	// stop ends the display watcher, which runs while the mode is on.
	stop context.CancelFunc
}

// sleepDisabledIn reads SleepDisabled from `pmset -g`.
func sleepDisabledIn(out string) (bool, bool) {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "SleepDisabled" {
			return f[1] == "1", true
		}
	}
	return false, false
}

func sleepDisabled() (bool, error) {
	out, err := exec.Command("pmset", "-g").Output()
	if err != nil {
		return false, err
	}
	on, ok := sleepDisabledIn(string(out))
	if !ok {
		return false, fmt.Errorf("pmset reports no SleepDisabled")
	}
	return on, nil
}

// refreshClosedLid reads the mode from pmset, off the UI thread.
func (a *App) refreshClosedLid() {
	if runtime.GOOS != "darwin" {
		return
	}
	update := a.desktopDispatcher()
	go func() {
		on, err := sleepDisabled()
		update(func() {
			if err == nil {
				a.closedLid.on, a.closedLid.known = on, true
			}
			a.syncDisplayWatcher()
		})
	}()
}

// setClosedLid turns the mode on or off. pmset needs an administrator, so
// macOS asks for a password; canceling it changes nothing.
func (a *App) setClosedLid(on bool) {
	l := &a.closedLid
	if runtime.GOOS != "darwin" || l.busy {
		return
	}
	l.busy, l.err = true, ""
	update := a.desktopDispatcher()
	value := "0"
	if on {
		value = "1"
	}
	go func() {
		script := fmt.Sprintf(`do shell script "/usr/bin/pmset -a disablesleep %s" with administrator privileges with prompt "Retty 需要管理员权限来更改合盖睡眠设置。"`, value)
		out, err := exec.Command("osascript", "-e", script).CombinedOutput()
		now, readErr := sleepDisabled()
		update(func() {
			l.busy = false
			if readErr == nil {
				l.on, l.known = now, true
			}
			// Canceling the password prompt is a choice, not a failure.
			if err != nil && !strings.Contains(string(out), "-128") {
				l.err = "无法更改合盖睡眠设置"
			}
			a.syncDisplayWatcher()
		})
	}()
}

// closedLidLabel explains the mode and its state, for the settings.
func (a *App) closedLidLabel() string {
	l := &a.closedLid
	switch {
	case l.err != "":
		return l.err
	case l.busy:
		return "正在更改…"
	case !l.known:
		return "正在读取系统睡眠设置…"
	}
	return "开启后合上盖子也不睡眠、不锁屏；没有显示器时自动使用虚拟屏幕，手机可继续查看和操作。合盖期间电脑保持解锁，请只在可信环境开启。开启和关闭需要管理员密码。"
}

// syncDisplayWatcher runs the display watcher while the mode is on.
func (a *App) syncDisplayWatcher() {
	l := &a.closedLid
	helper := screenHelperPath()
	switch {
	case l.on && l.stop == nil && helper != "":
		ctx, stop := context.WithCancel(context.Background())
		l.stop = stop
		go watchDisplays(ctx, helper, 10*time.Second)
	case !l.on && l.stop != nil:
		l.stop()
		l.stop = nil
	}
}

// watchDisplays adds a virtual display while no real one is online, as
// when the lid is shut with nothing plugged in, and removes it when one
// returns. The display's helper ends with Retty, as its stdin closes.
func watchDisplays(ctx context.Context, helper string, every time.Duration) {
	var in io.WriteCloser
	var ended chan struct{}
	remove := func() {
		if ended != nil {
			in.Close()
			<-ended
			in, ended = nil, nil
		}
	}
	defer remove()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if ended != nil {
			select {
			case <-ended:
				in, ended = nil, nil // it failed or was ended; try again below
			default:
			}
		}
		out, err := exec.CommandContext(ctx, helper, "displays").Output()
		if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && perr == nil {
			switch {
			case n == 0 && ended == nil:
				cmd := exec.Command(helper, "vdisplay", "1440", "900")
				if pipe, err := cmd.StdinPipe(); err == nil && cmd.Start() == nil {
					in, ended = pipe, make(chan struct{})
					done := ended
					go func() { cmd.Wait(); close(done) }()
				}
			case n > 0:
				remove()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
