//go:build darwin

package automation

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestTimerPlistEscapesAndRunsEveryMinute(t *testing.T) {
	data := timerPlist("com.daodao.retty.automation.test", "/Apps/R&D/Retty", "/Users/x/<dir>")
	if err := xml.Unmarshal(data, new(struct{})); err != nil {
		t.Fatal("invalid plist", err)
	}
	text := string(data)
	for _, want := range []string{"/Apps/R&amp;D/Retty", "-automation-tick", "/Users/x/&lt;dir&gt;", "<key>StartInterval</key><integer>60</integer>"} {
		if !strings.Contains(text, want) {
			t.Error("plist lacks", want)
		}
	}
	// The timer must not keep a process alive or restart one.
	if strings.Contains(text, "KeepAlive") {
		t.Error("timer keeps itself alive")
	}
}
