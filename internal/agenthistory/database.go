//go:build !ios

package agenthistory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

func readCodexDatabase(home string) ([]Session, error) {
	path := filepath.Join(home, "state_5.sqlite")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{ReadOnly: true, BusyTimeout: 100 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	schema, err := db.Query(ctx, "PRAGMA table_info(threads)")
	if err != nil {
		return nil, err
	}
	columns := make(map[string]bool)
	for _, row := range schema.Values {
		if len(row) > 1 {
			name, _ := row[1].(string)
			columns[name] = true
		}
	}
	if !columns["id"] || !columns["cwd"] {
		return nil, nil
	}
	// Only fixed, known column names enter SQL. Old schemas supply defaults.
	fields := []string{"id", "cwd", "model_provider", "source", "thread_source", "title", "name", "rollout_path", "history_mode", "creator_account_id", "updated_at", "updated_at_ms", "recency_at_ms"}
	selects := make([]string, len(fields))
	for i, field := range fields {
		selects[i] = "NULL"
		if columns[field] {
			selects[i] = field
			if field == "title" || field == "name" {
				selects[i] = "substr(" + field + ",1,512)"
			}
		}
	}
	var sessions []Session
	for offset := 0; ; offset += 1000 {
		rows, err := db.Query(ctx, "SELECT "+strings.Join(selects, ",")+" FROM threads ORDER BY id LIMIT ? OFFSET ?", 1000, offset)
		if err != nil {
			return nil, err
		}
		for _, row := range rows.Values {
			text := func(i int) string { s, _ := row[i].(string); return s }
			if !validID(text(0)) || !filepath.IsAbs(text(1)) || hiddenCodexSource(text(3), text(4)) || strings.Contains(filepath.ToSlash(text(7)), "/sessions/retty-import-") {
				continue
			}
			if text(8) != "paginated" {
				// A deleted legacy rollout may leave stale index metadata.
				if _, err := os.Stat(text(7)); err != nil {
					continue
				}
			}
			title := text(6)
			if title == "" {
				title = text(5)
			}
			seconds, _ := row[10].(int64)
			updated := time.Unix(seconds, 0)
			for _, i := range []int{11, 12} {
				if ms, ok := row[i].(int64); ok && ms > 0 {
					updated = time.UnixMilli(ms)
				}
			}
			sessions = append(sessions, Session{Agent: "codex", ConfigDir: home, ID: text(0), Dir: text(1), Provider: text(2), AccountID: text(9), Title: displayTitle(title), Updated: updated, named: text(6) != ""})
		}
		if len(rows.Values) < 1000 {
			break
		}
	}
	return sessions, nil
}
