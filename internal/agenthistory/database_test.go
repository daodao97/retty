//go:build !ios

package agenthistory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

func TestPaginatedCodexHistoryAndOriginalProvider(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, "state_5.sqlite")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Execute(ctx, `CREATE TABLE threads(id TEXT PRIMARY KEY,cwd TEXT,model_provider TEXT,source TEXT,thread_source TEXT,title TEXT,name TEXT,rollout_path TEXT,history_mode TEXT,creator_account_id TEXT,updated_at INTEGER,updated_at_ms INTEGER,recency_at_ms INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, "sessions", "rollout-legacy.jsonl")
	write(t, legacy, codexMeta("legacy", project))
	for _, row := range []struct{ id, provider, source, threadSource, history, owner, rollout string }{
		{"paginated", "gateway", "cli", "user", "paginated", "old-owner", ""},
		{"other-provider", "openai", "vscode", "user", "paginated", "owner-a", ""},
		{"legacy", "gateway-old", "cli", "", "legacy", "", legacy},
		{"deleted", "openai", "cli", "user", "legacy", "", "/absent/rollout.jsonl"},
		{"worker", "openai", `{"subagent":"review"}`, "", "paginated", "", ""},
		{"guardian", "openai", "cli", "guardian_review", "paginated", "", ""},
	} {
		_, err := db.Execute(ctx, `INSERT INTO threads VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.id, project, row.provider, row.source, row.threadSource, "Initial title", "Renamed "+row.id, row.rollout, row.history, row.owner, 1, 2000, 3000)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A stale rollout/index must not duplicate or override current DB metadata.
	write(t, filepath.Join(home, "sessions", "rollout-paginated.jsonl"), codexMeta("paginated", project))
	write(t, filepath.Join(home, "session_index.jsonl"), map[string]string{"id": "paginated", "thread_name": "Stale name"})
	write(t, filepath.Join(home, "auth.json"), map[string]any{"tokens": map[string]string{"account_id": "owner-b", "access_token": "fixture-secret-never-retained"}})
	before, _ := os.Stat(path)
	var r Reader
	list, err := r.Read(home, filepath.Join(home, "absent-claude"))
	if err != nil || len(list) != 3 {
		t.Fatalf("sessions=%+v error=%v", list, err)
	}
	byID := make(map[string]Session)
	for _, s := range list {
		byID[s.ID] = s
		if !s.Updated.Equal(time.UnixMilli(3000)) {
			t.Fatal("file mtime replaced database activity")
		}
	}
	s := byID["paginated"]
	if s.Title != "Renamed paginated" || s.Provider != "gateway" || s.ResumeError != "" {
		t.Fatal("current provider/name lost, or gateway incorrectly required ChatGPT account", s)
	}
	cmd, env, err := s.Resume()
	if err != nil || !reflect.DeepEqual(cmd, []string{"codex", "resume", "paginated", "-C", project, "-c", `model_provider="gateway"`}) || !reflect.DeepEqual(env, []string{"CODEX_HOME=" + home}) {
		t.Fatal(cmd, env, err)
	}
	if _, _, err := byID["other-provider"].Resume(); err == nil {
		t.Fatal("OpenAI conversation resumed under a different account")
	}
	// A refresh after account switching must clear the mismatch, even with caches.
	write(t, filepath.Join(home, "auth.json"), map[string]any{"tokens": map[string]string{"account_id": "owner-a"}})
	list, err = r.Read(home, filepath.Join(home, "absent-claude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == "other-provider" && s.ResumeError != "" {
			t.Fatal("account switch did not refresh ownership")
		}
	}
	after, _ := os.Stat(path)
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("history scan modified the Codex database")
	}
}

