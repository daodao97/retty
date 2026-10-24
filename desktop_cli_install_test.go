package main

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestInstallCLIIsSearchableAndRunsOnceWithoutChangingSession(t *testing.T) {
	a, tt := newStaticTestApp(t)
	beforeTab, beforeBounds := a.tab(), a.tab().Focus.bounds
	started, release, finished := make(chan struct{}, 1), make(chan struct{}), make(chan error, 1)
	want := errors.New("owned installer fixture")
	a.cliInstaller = &desktopCLIInstaller{
		install: func(context.Context) error { started <- struct{}{}; <-release; return want },
		result:  func(err error) { finished <- err },
	}
	a.openPalette()
	tt.Frame()
	tt.Type("install cli")
	if !tt.HasText("Install CLI…") {
		t.Fatal("CLI installer is missing from the command palette", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	<-started
	if a.paletteOpen || !a.cliInstaller.busy {
		t.Fatal("palette did not start installation")
	}
	a.installCLI()
	select {
	case <-started:
		t.Fatal("repeated activation started a second installer")
	default:
	}
	close(release)
	waitFor(t, tt, "CLI install result", func() bool { return !a.cliInstaller.busy })
	if err := <-finished; err != want {
		t.Fatal("installation error was lost", err)
	}
	if a.tab() != beforeTab || a.tab().Focus.bounds != beforeBounds {
		t.Fatal("CLI installation changed the terminal session")
	}
	cmd := actionByID("cli.install")
	if cmd == nil || cmd.Automation {
		t.Fatal("system installation must only be invoked from the user interface")
	}
}
