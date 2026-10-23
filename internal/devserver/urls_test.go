package devserver

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdvertisedURLsNormalizeWildcardsAndKeepLatestPerOrigin(t *testing.T) {
	text := "Local: http://localhost:5173/\nNetwork: http://192.168.1.1:5173/\nListening on 0.0.0.0:3000\nhttps://[::1]:8443/path?q=a#b\nhttp://localhost:5173/new.\nhttp://[::]:4321/"
	got := Extract(text)
	want := []string{"http://localhost:4321/", "http://localhost:5173/new", "https://[::1]:8443/path?q=a#b", "http://localhost:3000"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i, u := range got {
		if u.URL != want[i] {
			t.Fatal(i, u.URL, want[i])
		}
	}
}

func TestDoesNotExtractOtherHostsCredentialsOrInvalidPorts(t *testing.T) {
	for _, text := range []string{"http://evil.localhost:3000", "http://localhost.evil:3000", "http://user:pass@localhost:3000", "http://example.com/localhost:3000", "127.999.0.1:3000", "localhost:0", "localhost:65536", "localhost:300000", "localhost:3000abc", "127.0.0.1:3.14", "file://localhost:3000/path"} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("extracted from %q: %+v", text, got)
		}
	}
	if len(Extract("http://127.0.0.2:3000/")) != 1 {
		t.Fatal("valid loopback alias missing")
	}
}

func TestCandidateLimitsAndPlainURLsStayDistinctByScheme(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "localhost:%d\n", 2000+i)
	}
	if len(Extract(b.String())) != MaxURLs {
		t.Fatal("URL list is not bounded")
	}
	if got := Extract("localhost:3000\nhttps://localhost:3000"); len(got) != 2 || got[0].Label == got[1].Label {
		t.Fatal("scheme was lost", got)
	}
}
