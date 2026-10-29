package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
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

// TestStateColorsReadAsText keeps the state colors and the faintest text
// legible on the grounds they sit on, in either window: a status pill's
// label is its state's color.
func TestStateColorsReadAsText(t *testing.T) {
	for _, w := range []struct {
		name    string
		k       *colors
		grounds map[string]ui.Color
	}{
		{"light", &lightColors, map[string]ui.Color{"terminal": lightTerm.Background, "sidebar": lightColors.sidebar}},
		{"dark", &darkColors, map[string]ui.Color{"terminal": darkTerm.Background, "sidebar": darkColors.sidebar}},
	} {
		tones := map[string]ui.Color{"info": w.k.info, "success": w.k.success, "warning": w.k.warning, "danger": w.k.danger, "textFaint": w.k.textFaint}
		for name, tone := range tones {
			for ground, bg := range w.grounds {
				if got := colorContrast(tone, bg); got < 4.5 {
					t.Errorf("%s %s on the %s is %.2f:1, below 4.5:1", w.name, name, ground, got)
				}
			}
		}
	}
}
