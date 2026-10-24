// Package approval transports pending, structured Agent decisions. It never
// reads terminal output or writes terminal input. Requests live only in memory.
package approval

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrStale = errors.New("授权请求已处理或失效，请查看当前会话")

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Request struct {
	ID       string    `json:"id"`
	SID      string    `json:"sid"`
	Agent    string    `json:"agent"`
	ThreadID string    `json:"threadID"`
	TurnID   string    `json:"turnID,omitempty"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail"`
	Options  []Option  `json:"options"`
	Expires  time.Time `json:"expires"`
}

type entry struct {
	request Request
	answer  func(string) error
}
type Store struct {
	mu      sync.Mutex
	prefix  string
	entries map[string]entry
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func NewStore() *Store { return &Store{prefix: randomID(8), entries: map[string]entry{}} }

// Add binds displayed option IDs to a producer-owned callback. Clients cannot
// supply RPC responses, tool parameters, permission rules, or a socket path.
func (s *Store) Add(r Request, answer func(string) error) (string, error) {
	if r.SID == "" || r.ThreadID == "" || (r.Agent != "codex" && r.Agent != "claude") || len(r.Options) < 2 || len(r.Options) > 9 || len(r.Detail) > 64<<10 || len(r.Title) > 1024 || answer == nil {
		return "", errors.New("invalid structured approval")
	}
	seen := map[string]bool{}
	for _, o := range r.Options {
		if o.ID == "" || len(o.ID) > 128 || o.Label == "" || len(o.Label) > 1024 || seen[o.ID] {
			return "", errors.New("invalid approval option")
		}
		seen[o.ID] = true
	}
	if r.Expires.IsZero() {
		r.Expires = time.Now().Add(2 * time.Hour)
	}
	r.Options = append([]Option(nil), r.Options...)
	r.ID = s.prefix + "/" + randomID(16)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= 32 {
		return "", errors.New("too many pending approvals")
	}
	s.entries[r.ID] = entry{r, answer}
	return r.ID, nil
}

func (s *Store) Remove(id string) { s.mu.Lock(); delete(s.entries, id); s.mu.Unlock() }
func (s *Store) List(sid, thread string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for id, e := range s.entries {
		if time.Now().After(e.request.Expires) {
			delete(s.entries, id)
			continue
		}
		if e.request.SID == sid && (thread == "" || e.request.ThreadID == thread) {
			r := e.request
			r.Options = append([]Option(nil), r.Options...)
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Store) Answer(sid, thread, id, choice string) error {
	s.mu.Lock()
	e, ok := s.entries[id]
	if !ok || e.request.SID != sid || e.request.ThreadID != thread || time.Now().After(e.request.Expires) {
		s.mu.Unlock()
		return ErrStale
	}
	valid := false
	for _, o := range e.request.Options {
		valid = valid || o.ID == choice
	}
	if !valid {
		s.mu.Unlock()
		return errors.New("invalid approval choice")
	}
	delete(s.entries, id)
	s.mu.Unlock()
	return e.answer(choice)
}
