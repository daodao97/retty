package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestViewsUseStyleTokens keeps colors and type sizes in style.go, as
// docs/ui-design.md asks: a view names a token, not a value.
func TestViewsUseStyleTokens(t *testing.T) {
	// Whole files whose literals are data, not chrome: the tokens themselves,
	// the programs' brand colors and the QR code's black on white.
	skipFiles := map[string]bool{"style.go": true, "programs.go": true, "pairing.go": true}
	// Lines whose literal is the thing's own color, not the app's.
	allowed := []string{
		"IconColor:",      // a pane kind's brand color, as programs.go keeps
		`Key("phone-qr")`, // a QR code reads on white in either window
	}
	color := regexp.MustCompile(`ui\.(Hex|RGBA|RGB)\(`)
	size := regexp.MustCompile(`FontSize\(\s*[0-9.]+\s*\)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || skipFiles[f] {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
	lines:
		for i, line := range strings.Split(string(data), "\n") {
			if !color.MatchString(line) && !size.MatchString(line) {
				continue
			}
			for _, a := range allowed {
				if strings.Contains(line, a) && !size.MatchString(line) {
					continue lines
				}
			}
			t.Errorf("%s:%d uses a literal color or type size; add a token to style.go: %s", f, i+1, strings.TrimSpace(line))
		}
	}
}
