// Package screen keeps a session's Ghostty screen without a window, renderer,
// input goroutines or a graphics dependency. It retains terminal state and
// captures query replies and one-time effects for the session transport.
package screen

import (
	"bytes"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"retty/internal/terminal/internal/library"
	"retty/internal/terminal/internal/library/desktop"
	"retty/internal/terminal/internal/vt"
)

type Screen struct {
	mu             sync.Mutex
	term           *vt.Terminal
	cols, rows     int
	replies        [][]byte
	effects        []byte
	effectOverflow bool
	held           bool
	heldSince      time.Time
}

func Load() error {
	if vt.Loaded() {
		return nil
	}
	path, err := library.Find(desktop.Manifest)
	if err != nil {
		return err
	}
	return vt.Load(path)
}

func New(cols, rows, scrollback int, bell func()) (*Screen, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	s := &Screen{cols: max(cols, 1), rows: max(rows, 1)}
	t, err := vt.NewTerminal(s.cols, s.rows, vt.Effects{
		WritePTY: func(p []byte) {
			if len(s.replies) < 32 && len(p) <= 4096 {
				s.replies = append(s.replies, bytes.Clone(p))
			}
		},
		RenderHold: func(held bool) {
			s.held = held
			if held {
				s.heldSince = time.Now()
			}
		},
		Bell: func() {
			if bell != nil {
				bell()
			}
			s.addEffect([]byte{7})
		},
		Clipboard: func(text string, primary bool) bool {
			if primary {
				return false
			}
			if len(text) > (3<<20)-16 {
				s.effectOverflow = true
				return false
			}
			s.addEffect([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"))
			return true
		},
		Notify: func(title, body string) {
			clean := func(s string) string {
				return strings.Map(func(r rune) rune {
					if r < 32 || r == 127 {
						return -1
					}
					return r
				}, s)
			}
			if len(title)+len(body) > 4<<20 {
				s.effectOverflow = true
				return
			}
			s.addEffect([]byte("\x1b]777;notify;" + clean(title) + ";" + clean(body) + "\x07"))
		},
		Size: func() (int, int, int, int) { return s.cols, s.rows, 8, 16 },
		Dark: func() bool { return true },
	})
	if err != nil {
		return nil, err
	}
	t.SetTerminfoName("xterm-256color")
	t.SetImageLimit(8 << 20)
	// Kitty placement geometry also needs cell pixels before the first viewer.
	t.Resize(s.cols, s.rows, 8, 16)
	t.SetModeDefault(vt.ModeGraphemes, true)
	t.SetScrollback(scrollback)
	t.SetDefaultCursor(vt.CursorBlock, true)
	s.term = t
	return s, nil
}

func (s *Screen) addEffect(data []byte) {
	if len(s.effects)+len(data) > 4<<20 {
		s.effectOverflow = true
		return
	}
	if !s.effectOverflow {
		s.effects = append(s.effects, data...)
	}
}

func (s *Screen) DrainEffects() ([][]byte, [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replies := s.replies
	var effects [][]byte
	if s.effectOverflow {
		effects = [][]byte{nil}
	} else if len(s.effects) > 0 {
		effects = [][]byte{s.effects}
	}
	s.replies, s.effects = nil, nil
	s.effectOverflow = false
	return replies, effects
}

func (s *Screen) Feed(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term != nil {
		s.term.Write(data)
		if s.held && len(data) > 0 {
			s.heldSince = time.Now()
		}
	}
}

func (s *Screen) SnapshotReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.held || time.Since(s.heldSince) >= 250*time.Millisecond
}

func (s *Screen) Resize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = max(cols, 1), max(rows, 1)
	if s.term != nil {
		s.term.Resize(s.cols, s.rows, 8, 16)
	}
}

func (s *Screen) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil
	}
	return s.term.VT()
}

func (s *Screen) SnapshotRecent(historyRows int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil
	}
	if historyRows < 0 {
		return s.term.VTScreen()
	}
	return s.term.VTRecent(historyRows)
}

// SnapshotGraphicsRecent includes portable Kitty pixel transfers and placements.
// Ordinary history/text readers continue to receive the smaller text snapshot.
func (s *Screen) SnapshotGraphicsRecent(historyRows int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return nil
	}
	return s.term.VTGraphicsRecent(historyRows)
}

func (s *Screen) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ""
	}
	return s.term.Title()
}

func (s *Screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == nil {
		return ""
	}
	return s.term.Text()
}

func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term != nil {
		s.term.Free()
		s.term = nil
	}
	return nil
}