func TestCodexHomesDiscoveryAndAccountBoundCopies(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	primary, account, custom := filepath.Join(user, ".codex"), filepath.Join(user, ".codexhost", "codex-homes", "second"), filepath.Join(user, ".codex-work")
	for _, home := range []string{primary, account, custom, filepath.Join(user, ".codex.backup-1"), filepath.Join(user, ".codex-backup-old")} {
		write(t, filepath.Join(home, "sessions", "rollout-shared.jsonl"), codexMeta("same-id", project))
		write(t, filepath.Join(home, "session_index.jsonl"), map[string]string{"id": "same-id", "thread_name": filepath.Base(home)})
	}
	registry := filepath.Join(user, ".codexhost", "codex-accounts")
	write(t, filepath.Join(registry, "accounts.json"), map[string]any{"accounts": []any{
		map[string]string{"accountId": "one", "codexHome": primary, "label": "Personal"},
		map[string]string{"accountId": "two", "codexHome": account, "label": "Work"},
	}})
	write(t, filepath.Join(registry, "thread-accounts.json"), map[string]any{"bindings": map[string]string{"same-id": "two"}})
	alias := filepath.Join(user, "active-home")
	if err := os.Symlink(primary, alias); err != nil {
		t.Fatal(err)
	}
	sources := DiscoverCodexSources(user, alias)
	if len(sources.Homes) != 3 {
		t.Fatal("account home missing, alias duplicated, or backup scanned", sources.Homes)
	}
	var r Reader
	list, err := r.ReadSources(sources, filepath.Join(user, "claude"))
	if err != nil || len(list) != 1 || list[0].ConfigDir != canonicalHome(account) || list[0].AccountLabel != "Work" || list[0].Title != "second" {
		t.Fatal("account binding or per-home title mixed", list, err)
	}
	// Independent conversations with the same ID remain distinct when no
	// ownership registry proves that they are copies of one conversation.
	sources.Bindings = nil
	list, err = r.ReadSources(sources, filepath.Join(user, "claude"))
	if err != nil || len(list) != 3 {
		t.Fatal("separate home identities were collapsed", list, err)
	}
	keys := make(map[string]bool)
	for _, s := range list {
		if keys[s.Key()] {
			t.Fatal("duplicate identity")
		}
		keys[s.Key()] = true
	}
}

func TestCodexProviderResumeEscapesConfigValue(t *testing.T) {
	s := Session{Agent: "codex", ID: "thread", Dir: t.TempDir(), ConfigDir: t.TempDir(), Provider: `provider"quoted`}
	cmd, _, err := s.Resume()
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.TrimPrefix(cmd[len(cmd)-1], "model_provider=")
	var provider string
	if json.Unmarshal([]byte(raw), &provider) != nil || provider != s.Provider || len(cmd) != 7 {
		t.Fatal("provider was interpreted as config syntax", cmd)
	}
}

func TestSharedHistorySymlinkKeepsAccountScopeAcrossCachedReads(t *testing.T) {
	base, project := t.TempDir(), t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	write(t, filepath.Join(first, "sessions", "rollout-shared.jsonl"), codexMeta("shared", project))
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(first, "sessions"), filepath.Join(second, "sessions")); err != nil {
		t.Fatal(err)
	}
	sources := Sources{Homes: []Home{{Dir: first, Label: "First"}, {Dir: second, Label: "Second"}}}
	var r Reader
	for range 2 {
		list, err := r.ReadSources(sources, filepath.Join(base, "claude"))
		if err != nil || len(list) != 2 {
			t.Fatal("shared transcript lost one account", list, err)
		}
		if list[0].ConfigDir == list[1].ConfigDir || list[0].AccountLabel == list[1].AccountLabel {
			t.Fatal("cached transcript reused a different account's metadata")
		}
	}
}

func TestOlderCodexDatabaseSchema(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(home, "state_5.sqlite"), sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Execute(ctx, `CREATE TABLE threads(id TEXT,cwd TEXT,model_provider TEXT,source TEXT,title TEXT,rollout_path TEXT,updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(home, "sessions", "rollout-old.jsonl")
	write(t, rollout, codexMeta("old", project))
	if _, err := db.Execute(ctx, `INSERT INTO threads VALUES(?,?,?,?,?,?,?)`, "old", project, "older-gateway", "cli", "Older title", rollout, 1234); err != nil {
		t.Fatal(err)
	}
	var r Reader
	list, err := r.Read(home, filepath.Join(home, "claude"))
	if err != nil || len(list) != 1 || list[0].Provider != "older-gateway" || !list[0].Updated.Equal(time.Unix(1234, 0)) {
		t.Fatal("old metadata schema was not supported", list, err)
	}
}
