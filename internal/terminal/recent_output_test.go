package terminal

import (
	"fmt"
	"strings"
	"testing"

	"retty/internal/devserver"
)

func TestRecentOutputBoundsUnwrapsAndDoesNotMoveViewport(t *testing.T) {
	loadLib(t)
	term, err := New(Options{Conn: newPipe()})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	term.Resize(24, 6)
	for i := range 300 {
		term.Feed([]byte(fmt.Sprintf("old row %03d\r\n", i)))
	}
	term.Feed([]byte("Local: \x1b[32mhttp://localhost:5173/a/long/path\x1b[0m\r\n"))
	term.mu.Lock()
	term.term.ScrollBy(-12)
	before := term.term.Scrollbar()
	term.mu.Unlock()
	text, rev, changed := term.RecentOutput(10, 0)
	if !changed || len(text) > 64<<10 || strings.Contains(text, "old row 000") || strings.Contains(text, "\x1b") {
		t.Fatal("tail is unbounded or undecoded", text, rev, changed)
	}
	urls := devserver.Extract(text)
	if len(urls) != 1 || urls[0].URL != "http://localhost:5173/a/long/path" {
		t.Fatal("wrapped advertised URL not restored", text, urls)
	}
	term.mu.Lock()
	after := term.term.Scrollbar()
	term.mu.Unlock()
	if before != after {
		t.Fatal("tail read moved viewport", before, after)
	}
	if text, _, changed := term.RecentOutput(10, rev); changed || text != "" {
		t.Fatal("unchanged output recopied")
	}
	term.Close()
	if text, _, changed := term.RecentOutput(10, 0); changed || text != "" {
		t.Fatal("closed terminal was read")
	}
}
