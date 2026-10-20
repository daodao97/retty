package agenthistory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// This opt-in contract test uses the installed Codex protocol against disposable
// homes with unreachable mock providers. No inference request or real account
// data is sent; only native history/configuration APIs run.
func TestCodexContinuationNative(t *testing.T) {
	if os.Getenv("RETTY_CODEX_RESUME_E2E") != "1" {
		t.Skip("set RETTY_CODEX_RESUME_E2E=1 for disposable native Codex checks")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	makeHome := func(name, provider string) string {
		home := filepath.Join(root, name)
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		config := `model_provider = "` + provider + `"
model = "fixture"
[model_providers.before]
name = "Before"
base_url = "http://127.0.0.1:9/v1"
wire_api = "responses"
requires_openai_auth = false
[model_providers.after]
name = "After"
base_url = "http://127.0.0.1:9/v1"
wire_api = "responses"
requires_openai_auth = false
`
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		return home
	}
	home := makeHome("source", "after")
	s := Session{Agent: "codex", ID: "11111111-1111-4111-8111-111111111111", Dir: root, ConfigDir: home, Provider: "before", Title: "Fixture"}
	path := filepath.Join(home, "sessions", "2026", "10", "20", "rollout-2026-10-20T00-00-00-"+s.ID+".jsonl")
	var events []any
	events = append(events, map[string]any{"type": "session_meta", "payload": map[string]any{"id": s.ID, "timestamp": "2026-10-20T00:00:00Z", "cwd": root, "originator": "fixture", "cli_version": "0.163.0", "source": "cli", "model_provider": "before"}})
	for _, id := range []string{"first", "second"} {
		events = append(events,
			map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": id, "model_context_window": 128000}},
			map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Fixture context " + id}}}},
			map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "Fixture context " + id, "images": []any{}, "local_images": []any{}, "text_elements": []any{}}},
			map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": "Fixture answer " + id}}}},
			map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": id, "last_agent_message": "Fixture answer " + id}},
		)
	}
	for _, event := range events {
		event.(map[string]any)["timestamp"] = "2026-10-20T00:00:00Z"
	}
	write(t, path, events...)
	before, _ := os.ReadFile(path)
	// Register only the disposable rollout through the native protocol.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rpc, err := openCodexRPC(ctx, executable, home, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = rpc.call("thread/resume", map[string]any{"threadId": s.ID, "path": path, "excludeTurns": true}, nil)
	rpc.close()
	if err != nil {
		t.Fatal(err)
	}
	before, _ = os.ReadFile(path)
	state := filepath.Join(root, "retty-state", "continuations.json")
	fork, err := ContinueCodex(s, executable, home, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ID == s.ID || fork.Provider != "after" || fork.ConfigDir != home {
		t.Fatalf("wrong continuation: %+v", fork)
	}
	again, err := ContinueCodex(s, executable, home, state, nil)
	if err != nil || again.ID != fork.ID {
		t.Fatalf("did not reuse persisted fork: %+v %v", again, err)
	}
	same, err := ContinueCodex(fork, executable, home, state, nil)
	if err != nil || same.ID != fork.ID {
		t.Fatalf("same-provider resume forked again: %+v %v", same, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("original history was modified")
	}
	st, _ := os.Stat(state)
	if st.Mode().Perm() != 0600 {
		t.Fatal("continuation metadata is not private")
	}
	data, _ := os.ReadFile(state)
	if strings.Contains(string(data), "Fixture context") {
		t.Fatal("transcript copied into Retty state")
	}
	// Cross-home import must preserve both turns and use the destination config.
	destination := makeHome("destination", "after")
	imported, err := ContinueCodex(s, executable, destination, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ConfigDir != destination || imported.ID == s.ID || imported.Provider != "after" {
		t.Fatalf("wrong imported continuation: %+v", imported)
	}
	if _, err := os.Stat(filepath.Join(destination, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("credentials unexpectedly copied")
	}
	repeated, err := ContinueCodex(s, executable, destination, state, nil)
	if err != nil || repeated.ID != imported.ID {
		t.Fatalf("cross-home mapping not reused: %+v %v", repeated, err)
	}
	// The same native rollout can be read from a compressed archive without
	// depending on a zstd executable or changing source history.
	compressed := makeHome("compressed-source", "before")
	compressedPath := filepath.Join(compressed, "sessions", filepath.Base(path))
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressedData := encoder.EncodeAll(before, nil)
	encoder.Close()
	os.MkdirAll(filepath.Dir(compressedPath), 0700)
	os.WriteFile(compressedPath+".zst", compressedData, 0600)
	// Use native registration before compressing the legacy fixture so Codex can
	// resolve its canonical virtual path from the database.
	os.WriteFile(compressedPath, before, 0600)
	rpc, err = openCodexRPC(ctx, executable, compressed, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = rpc.call("thread/resume", map[string]any{"threadId": s.ID, "path": compressedPath, "excludeTurns": true}, nil)
	rpc.close()
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(compressedPath)
	compressedSession := s
	compressedSession.ConfigDir = compressed
	_, err = ContinueCodex(compressedSession, executable, destination, state, nil)
	if err != nil {
		t.Fatalf("compressed cross-home continuation: %v", err)
	}
	// A real native migration exercises paginated history without model calls.
	command := exec.Command(executable, "migrate-rollouts", "--apply", "--thread", s.ID, "--json")
	command.Dir = root
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture migration failed: %v %s", err, out)
	}
	migratedDestination := makeHome("paginated-destination", "after")
	migrated, err := ContinueCodex(s, executable, migratedDestination, state, nil)
	if err != nil {
		t.Fatalf("paginated cross-home continuation: %v", err)
	}
	if migrated.Provider != "after" || migrated.ConfigDir != migratedDestination {
		t.Fatalf("wrong migrated continuation: %+v", migrated)
	}
	var records map[string]continuationRecord
	if json.Unmarshal(data, &records) != nil || len(records) == 0 {
		t.Fatal("missing durable continuation mapping")
	}
}
