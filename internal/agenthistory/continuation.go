package agenthistory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"retty/internal/atomicfile"
)

// Continuations records only source/target identities, never credentials or
// conversation content. Serialize preparation so simultaneous clicks cannot fork
// the same conversation twice, including clicks on copies in different homes.
var continuationMu sync.Mutex

type codexRPCError struct{ method, message string }

func (e *codexRPCError) Error() string { return "Codex " + e.method + ": " + e.message }

func missingCodexThread(err error) bool {
	var rpcErr *codexRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	message := strings.ToLower(rpcErr.message)
	return strings.Contains(message, "thread not loaded") || strings.Contains(message, "thread not found") || strings.Contains(message, "no conversation found") || strings.Contains(message, "no rollout found")
}

type codexRPC struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Scanner
	next int
}

func openCodexRPC(ctx context.Context, executable, home, dir string, extraEnv []string) (*codexRPC, error) {
	cmd := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
	cmd.Dir = home
	cmd.WaitDelay = 2 * time.Second
	// These helpers have no PTY and must never impersonate an existing pane.
	environment := make(map[string]string)
	for _, value := range append(os.Environ(), extraEnv...) {
		key, value, ok := strings.Cut(value, "=")
		if ok && !strings.HasPrefix(key, "RETTY_") && key != "CODEX_HOME" {
			environment[key] = value
		}
	}
	for key, value := range environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	slices.Sort(cmd.Env)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		return nil, err
	}
	rpc := &codexRPC{cmd: cmd, in: in, out: bufio.NewScanner(out)}
	rpc.out.Buffer(make([]byte, 4096), 32<<20)
	if err = rpc.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "retty_history", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, nil); err != nil {
		rpc.close()
		return nil, err
	}
	_, err = io.WriteString(in, "{\"method\":\"initialized\"}\n")
	if err != nil {
		rpc.close()
		return nil, err
	}
	return rpc, nil
}

func (r *codexRPC) close() {
	_ = r.in.Close()
	// EOF lets the owned helper shut down its MCP children normally. Only this
	// helper is eligible for a forced stop if graceful shutdown times out.
	done := make(chan struct{})
	go func() { _ = r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = r.cmd.Process.Kill()
		<-done
	}
}

