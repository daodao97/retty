package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"retty/internal/files"
	"retty/internal/gitstate"
	"retty/internal/rex"
)

func TestPaneGitSummaryOpensItsOwnChangesAndCanBeDisabled(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.info.Dir = t.TempDir()
	p.term.Feed([]byte("\x1b[H\x1b[2J$ git status --short\r\n M app.go\r\n?? new-feature.go\r\n"))
	status := &gitstate.Status{Repository: gitstate.Repository{Root: p.info.Dir}, Branch: "task/fix", Added: 4, Removed: 2, Files: 2, Ahead: 1}
	a.gitStatus = &desktopGitState{entries: map[gitScope]*desktopGitEntry{paneGitScope(p): {status: status}}}
	tt.Frame()
	if !tt.HasText("task/fix · +4 −2") {
		t.Fatal("pane is missing its Git summary", tt.Texts())
	}
	saveSettingsImage(t, tt, "git-pane-summary")
	if err := tt.Click("Git 变更 task/fix · +4 −2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !prefs.FilesOpen || !prefs.FilesDiff || a.files == nil || a.files.pane != p || !a.files.diff || a.files.dir != p.info.Dir {
		t.Fatal("Git summary opened another pane or a non-Diff view")
	}
	a.files.stop()
	a.toggleFiles()
	a.setGitStatus(false)
	tt.Frame()
	if !readSettings().HideGitStatus || a.paneGit(p) != nil || tt.HasText("task/fix · +4 −2") {
		t.Fatal("disabling Git status did not remove its summary")
	}
}

func TestGitStatusReadsAreAsyncDeduplicatedAndLateResultsCannotFollowDirectory(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	a, tt := newStaticTestApp(t)
	p := a.tab().Focus
	p.info.Dir = "/fixture/old"
	p2 := &Pane{info: rex.SessionInfo{Dir: p.info.Dir}}
	a.tabs = append(a.tabs, &Tab{Focus: p2})
	var calls atomic.Int32
	release := make(chan struct{})
	a.gitRead = func(ctx context.Context, client *rex.Client, dir string) (files.Result, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return files.Result{}, ctx.Err()
		}
		return files.Result{Git: &gitstate.Status{Branch: "old"}}, nil
	}
	tt.Frame()
	waitFor(t, tt, "background Git read", func() bool { return calls.Load() > 0 })
	if calls.Load() != 1 || a.gitStatus.pending != 1 || a.paneGit(p) != nil {
		t.Fatal("view waited for Git or duplicated the same directory")
	}
	p.info.Dir, p2.info.Dir = "/fixture/new", "/fixture/new"
	a.gitRead = func(ctx context.Context, client *rex.Client, dir string) (files.Result, error) {
		return files.Result{Git: &gitstate.Status{Branch: "new"}}, nil
	}
	close(release)
	waitFor(t, tt, "new directory's Git summary", func() bool {
		return a.paneGit(p) != nil && a.paneGit(p).Branch == "new" && a.gitStatus.pending == 0
	})
	if tt.HasText("old") || len(a.gitStatus.entries) != 1 {
		t.Fatal("late response escaped its directory scope")
	}
}

func TestGitCacheKeepsHostAndConnectionIdentity(t *testing.T) {
	previous := prefs
	prefs.HideGitStatus = false
	t.Cleanup(func() { prefs = previous })
	local := &Pane{info: rex.SessionInfo{Dir: "/same/path"}}
	remote := &Pane{host: &desktopHost{client: &rex.Client{}}, info: local.info}
	a := &App{gitStatus: &desktopGitState{entries: map[gitScope]*desktopGitEntry{
		paneGitScope(local):  {status: &gitstate.Status{Branch: "local"}},
		paneGitScope(remote): {status: &gitstate.Status{Branch: "remote"}},
	}}}
	if a.paneGit(local).Branch != "local" || a.paneGit(remote).Branch != "remote" {
		t.Fatal("remote path was resolved against the local host")
	}
	remote.host.client = &rex.Client{}
	if a.paneGit(remote) != nil {
		t.Fatal("reconnected host reused the previous connection's result")
	}
	if got := gitSummary(&gitstate.Status{Branch: "main", Operation: "rebase", Files: 1, Truncated: true}); !strings.Contains(got, "rebase") || !strings.Contains(got, "部分") {
		t.Fatal("operation or truncated counts were hidden", got)
	}
}
