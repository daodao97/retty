package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// An environment-based command stays stable across app moves/upgrades. It is
// a no-op outside Retty, and contains neither a session ID nor a secret.
func HookCommand(agent string) string {
	return `if [ -n "$RETTY_HOOK" ] && [ -x "$RETTY_HOOK" ]; then "$RETTY_HOOK" -agent-hook ` + agent + `; fi`
}

// ConfigEnv names the environment variable that relocates an agent's
// configuration directory, or is empty for agents that have none.
func ConfigEnv(agent string) string {
	switch agent {
	case "claude":
		return "CLAUDE_CONFIG_DIR"
	case "codex":
		return "CODEX_HOME"
	}
	return ""
}

// ConfigDir is an agent's configuration directory: the one its ConfigEnv
// names when set, otherwise ~/.<agent>.
func ConfigDir(agent string) (string, error) {
	if key := ConfigEnv(agent); key != "" {
		if dir := os.Getenv(key); dir != "" {
			return dir, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "."+agent), nil
}

func HookPath(agent string) (string, error) {
	file := "settings.json"
	switch agent {
	case "claude", "gemini", "qwen":
	case "codex":
		file = "hooks.json"
	default:
		return "", fmt.Errorf("unsupported hook agent %q", agent)
	}
	dir, err := ConfigDir(agent)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, file), nil
}

type HookInstallation struct {
	Installed bool // all expected events present
	Present   bool // includes incomplete installations
	Error     string
}

func InspectHooks(agent string) HookInstallation {
	path, err := HookPath(agent)
	if err != nil {
		return HookInstallation{Error: err.Error()}
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return HookInstallation{}
	}
	if err != nil {
		return HookInstallation{Error: err.Error()}
	}
	root, err := readHookConfig(b)
	if err != nil {
		return HookInstallation{Error: err.Error()}
	}
	hooks, _ := root["hooks"].(map[string]any)
	n := 0
	for _, event := range HookEvents(agent) {
		found := false
		groups, _ := hooks[event].([]any)
		for _, value := range groups {
			group := value.(map[string]any)
			entries, _ := group["hooks"].([]any)
			for _, item := range entries {
				entry, _ := item.(map[string]any)
				if ownedHook(agent, entry) {
					found = true
				}
			}
		}
		if found {
			n++
		}
	}
	return HookInstallation{Installed: n == len(HookEvents(agent)) && n > 0, Present: n > 0}
}

func ownedHook(agent string, h map[string]any) bool {
	return h["type"] == "command" && h["command"] == HookCommand(agent)
}

func readHookConfig(b []byte) (map[string]any, error) {
	root := map[string]any{}
	if len(b) == 0 {
		return root, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid hook configuration: %w", err)
	}
	if root == nil || !json.Valid(b) {
		return nil, errors.New("hook configuration must be a JSON object")
	}
	if raw, exists := root["hooks"]; exists {
		hooks, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("hooks must be a JSON object")
		}
		for event, rawGroups := range hooks {
			groups, ok := rawGroups.([]any)
			if !ok {
				return nil, fmt.Errorf("hooks.%s must be an array", event)
			}
			for _, value := range groups {
				group, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("hooks.%s contains an invalid matcher group", event)
				}
				if _, ok := group["hooks"].([]any); !ok {
					return nil, fmt.Errorf("hooks.%s handlers must be an array", event)
				}
			}
		}
	}
	return root, nil
}

// MergeHooks preserves all foreign handlers and settings, even when a
// matcher group mixes Retty and user hooks. Running it twice is idempotent.
func MergeHooks(agent string, b []byte, install bool) ([]byte, error) {
	events := HookEvents(agent)
	if len(events) == 0 {
		return nil, fmt.Errorf("unsupported hook agent %q", agent)
	}
	root, err := readHookConfig(b)
	if err != nil {
		return nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, raw := range hooks {
		groups := raw.([]any)
		kept := make([]any, 0, len(groups))
		for _, value := range groups {
			group := value.(map[string]any)
			entries := group["hooks"].([]any)
			remaining := slices.DeleteFunc(entries, func(v any) bool {
				entry, _ := v.(map[string]any)
				return ownedHook(agent, entry)
			})
			if len(remaining) == 0 && len(entries) > 0 {
				continue
			}
			group["hooks"] = remaining
			kept = append(kept, group)
		}
		if len(kept) == 0 && len(groups) > 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if install {
		timeout := 2
		if agent == "gemini" {
			timeout = 2000
		} // Gemini uses milliseconds.
		for _, event := range events {
			groups, _ := hooks[event].([]any)
			eventTimeout := timeout
			if agent == "claude" && event == "PermissionRequest" {
				eventTimeout = 120
			}
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": HookCommand(agent), "timeout": eventTimeout}}}
			hooks[event] = append(groups, group)
		}
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	result, err := json.MarshalIndent(root, "", "  ")
	return append(result, '\n'), err
}

// SetHooks changes only Retty's entries. The first original file is backed
// up alongside it; malformed JSON and concurrent edits are never replaced.
func SetHooks(agent string, install bool) error {
	path, err := HookPath(agent)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) && !install {
		return nil
	}
	updated, err := MergeHooks(agent, original, install)
	if err != nil {
		return err
	}
	return writeHookConfig(path, original, updated)
}

// UpgradeApprovalHook changes only the old Retty-owned two-second Claude
// permission handler. It does not enable hooks or touch other handlers/settings.
func UpgradeApprovalHook() error {
	path, err := HookPath("claude")
	if err != nil {
		return err
	}
	if real, e := filepath.EvalSymlinks(path); e == nil {
		path = real
	}
	original, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	root, err := readHookConfig(original)
	if err != nil {
		return err
	}
	hooks, _ := root["hooks"].(map[string]any)
	groups, _ := hooks["PermissionRequest"].([]any)
	changed := false
	for _, value := range groups {
		group, _ := value.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, v := range entries {
			entry, _ := v.(map[string]any)
			if ownedHook("claude", entry) && fmt.Sprint(entry["timeout"]) == "2" {
				entry["timeout"] = 120
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeHookConfig(path, original, append(updated, '\n'))
}

func writeHookConfig(path string, original, updated []byte) error {
	if bytes.Equal(updated, original) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(original) > 0 {
		backup, err := os.OpenFile(path+".retty-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := backup.Write(original)
			closeErr := backup.Close()
			if writeErr != nil {
				os.Remove(path + ".retty-backup")
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		} else if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".retty-hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(updated); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(current, original) {
		return errors.New("hook configuration changed during installation; retry")
	}
	return os.Rename(tmp.Name(), path)
}
