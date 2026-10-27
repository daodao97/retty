package main

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"retty/internal/agents"
	"retty/internal/rex"
)

// Read receipts are separate from notification delivery. Receiving a push or
// visiting the session list does not mean the result has been read.
type readReceiptState struct {
	loaded bool
	seen   map[string]time.Time
	retry  map[string]time.Time
}

func resultNoticeID(h rex.Hello, desktop string, s rex.SessionInfo) string {
	if h.Version < 5 {
		s.Agent.CompletionRevision = 0
	}
	return rex.AgentNoticeID(desktop, s)
}

func (in *readReceiptState) unread(id string, s rex.SessionInfo) bool {
	return rex.AttentionState(s) && (s.Agent.State == agents.Waiting || s.AttentionSeen != id && in.seen[id].IsZero())
}

func (in *readReceiptState) remember(id string) {
	if in.seen == nil {
		in.seen = map[string]time.Time{}
	}
	in.seen[id] = time.Now()
	trimReceipts(in.seen, 256)
}

// trimReceipts keeps the newest limit receipts of seen, by when they were
// made, and the first of their ids for those made at once.
func trimReceipts(seen map[string]time.Time, limit int) {
	if len(seen) <= limit {
		return
	}
	keys := slices.Collect(maps.Keys(seen))
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(seen[b].Compare(seen[a]), strings.Compare(a, b))
	})
	for _, k := range keys[limit:] {
		delete(seen, k)
	}
}

// readable tells that the result of a session can be marked read now:
// waiting for input is never read, and a failed sync waits out its retry.
func (in *readReceiptState) readable(id string, s rex.SessionInfo) bool {
	return s.Agent.State != agents.Waiting && in.unread(id, s) && !time.Now().Before(in.retry[id])
}

// markRead keeps the receipt here and, given a client, on the server of
// sid too; persist saves the receipts. If the server cannot take it, post
// runs on the main thread what forgets the receipt and retries it later.
func (in *readReceiptState) markRead(client *rex.Client, sid, id string, persist func(), post func(func())) {
	in.remember(id)
	delete(in.retry, id)
	persist()
	if client == nil {
		return
	}
	go func() {
		if client.MarkAttention(sid, id) == nil {
			return
		}
		post(func() {
			delete(in.seen, id)
			if in.retry == nil {
				in.retry = map[string]time.Time{}
			}
			in.retry[id] = time.Now().Add(30 * time.Second)
			persist()
		})
	}()
}

func (a *App) readResult(p *Pane) {
	if p == nil || p.terminal() == nil || p.closed || p.host != nil && !p.host.connected() {
		return
	}
	h := a.hostHello(p.host)
	id := resultNoticeID(h, h.Host.ID, p.terminal().info)
	if !a.readReceipts.readable(id, p.terminal().info) {
		return
	}
	client := a.paneClient(p)
	if !h.Attention {
		client = nil
	}
	a.readReceipts.markRead(client, p.terminal().SID, id, a.persistReadReceipts, a.desktopDispatcher())
}

func (a *App) loadReadReceipts() {
	if a.readReceipts.loaded {
		return
	}
	a.readReceipts.loaded = true
	// Preserve receipts saved before the standalone pending list was removed.
	data, err := os.ReadFile(filepath.Join(rex.Dir(), "inbox-seen.json"))
	if err == nil && len(data) <= 128<<10 {
		json.Unmarshal(data, &a.readReceipts.seen)
	}
	trimReceipts(a.readReceipts.seen, 256)
}

func (a *App) persistReadReceipts() {
	if a.win == nil {
		return
	} // Headless fixture views never write application data.
	data, _ := json.Marshal(a.readReceipts.seen)
	dir := rex.Dir()
	if a.desktopStorage == nil {
		a.desktopStorage = connectionStorage()
	}
	a.desktopStorage <- func() {
		f, err := os.CreateTemp(dir, ".inbox-*")
		if err != nil {
			return
		}
		defer os.Remove(f.Name())
		_, err = f.Write(data)
		closed := f.Close()
		if err == nil && closed == nil {
			os.Rename(f.Name(), filepath.Join(dir, "inbox-seen.json"))
		}
	}
}
