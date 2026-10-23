package controlcli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"retty/internal/browsercontrol"
	"retty/internal/desktopcontrol"
)

func TestBrowserCLISelectsOwningGUIAndPreservesLiteralInput(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	t.Setenv("RETTY_DIR", "")
	t.Setenv("RETTY_AGENT_SOCKET", filepath.Join(dir, "server.sock"))
	requests := make(chan browsercontrol.Request, 8)
	s, err := desktopcontrol.Listen(dir, func(_ context.Context, r desktopcontrol.Request) (desktopcontrol.Result, error) {
		requests <- *r.Browser
		return desktopcontrol.Result{Browser: &browsercontrol.Result{Browser: &browsercontrol.Info{ID: "web-owned"}, PNG: []byte("fixture-bytes")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := func(args ...string) error {
		return Run(context.Background(), append([]string{"browser"}, args...), strings.NewReader(""), io.Discard, io.Discard, Options{DefaultDir: other})
	}
	literal := "'\"` $(touch never)\nnext"
	if err := run("type", "web-owned", "--text", literal, "--selector", "#name"); err != nil {
		t.Fatal(err)
	}
	if r := <-requests; r.ID != "web-owned" || r.Text != literal || r.Selector != "#name" {
		t.Fatal(r)
	}
	output := filepath.Join(t.TempDir(), "page.png")
	if err := run("screenshot", "web-owned", "--output", output, "--data-dir", dir); err != nil {
		t.Fatal(err)
	}
	<-requests
	data, err := os.ReadFile(output)
	st, _ := os.Stat(output)
	if err != nil || !bytes.Equal(data, []byte("fixture-bytes")) || st.Mode().Perm() != 0600 {
		t.Fatal(string(data), st, err)
	}
	t.Setenv("RETTY_AGENT_SOCKET", "/tmp/retty-hashed.sock")
	if err := run("list"); err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatal("guessed GUI for hashed agent socket", err)
	}
	if err := run("list", "--data-dir", dir); err != nil {
		t.Fatal(err)
	}
	<-requests
	for _, args := range [][]string{{"wait", "web-owned", "--timeout", "1ns"}, {"list", "extra"}, {"close", "web-owned", "extra"}, {"screenshot", "web-owned"}, {"type", "web-owned", "--selector"}} {
		if err := run(args...); err == nil {
			t.Fatal("accepted invalid arguments", args)
		}
	}
}
