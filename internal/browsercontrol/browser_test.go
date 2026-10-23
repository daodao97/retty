package browsercontrol

import (
	"strings"
	"testing"
)

func TestURLBoundaryAndRemoteOrigins(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.com", "http://localhost:0", "http://localhost:65536", "http://localhost:abc", "http://example.com/\nnext"} {
		if _, err := NormalizeURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for raw, want := range map[string]int{"localhost:5173/path?q=1#hash": 5173, "http://127.0.0.1/": 80, "https://[::1]/": 443} {
		u, err := NormalizeURL(raw)
		port, ok := LoopbackPort(u)
		if err != nil || !ok || port != want {
			t.Fatal(raw, u, port, err)
		}
	}
	if _, ok := LoopbackPort("https://localhost.example.com:5173"); ok {
		t.Fatal("forwarded a non-loopback host")
	}
	if got := RemapOrigin("http://127.0.0.1:49152/path?q=1#hash", "http://127.0.0.1:49152", "http://localhost:5173"); got != "http://localhost:5173/path?q=1#hash" {
		t.Fatal(got)
	}
	for _, raw := range []string{"http://127.0.0.1:49153/path", "https://127.0.0.1:49152/path"} {
		if got := RemapOrigin(raw, "http://127.0.0.1:49152", "http://localhost:5173"); got != raw {
			t.Fatal("mapped a different origin", got)
		}
	}
}

func TestRequestLimitsBeforeBrowserWork(t *testing.T) {
	for _, r := range []Request{{Command: "eval"}, {Command: "click"}, {Command: "read", ID: strings.Repeat("x", 129)}, {Command: "wait", TimeoutMS: 120001}, {Command: "type", Selector: "#name", Text: strings.Repeat("x", 65537)}, {Command: "open", URL: "file:///tmp/page"}} {
		if r.Validate() == nil {
			t.Fatal("accepted invalid request", r.Command)
		}
	}
	// Literal quotes, newlines, backticks and script-looking text remain data.
	r := Request{Command: "type", Selector: "#a\"\\b", Text: "` ${x}\n\";throw new Error('injected');//"}
	js, err := r.Script()
	if err != nil || strings.Contains(js, "value=`") || !strings.Contains(js, `\n\";throw`) || !strings.Contains(js, `querySelectorAll("#a\"\\b")`) {
		t.Fatal(js, err)
	}
}
