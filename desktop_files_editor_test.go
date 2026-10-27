package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopFilePreviewKeepsLargeTextAndUnknownLanguages(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	dir := t.TempDir()
	text := "\t" + strings.Repeat("x", 17000) + "\r\n" + strings.Repeat("line\r\n", 20001) + "末行\r\n"
	path := filepath.Join(dir, "large.unknown-type")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a, tt := newStaticTestApp(t)
	a.tab().Focus.terminal().info.Dir = dir
	a.toggleFiles()
	t.Cleanup(func() {
		if a.focusedFiles() != nil {
			a.focusedFiles().stop()
		}
	})
	tt.Frame()
	waitFor(t, tt, "directory", func() bool { return a.focusedFiles() != nil && !a.focusedFiles().loading })
	if err := tt.Click("large.unknown-type"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "large text", func() bool { return !a.focusedFiles().previewLoading && a.focusedFiles().previewCode != nil })
	state := a.focusedFiles().previewCode
	if state.Buffer.String() != strings.ReplaceAll(text, "\r\n", "\n") || state.Buffer.Language() != nil || a.focusedFiles().previewTruncated {
		t.Fatal("large plain-text preview truncated lines or changed their content")
	}
	chooseFileAction(t, tt, "复制预览内容")
	if tt.Clipboard() != text {
		t.Fatal("copy did not retain the original tabs and CRLF endings")
	}
	if err := tt.Click("empty.txt"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "empty text", func() bool {
		return !a.focusedFiles().previewLoading && a.focusedFiles().previewCode != nil && a.focusedFiles().previewCode != state
	})
	if a.focusedFiles().previewCode.Buffer.String() != "" || a.focusedFiles().previewDiff != nil || a.focusedFiles().previewImage != nil || !tt.HasText("文件预览") {
		t.Fatal("empty file retained the previous preview")
	}
}
