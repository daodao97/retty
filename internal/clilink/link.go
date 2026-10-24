// Package clilink installs a command without replacing an existing program.
package clilink

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const SystemPath = "/usr/local/bin/retty"

var ErrConflict = errors.New("command path already exists")

// Install is idempotent for the same executable. Symlink creation is exclusive,
// so another installer cannot cause us to overwrite a file between checks.
func Install(executable, link string) error {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(link) {
		return errors.New("CLI installation requires absolute paths")
	}
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("CLI target is not an executable file")
	}
	check := func() error {
		if target, err := os.Readlink(link); err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(link), target)
			}
			if filepath.Clean(target) == filepath.Clean(executable) {
				return nil
			}
		}
		return fmt.Errorf("%w: %s（未覆盖已有文件）", ErrConflict, link)
	}
	if _, err := os.Lstat(link); err == nil {
		return check()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		return err
	}
	if err := os.Symlink(executable, link); errors.Is(err, os.ErrExist) {
		return check()
	} else {
		return err
	}
}
