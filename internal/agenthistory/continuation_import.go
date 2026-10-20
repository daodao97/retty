package agenthistory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// importCodexRollout uses native reads and forks; it never patches Codex's
// SQLite schema or copies auth/config. The snapshot stays in CODEX_HOME because
// paginated forks can retain a lineage reference to it.
func importCodexRollout(ctx context.Context, s Session, executable, targetHome string, extraEnv []string) (path string, turns []any, cleanup func(), err error) {
	source, err := openCodexRPC(ctx, executable, canonicalHome(s.ConfigDir), s.Dir, extraEnv)
	if err != nil {
		return "", nil, nil, err
	}
	defer source.close()
	var original struct {
		Thread struct {
			ID    string `json:"id"`
			Path  string `json:"path"`
			Turns []any  `json:"turns"`
		} `json:"thread"`
	}
	if err = source.call("thread/read", map[string]any{"threadId": s.ID, "includeTurns": true}, &original); err != nil {
		return "", nil, nil, err
	}
	if original.Thread.ID != s.ID || len(original.Thread.Turns) == 0 {
		return "", nil, nil, errors.New("Codex 未提供完整会话历史，无法在当前配置目录中安全续接")
	}
	for _, turn := range original.Thread.Turns {
		if value, ok := turn.(map[string]any); ok && value["itemsView"] != nil && value["itemsView"] != "full" {
			return "", nil, nil, errors.New("Codex 返回了分页历史摘要，请升级 Codex 后重试跨目录续接")
		}
	}
	path = original.Thread.Path
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") {
		return "", nil, nil, errors.New("Codex 会话历史路径无效")
	}
	file, err := os.Open(path)
	compressed := false
	if os.IsNotExist(err) {
		file, err = os.Open(path + ".zst")
		compressed = true
	}
	if err != nil {
		return "", nil, nil, fmt.Errorf("读取 Codex 原会话：%w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, nil, errors.New("Codex 会话历史不是普通文件")
	}
	var reader io.Reader = file
	if compressed {
		decoder, decodeErr := zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(256<<20))
		if decodeErr != nil {
			return "", nil, nil, decodeErr
		}
		defer decoder.Close()
		reader = decoder
	}
	root := filepath.Join(targetHome, "sessions")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", nil, nil, err
	}
	dir, err := os.MkdirTemp(root, "retty-import-")
	if err != nil {
		return "", nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, filepath.Base(path))
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	const limit = 256 << 20
	n, err := io.Copy(output, io.LimitReader(reader, limit+1))
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if n > limit {
		err = errors.New("Codex 会话历史超过 256MB，无法自动跨目录续接")
	}
	if err != nil {
		cleanup()
		return "", nil, nil, err
	}
	return path, original.Thread.Turns, cleanup, nil
}

func verifyImportedTurns(rpc *codexRPC, id string, source []any) error {
	var imported struct {
		Thread struct {
			Turns []any `json:"turns"`
		} `json:"thread"`
	}
	if err := rpc.call("thread/read", map[string]any{"threadId": id, "includeTurns": true}, &imported); err != nil {
		return err
	}
	// Decode both native views the same way; IDs, tool items, ordering and every
	// turn must survive. Refuse partial or stale archives rather than lose context.
	if !reflect.DeepEqual(source, imported.Thread.Turns) {
		return errors.New("Codex 跨目录续接的历史校验未通过，已保留原会话；当前版本无法完整导入此会话")
	}
	return nil
}
