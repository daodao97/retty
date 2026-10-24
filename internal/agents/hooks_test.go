package agents

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookMergePreservesUserConfiguration(t *testing.T) {
	original := []byte(`{"model":"user-model","future":9007199254740993,"hooks":{"Stop":[{"matcher":"mine","custom":true,"hooks":[{"type":"command","command":"user-stop"}]}],"FutureEvent":[{"hooks":[{"type":"future","value":1}]}]}}`)
	installed, err := MergeHooks("claude", original, true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := MergeHooks("claude", installed, true)
	if err != nil || !bytes.Equal(again, installed) {
		t.Fatalf("install is not idempotent: %v", err)
	}
	// A user's handler in the same group as ours must survive removal.
	var config map[string]any
	json.Unmarshal(installed, &config)
	hooks := config["hooks"].(map[string]any)
	groups := hooks["Stop"].([]any)
	ours := groups[len(groups)-1].(map[string]any)
	ours["hooks"] = append(ours["hooks"].([]any), map[string]any{"type": "command", "command": "mixed-user-stop"})
	mixed, _ := json.Marshal(config)
	removed, err := MergeHooks("claude", mixed, false)
	if err != nil || !bytes.Contains(removed, []byte("mixed-user-stop")) || bytes.Contains(removed, []byte("RETTY_HOOK")) {
		t.Fatalf("uninstall lost foreign hook: %s, %v", removed, err)
	}
	removed, err = MergeHooks("claude", installed, false)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := readHookConfig(original)
	b, _ := readHookConfig(removed)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(aa, bb) {
		t.Fatalf("configuration changed: %s != %s", aa, bb)
	}
	for _, bad := range []string{`null`, `[]`, `{"hooks":[]}`, `{"hooks":{"Stop":{}}}`, `{"hooks":{"Stop":[{}]}}`, `{"hooks":{}} trailing`} {
		if _, err := MergeHooks("codex", []byte(bad), true); err == nil {
			t.Fatalf("invalid config accepted: %s", bad)
		}
	}
}

func TestInstallHooksBackupOverridesAndRemoval(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			if agent == "claude" {
				t.Setenv("CLAUDE_CONFIG_DIR", dir)
			} else {
				t.Setenv("CODEX_HOME", dir)
			}
			path, err := HookPath(agent)
			if err != nil || filepath.Dir(path) != dir {
				t.Fatalf("configuration override ignored: %s/%v", path, err)
			}
			original := []byte("{\n  \"description\": \"keep me\"\n}\n")
			os.WriteFile(path, original, 0o600)
			if err := SetHooks(agent, true); err != nil {
				t.Fatal(err)
			}
			if s := InspectHooks(agent); !s.Installed || !s.Present || s.Error != "" {
				t.Fatalf("not installed: %+v", s)
			}
			backup, _ := os.ReadFile(path + ".retty-backup")
			if !bytes.Equal(backup, original) {
				t.Fatal("original backup changed")
			}
			first, _ := os.ReadFile(path)
			if err := SetHooks(agent, true); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if !bytes.Equal(first, second) {
				t.Fatal("second install changed hooks")
			}
			if err := SetHooks(agent, false); err != nil {
				t.Fatal(err)
			}
			if s := InspectHooks(agent); s.Present || s.Error != "" {
				t.Fatalf("not removed: %+v", s)
			}
			os.WriteFile(path, []byte("malformed"), 0o600)
			if err := SetHooks(agent, true); err == nil {
				t.Fatal("malformed user settings overwritten")
			}
			got, _ := os.ReadFile(path)
			if string(got) != "malformed" {
				t.Fatal("malformed file changed")
			}
		})
	}
}

func TestHookCommandIsSilentOutsideRetty(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", HookCommand("claude"))
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("outside Retty: %q/%v", output, err)
	}
	path := filepath.Join(t.TempDir(), "hook ' with spaces")
	os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = '-agent-hook' ] && [ \"$2\" = 'codex' ]\n"), 0o700)
	cmd = exec.Command("/bin/sh", "-c", HookCommand("codex"))
	cmd.Env = []string{"RETTY_HOOK=" + path}
	if output, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("quoted executable: %q/%v", output, err)
	}
}

func TestApprovalHookUpgradePreservesOtherConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"custom":"keep","hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"user-handler","timeout":2},{"type":"command","command":` + quotedHookCommand() + `,"timeout":2}]}]}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeApprovalHook(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	var root map[string]any
	json.Unmarshal(first, &root)
	hooks := root["hooks"].(map[string]any)
	group := hooks["PermissionRequest"].([]any)[0].(map[string]any)
	entries := group["hooks"].([]any)
	if root["custom"] != "keep" || group["matcher"] != "Bash" || entries[0].(map[string]any)["timeout"] != float64(2) || entries[1].(map[string]any)["timeout"] != float64(120) {
		t.Fatal(string(first))
	}
	if err := UpgradeApprovalHook(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	backup, _ := os.ReadFile(path + ".retty-backup")
	if !bytes.Equal(first, second) || !bytes.Equal(original, backup) {
		t.Fatal("upgrade not idempotent or backup changed")
	}
}
func quotedHookCommand() string { b, _ := json.Marshal(HookCommand("claude")); return string(b) }
