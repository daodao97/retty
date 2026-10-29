//go:build darwin

package automation

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"retty/internal/atomicfile"
)

// Install keeps one LaunchAgent per data directory that runs the timer every
// minute. Unlike the session server's on-demand job, it is installed in
// ~/Library/LaunchAgents: a schedule must survive logging out and restarting,
// when no Retty window or server runs. Sync keeps it only while an automation
// is enabled. Tests never install one.
func Install(dir, exe string) error {
	if testing.Testing() {
		return nil
	}
	label, path, err := timerFiles(dir)
	if err != nil {
		return err
	}
	data := timerPlist(label, exe, dir)
	target := launchdDomain()
	service := target + "/" + label
	loaded := exec.Command("/bin/launchctl", "print", service).Run() == nil
	// Opening the app syncs every time; an unchanged timer is left alone.
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) && loaded {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err = atomicfile.Write(path, data); err != nil {
		return err
	}
	if loaded {
		exec.Command("/bin/launchctl", "bootout", service).Run()
	}
	if out, err := exec.Command("/bin/launchctl", "bootstrap", target, path).CombinedOutput(); err != nil {
		return fmt.Errorf("installing the automation timer: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall removes the timer. Runs already started keep running.
func Uninstall(dir string) error {
	if testing.Testing() {
		return nil
	}
	label, path, err := timerFiles(dir)
	if err != nil {
		return err
	}
	// Opening the app without automations must not even ask launchd.
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	service := launchdDomain() + "/" + label
	if exec.Command("/bin/launchctl", "print", service).Run() == nil {
		exec.Command("/bin/launchctl", "bootout", service).Run()
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func timerFiles(dir string) (label, path string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	// Development and installed apps have different data directories, and so
	// timers of their own.
	sum := sha256.Sum256([]byte(dir))
	label = fmt.Sprintf("com.daodao.retty.automation.%x", sum[:6])
	return label, filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func launchdDomain() string {
	target := fmt.Sprintf("gui/%d", os.Getuid())
	if exec.Command("/bin/launchctl", "print", target).Run() != nil {
		target = fmt.Sprintf("user/%d", os.Getuid())
	}
	return target
}

// timerPlist runs the executable directly: the timer itself creates no
// terminals, and a server it starts gets the login environment as usual.
func timerPlist(label, exe, dir string) []byte {
	var plist bytes.Buffer
	str := func(s string) {
		plist.WriteString("<string>")
		xml.EscapeText(&plist, []byte(s))
		plist.WriteString("</string>")
	}
	plist.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key>`)
	str(label)
	plist.WriteString(`<key>ProgramArguments</key><array>`)
	str(exe)
	str("-automation-tick")
	plist.WriteString(`</array><key>EnvironmentVariables</key><dict><key>RETTY_DIR</key>`)
	str(dir)
	plist.WriteString(`</dict><key>StartInterval</key><integer>60</integer><key>RunAtLoad</key><true/><key>ProcessType</key><string>Background</string><key>StandardOutPath</key>`)
	str(filepath.Join(dir, "automation.log"))
	plist.WriteString(`<key>StandardErrorPath</key>`)
	str(filepath.Join(dir, "automation.log"))
	plist.WriteString(`</dict></plist>`)
	return plist.Bytes()
}
