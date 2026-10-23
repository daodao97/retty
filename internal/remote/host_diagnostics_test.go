package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"retty/internal/diagnostics"
)

func TestHostDiagnosticsUsesAuthenticatedBridgeWithoutDaemon(t *testing.T) {
	c, _, socket, _ := fileUploadFixture(t)
	os.Remove(socket)
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/missing/remote-hook"}]}]}}`), 0600)
	r, err := DiagnoseHost(context.Background(), c, diagnostics.Request{Agent: "codex", ConfigDir: dir, CWD: filepath.Join(dir, "moved-project")})
	if err != nil || r.Config.Path != path || r.CWD.Code != "missing" || len(r.Hooks) != 1 || r.Hooks[0].Executable.Path != "/missing/remote-hook" || r.Hooks[0].Executable.Code != "missing" {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DiagnoseHost(ctx, c, diagnostics.Request{Agent: "codex"}); err == nil {
		t.Fatal("canceled remote diagnostic ran")
	}
}
