package main

import (
	"testing"

	"github.com/egoist/mygo"
)

func TestNotificationChannelSettingsAreIndependent(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.openSettings()
	a.settingsQuery = "notifications"
	tt.Frame()
	p := &Pane{content: &terminalPane{SID: "task"}}
	notices, closed := 0, 0
	a.agentNotify = func(mygo.NotificationOptions, func()) func() {
		notices++
		return func() { closed++ }
	}
	a.showTerminalNotice(p, "Task", "Before disabling")
	if err := tt.Click("Desktop notifications"); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || !readSettings().HideDesktopNotifications || readSettings().HidePhoneNotifications {
		t.Fatal("desktop switch did not close its notice or changed the phone preference")
	}
	for _, kind := range []string{"waiting", "completed", "failed", "program", "terminal"} {
		a.showPaneNotice(p, kind, mygo.NotificationOptions{Title: "Task", Body: kind})
	}
	if notices != 1 {
		t.Fatal("a desktop notification bypassed the master switch")
	}
	if err := tt.Click("Phone notifications"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Desktop notifications"); err != nil {
		t.Fatal(err)
	}
	if readSettings().HideDesktopNotifications || !readSettings().HidePhoneNotifications {
		t.Fatal("desktop re-enable changed the phone switch")
	}
	a.showTerminalNotice(p, "Task", "Desktop only")
	if notices != 2 {
		t.Fatal("disabling phone notifications also disabled the desktop")
	}
	if err := tt.Click("Phone notifications"); err != nil {
		t.Fatal(err)
	}
	if readSettings().HidePhoneNotifications || readSettings().HideDesktopNotifications {
		t.Fatal("channel preferences were not saved independently")
	}
}
