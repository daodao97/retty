//go:build darwin || linux

package rex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/agents"
	"retty/internal/gitstate"
)

func TestAcceptedTurnCapturesWithoutAViewerAndKeepsSessionIdentity(t *testing.T) {
	s := recoveryServer(t)
	s.turnSnapshots = &gitstate.SnapshotStore{Dir: filepath.Join(Dir(), "git-snapshots")}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-qb", "main", repo).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	path := filepath.Join(repo, "file.txt")
	os.WriteFile(path, []byte("already dirty\n"), 0600)
	ss, err := newSession("turn-pane", CreateOptions{Command: []string{"/bin/sh"}, Dir: repo, Cols: 91, Rows: 27})
	if err != nil {
		t.Fatal(err)
	}
	s.sessions[ss.id] = ss
	before := ss.info()
	report := func(turn string) {
		t.Helper()
		event := &AgentEvent{Agent: "claude", At: time.Now(), Input: agents.HookInput{Event: "UserPromptSubmit", SessionID: "conversation", TurnID: turn, CWD: repo}}
		if err := s.reportAgent(Request{SID: ss.id, Token: ss.agentToken, AgentEvent: event}); err != nil {
			t.Fatal(err)
		}
	}
	report("first")
	current := ss.info()
	if current.GitTurn == "" || current.ID != before.ID || current.PID != before.PID || current.Cols != before.Cols || current.Rows != before.Rows {
		t.Fatal("capturing a baseline changed the PTY or omitted its turn identity", before, current)
	}
	os.WriteFile(path, []byte("changed this turn\n"), 0600)
	report("first") // duplicate start must not swallow this turn's edits
	comparison, err := s.turnSnapshots.CompareTurn(context.Background(), repo, ss.id, "turn", current.GitTurn)
	if err != nil || len(comparison.Files) != 1 {
		t.Fatal(comparison, err)
	}
	patch, err := s.turnSnapshots.Diff(context.Background(), repo, comparison.Base, comparison.Current.Tree, "file.txt")
	if err != nil || !strings.Contains(string(patch), "-already dirty") || !strings.Contains(string(patch), "+changed this turn") {
		t.Fatal(string(patch), err)
	}
	report("second")
	if _, err := s.turnSnapshots.CompareTurn(context.Background(), repo, ss.id, "turn", current.GitTurn); err == nil {
		t.Fatal("a comparison accepted a baseline from another turn")
	}
	next := ss.info()
	comparison, err = s.turnSnapshots.CompareTurn(context.Background(), repo, ss.id, "turn", next.GitTurn)
	if err != nil || len(comparison.Files) != 0 {
		t.Fatal("new turn did not reset its baseline", comparison, err)
	}
}
