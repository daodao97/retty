//go:build darwin || linux

package rex

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const screenWriteTimeout = 15 * time.Second
const screenHeartbeatInterval = 2 * time.Second

// Each viewer has one unacknowledged state and a single wake token. PTY
// output only dirties the authoritative screen, so it cannot grow this queue.
type screenPeer struct {
	conn       net.Conn
	wake       chan struct{}
	acks       chan uint64
	stop       chan struct{}
	once       sync.Once
	inputOnly  bool
	events     chan []byte
	eventBytes int // guarded by session.mu, includes the in-flight event
}

func (p *screenPeer) close() { p.once.Do(func() { close(p.stop); p.conn.Close() }) }

func (s *session) notifyScreensLocked() {
	for p := range s.screenClients {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (s *session) sendScreenEffectLocked(data []byte) {
	for p := range s.screenClients {
		if len(data) == 0 || p.eventBytes+len(data) > screenStateLimit {
			p.close()
			delete(s.screenClients, p)
			continue
		}
		select {
		case p.events <- data:
			p.eventBytes += len(data)
		default:
			p.close()
			delete(s.screenClients, p)
		}
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (s *session) viewport() ([]byte, int, int, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, title := s.finalViewport, s.finalTitle
	if !s.exited {
		if source, ok := s.vt.(interface{ SnapshotReady() bool }); ok && !source.SnapshotReady() {
			return nil, s.cols, s.rows, s.screenRevision, false
		}
		data, title = s.vt.SnapshotRecent(-1), s.vt.Title()
	}
	// State frames are already applied atomically. The program's rendering
	// transaction must not leave a fresh receiver holding its own paint lock.
	data = bytes.ReplaceAll(data, []byte("\x1b[?2026h"), []byte("\x1b[?2026l"))
	if title != "" {
		data = append([]byte("\x1b]2;"+title+"\x07"), data...)
	}
	return data, s.cols, s.rows, s.screenRevision, s.exited
}

func (s *session) attachScreenSync(conn net.Conn, r *bufio.Reader, cols, rows int, inputOnly bool) {
	p := &screenPeer{conn: conn, wake: make(chan struct{}, 1), acks: make(chan uint64, 1), stop: make(chan struct{}), inputOnly: inputOnly, events: make(chan []byte, 32)}
	s.mu.Lock()
	if cols > 0 && rows > 0 && s.sizeLock == "" {
		s.resizeLocked(cols, rows)
	}
	if s.screenClients == nil {
		s.screenClients = make(map[*screenPeer]struct{})
	}
	s.screenClients[p] = struct{}{}
	s.mu.Unlock()
	writerDone := make(chan struct{})
	defer func() {
		p.close()
		<-writerDone
		s.mu.Lock()
		delete(s.screenClients, p)
		s.mu.Unlock()
	}()
	go func() { s.writeScreens(p); close(writerDone) }()
	for {
		var header [5]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return
		}
		length, kind := int(binary.BigEndian.Uint32(header[:4])), header[4]
		if kind != screenInput && kind != screenAck || length > 32<<10 || kind == screenAck && length != 8 || kind == screenInput && length == 0 {
			return
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			return
		}
		if kind == screenAck {
			select {
			case p.acks <- binary.BigEndian.Uint64(data):
			case <-p.stop:
				return
			}
		} else {
			s.input(data)
		}
	}
}

func (s *session) writeScreens(p *screenPeer) {
	defer p.close()
	var previous []byte
	var seq, lastRevision uint64
	var lastCols, lastRows int
	interval := 16 * time.Millisecond
	for {
		data, cols, rows, revision, exited := s.viewport()
		if data == nil && !exited {
			if previous == nil {
				timer := time.NewTimer(interval)
				select {
				case <-timer.C:
				case <-p.stop:
					timer.Stop()
					return
				}
				continue
			}
			data, cols, rows, revision = previous, lastCols, lastRows, lastRevision
		}
		if len(data) == 0 || len(data) > screenStateLimit || cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
			return
		}
		kind, body := byte(screenHeartbeat), []byte(nil)
		var event []byte
		changed := previous == nil || !p.inputOnly && (cols != lastCols || rows != lastRows || !bytes.Equal(data, previous))
		if changed {
			base := previous
			if cols != lastCols || rows != lastRows {
				base = nil
			}
			kind, body = screenDifference(base, data)
		}
		if previous != nil {
			select {
			case event = <-p.events:
				kind, body = screenEffect, event
				changed = false
			default:
			}
		}
		seq++
		frameCols, frameRows, frameRevision := cols, rows, revision
		if event != nil {
			frameCols, frameRows, frameRevision = lastCols, lastRows, lastRevision
		}
		frame := make([]byte, 8+screenStateHeader+len(body))
		binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)-8))
		binary.BigEndian.PutUint16(frame[4:6], uint16(frameCols))
		binary.BigEndian.PutUint16(frame[6:8], uint16(frameRows))
		frame[8] = kind
		binary.BigEndian.PutUint64(frame[9:17], seq)
		binary.BigEndian.PutUint64(frame[17:25], frameRevision)
		copy(frame[25:], body)
		started := time.Now()
		if err := p.conn.SetWriteDeadline(started.Add(screenWriteTimeout)); err != nil {
			return
		}
		if err := writeAll(p.conn, frame); err != nil {
			return
		}
		if changed {
			previous, lastCols, lastRows = data, cols, rows
		}
		if event == nil {
			lastRevision = revision
		}
		if exited && event == nil {
			return
		}
		deadline := time.NewTimer(screenWriteTimeout)
		select {
		case ack := <-p.acks:
			deadline.Stop()
			if ack != seq {
				return
			}
		case <-deadline.C:
			return
		case <-p.stop:
			deadline.Stop()
			return
		}
		if event != nil {
			s.mu.Lock()
			p.eventBytes -= len(event)
			s.mu.Unlock()
		}
		// Pace frame starts, counting delivery and receiver processing toward
		// the interval. Waiting a full interval after the ack stacks an idle gap
		// on top of network/decoding time and delays interactive scrolling.
		// At most one frame is in flight; queued mutations still collapse into
		// the next authoritative snapshot.
		interval = max(16*time.Millisecond, min((interval*3+time.Since(started)/2)/4, time.Second))
		pace := time.NewTimer(max(interval-time.Since(started), 0))
		select {
		case <-pace.C:
		case <-p.stop:
			pace.Stop()
			return
		}
		s.mu.Lock()
		dirty := s.screenRevision != lastRevision || s.exited
		s.mu.Unlock()
		if dirty && !p.inputOnly || len(p.events) > 0 {
			continue
		}
		idle := time.NewTimer(screenHeartbeatInterval)
	wait:
		for {
			select {
			case <-p.wake:
				s.mu.Lock()
				dirty, ended := s.screenRevision != lastRevision, s.exited
				s.mu.Unlock()
				if ended || dirty && !p.inputOnly || len(p.events) > 0 {
					break wait
				}
			case <-idle.C:
				break wait
			case <-p.stop:
				idle.Stop()
				return
			}
		}
		idle.Stop()
	}
}

func (s *session) history(rows int) (HistorySnapshot, error) {
	if rows <= 0 || rows > 8192 {
		return HistorySnapshot{}, errors.New("history rows must be between 1 and 8192")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.finalScreen
	if !s.exited {
		data = s.vt.SnapshotRecent(rows)
	}
	if len(data) == 0 {
		data = s.finalViewport
	}
	if len(data) > 8<<20 {
		return HistorySnapshot{}, errors.New("history snapshot exceeds 8 MiB")
	}
	return HistorySnapshot{Data: data, Cols: s.cols, Rows: s.rows, Revision: s.screenRevision, HistoryRows: rows}, nil
}
