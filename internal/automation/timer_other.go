//go:build !darwin

package automation

// Install has no system timer to register here; cron or a systemd timer
// runs `retty automation tick` every minute instead.
func Install(dir, exe string) error { return ErrNoTimer }

func Uninstall(dir string) error { return nil }