func (r *codexRPC) call(method string, params, result any) error {
	r.next++
	data, err := json.Marshal(map[string]any{"id": r.next, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err = r.in.Write(append(data, '\n')); err != nil {
		return err
	}
	for r.out.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(r.out.Bytes(), &response) != nil || response.ID != r.next {
			continue
		}
		if response.Error != nil {
			return &codexRPCError{method: method, message: response.Error.Message}
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
	if err := r.out.Err(); err != nil {
		return err
	}
	return errors.New("Codex 会话准备进程已退出")
}

type codexThread struct {
	Thread struct {
		ID       string `json:"id"`
		Path     string `json:"path"`
		Provider string `json:"modelProvider"`
	} `json:"thread"`
	ModelProvider string `json:"modelProvider"`
}

type continuationRecord struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Home     string `json:"home"`
}

// ContinueCodex prepares an exact resume in the current home/provider/account.
// A provider/account change forks natively, preserving the original conversation.
// No turn is started, and inherited goals are explicitly left paused.
func ContinueCodex(s Session, executable, home, statePath string, extraEnv []string) (Session, error) {
	continuationMu.Lock()
	defer continuationMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	validation := s
	validation.ResumeError = ""
	if _, _, err := validation.Resume(); err != nil {
		return Session{}, err
	}
	home = canonicalHome(home)
	if !filepath.IsAbs(home) || !validID(s.ID) {
		return Session{}, errors.New("Codex 续接信息无效")
	}
	rpc, err := openCodexRPC(ctx, executable, home, s.Dir, extraEnv)
	if err != nil {
		return Session{}, err
	}
	defer rpc.close()
	var config struct {
		Config struct {
			Provider string `json:"model_provider"`
			Model    string `json:"model"`
		} `json:"config"`
	}
	if err = rpc.call("config/read", map[string]any{"includeLayers": false, "cwd": s.Dir}, &config); err != nil {
		return Session{}, err
	}
	provider := config.Config.Provider
	if provider == "" {
		provider = "openai"
	}
	account := currentAccountID(home)
	sourceProvider := s.Provider
	if sourceProvider == "" {
		sourceProvider = "openai"
	}
	target := s
	target.ConfigDir, target.Provider, target.AccountID, target.ResumeError = home, provider, account, ""
	sameHome := canonicalHome(s.ConfigDir) == home
	if sameHome && sourceProvider == provider && (sourceProvider != "openai" || s.AccountID == "" || s.AccountID == account) {
		return target, nil
	}
	// The key uses the current account ID, not auth tokens. Switching accounts or
	// providers gets its own continuation; switching back reuses the old branch.
	sourceKey := s.Agent + ":" + canonicalHome(s.ConfigDir) + ":" + s.ID
	keyBytes, _ := json.Marshal([]string{sourceKey, home, provider, account})
	key := string(keyBytes)
	records := make(map[string]continuationRecord)
	if data, readErr := os.ReadFile(statePath); readErr == nil {
		if err = json.Unmarshal(data, &records); err != nil {
			return Session{}, fmt.Errorf("读取 Codex 续接记录：%w", err)
		}
	} else if !os.IsNotExist(readErr) {
		return Session{}, readErr
	}
	if records == nil {
		records = make(map[string]continuationRecord)
	}
	forkSource := s.ID
	if record, ok := records[key]; ok && validID(record.ID) && record.Home == home && record.Provider == provider {
		var existing codexThread
		if err = rpc.call("thread/read", map[string]any{"threadId": record.ID, "includeTurns": false}, &existing); err == nil && existing.Thread.ID == record.ID {
			if existing.Thread.Provider == provider {
				target.ID = record.ID
				return target, nil
			}
			forkSource = record.ID
		}
		if err != nil && !missingCodexThread(err) {
			return Session{}, err
		}
		if err == nil && existing.Thread.ID != record.ID {
			return Session{}, errors.New("Codex 续接记录与会话不匹配")
		}
	}
	params := map[string]any{"threadId": forkSource, "modelProvider": provider, "cwd": s.Dir, "excludeTurns": true, "deferGoalContinuation": true}
	if config.Config.Model != "" {
		params["model"] = config.Config.Model
	}
	var fork codexThread
	var importedTurns []any
	if !sameHome && forkSource == s.ID {
		path, turns, cleanup, importErr := importCodexRollout(ctx, s, executable, home, extraEnv)
		if importErr != nil {
			return Session{}, importErr
		}
		params["path"], importedTurns = path, turns
		// Once forked, this native snapshot may be referenced by its lineage.
		// Only pre-fork failures can safely remove it.
		if err = rpc.call("thread/fork", params, &fork); err != nil {
			cleanup()
			return Session{}, err
		}
	} else {
		if err = rpc.call("thread/fork", params, &fork); err != nil {
			return Session{}, err
		}
	}
	if !validID(fork.Thread.ID) || fork.Thread.ID == s.ID || fork.ModelProvider != provider {
		return Session{}, errors.New("Codex 未能创建当前 provider 的续接会话")
	}
	if importedTurns != nil {
		if err = verifyImportedTurns(rpc, fork.Thread.ID, importedTurns); err != nil {
			_ = rpc.call("thread/archive", map[string]any{"threadId": fork.Thread.ID}, nil)
			return Session{}, err
		}
	}
	target.ID = fork.Thread.ID
	records[key] = continuationRecord{ID: target.ID, Home: home, Provider: provider}
	data, err := json.Marshal(records)
	if err == nil {
		err = atomicfile.Write(statePath, data)
	}
	if err != nil {
		return Session{}, fmt.Errorf("保存 Codex 续接记录：%w", err)
	}
	return target, nil
}
