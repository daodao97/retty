package clilink

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func executableFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "Retty ' app", "Contents", "MacOS", "Retty")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf 'fixture-cli\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallCLILinkRunsExecutableAndIsIdempotent(t *testing.T) {
	exe := executableFixture(t)
	link := filepath.Join(t.TempDir(), "bin", "retty")
	if err := Install(exe, link); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Lstat(link)
	if out, err := exec.Command(link).Output(); err != nil || strings.TrimSpace(string(out)) != "fixture-cli" {
		t.Fatal("installed command did not run its app executable", string(out), err)
	}
	if err := Install(exe, link); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Lstat(link)
	if !os.SameFile(before, after) {
		t.Fatal("reinstall replaced an already-correct link")
	}
}

func TestInstallCLILinkPreservesConflictingPaths(t *testing.T) {
	exe := executableFixture(t)
	for _, kind := range []string{"file", "directory", "symlink", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			link := filepath.Join(t.TempDir(), "retty")
			switch kind {
			case "file":
				os.WriteFile(link, []byte("existing program"), 0755)
			case "directory":
				os.Mkdir(link, 0755)
			case "symlink":
				os.Symlink(executableFixture(t), link)
			case "dangling-symlink":
				os.Symlink(filepath.Join(t.TempDir(), "missing"), link)
			}
			before, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if err := Install(exe, link); !errors.Is(err, ErrConflict) {
				t.Fatal("expected conflict", err)
			}
			after, err := os.Lstat(link)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("installer altered the existing path", err)
			}
		})
	}
}

func TestInstallCLILinkRejectsInvalidTargetBeforeCreatingBin(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "bin", "retty")
	exe := filepath.Join(root, "not-executable")
	os.WriteFile(exe, []byte("data"), 0644)
	for _, target := range []string{exe, exe + "-missing", root, "relative"} {
		if err := Install(target, link); err == nil {
			t.Fatal("invalid executable accepted", target)
		}
		if _, err := os.Lstat(filepath.Dir(link)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid target created installation directory", err)
		}
	}
}
