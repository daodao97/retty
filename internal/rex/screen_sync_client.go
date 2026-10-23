package rex

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// PreferScreenSync opts into acknowledged viewport states. Call before the
// lazy attachment starts. Unsupported servers retain their existing protocol.
func (s *Stream) PreferScreenSync(inputOnly bool) *Stream {
	s.mu.Lock()
	s.wantScreenSync, s.inputOnly = true, inputOnly
	s.mu.Unlock()
	return s
}

func (s *Stream) syncEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screenSync
}

// ScreenStatus describes the last completely reconstructed server state,
// including heartbeats for legitimately quiet terminals. It is not an input ack.
func (s *Stream) ScreenStatus() (enabled bool, confirmed time.Time, revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screenSync, s.stateConfirmed, s.stateRevision
}

func (s *Stream) writeStateMessage(conn net.Conn, kind byte, data []byte) error {
	s.stateWriteMu.Lock()
	defer s.stateWriteMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	return writeAll(conn, syncMessage(kind, data))
}

func (s *Stream) acknowledgeState(conn net.Conn) error {
	if s.stateAck == 0 {
		return nil
	}
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], s.stateAck)
	s.stateAck = 0
	return s.writeStateMessage(conn, screenAck, data[:])
}

func (s *Stream) readScreenState(conn net.Conn) ([]byte, int, int, error) {
	for {
		// The previous result has now been consumed by the sole reader. Do
		// not let fast network reads outrun terminal decoding and application.
		if err := s.acknowledgeState(conn); err != nil {
			return nil, 0, 0, err
		}
		var header [8]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return nil, 0, 0, err
		}
		length := int(binary.BigEndian.Uint32(header[:4]))
		cols, rows := int(binary.BigEndian.Uint16(header[4:6])), int(binary.BigEndian.Uint16(header[6:8]))
		if length < screenStateHeader || length > screenGraphicsLimit+screenStateHeader || cols == 0 || rows == 0 {
			return nil, 0, 0, errors.New("rex: invalid screen state frame")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, 0, 0, err
		}
		kind, seq := payload[0], binary.BigEndian.Uint64(payload[1:9])
		revision := binary.BigEndian.Uint64(payload[9:17])
		body := payload[17:]
		if seq != s.stateSequence+1 {
			return nil, 0, 0, errors.New("rex: screen state sequence mismatch")
		}
		var next []byte
		switch kind {
		case screenFull:
			if len(body) == 0 {
				return nil, 0, 0, errors.New("rex: empty screen state")
			}
			next = body
		case screenDelta:
			if s.stateData == nil || cols != s.stateCols || rows != s.stateRows {
				return nil, 0, 0, errors.New("rex: screen delta without matching base")
			}
			var err error
			next, err = applyScreenDifference(s.stateData, body)
			if err != nil || len(next) == 0 {
				return nil, 0, 0, errors.New("rex: invalid screen state delta")
			}
		case screenHeartbeat:
			if len(body) != 0 || s.stateData == nil {
				return nil, 0, 0, errors.New("rex: invalid screen heartbeat")
			}
		case screenEffect:
			if s.stateData == nil || len(body) == 0 || len(body) > screenStateLimit {
				return nil, 0, 0, errors.New("rex: invalid screen effect")
			}
			s.stateEffects = body
		default:
			return nil, 0, 0, errors.New("rex: unknown screen state kind")
		}
		s.stateSequence, s.stateAck = seq, seq
		s.stateCols, s.stateRows = cols, rows
		if kind != screenEffect {
			s.mu.Lock()
			s.stateConfirmed, s.stateRevision = time.Now(), revision
			s.mu.Unlock()
		}
		if kind == screenEffect {
			return nil, cols, rows, nil
		}
		if next == nil {
			if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
				return nil, 0, 0, err
			}
			continue
		}
		s.stateData = next
		if s.OnData != nil {
			s.OnData(length + 8)
		}
		return next, cols, rows, nil
	}
}

// ReadScreenUpdate exposes complete state replacements to native terminal
// readers, and ordinary ANSI chunks for a legacy server.
func (s *Stream) ReadScreenUpdate(p []byte) (int, int, int, []byte, error) {
	conn, err := s.wait()
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if s.syncEnabled() {
		if len(s.stateEffects) > 0 {
			n := copy(p, s.stateEffects)
			s.stateEffects = s.stateEffects[n:]
			return n, s.stateCols, s.stateRows, nil, nil
		}
		if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return 0, 0, 0, nil, err
		}
		data, cols, rows, err := s.readScreenState(conn)
		if len(s.stateEffects) > 0 {
			n := copy(p, s.stateEffects)
			s.stateEffects = s.stateEffects[n:]
			return n, cols, rows, nil, err
		}
		return 0, cols, rows, data, err
	}
	n, cols, rows, err := s.ReadScreen(p)
	return n, cols, rows, nil, err
}

func (s *Stream) readStateBytes(conn net.Conn, p []byte) (int, int, int, error) {
	if len(s.statePending) == 0 {
		if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return 0, 0, 0, err
		}
		data, _, _, err := s.readScreenState(conn)
		if err != nil {
			return 0, 0, 0, err
		}
		if len(s.stateEffects) > 0 {
			s.statePending = s.stateEffects
			s.stateEffects = nil
		} else {
			// Byte-stream consumers still need a complete replacement, rather
			// than appending a visible snapshot to an old screen.
			s.statePending = append([]byte("\x1b[?2026h\x18\x1b[?6l\x1b[r\x1b[H\x1b[2J\x1b[3J"), data...)
			s.statePending = append(s.statePending, []byte("\x1b[?2026l")...)
		}
	}
	n := copy(p, s.statePending)
	s.statePending = s.statePending[n:]
	return n, s.stateCols, s.stateRows, nil
}

// History uses an independent, bounded connection so a large response never
// delays liveness polling, input, or the live screen's acknowledgements.
func (c *Client) History(ctx context.Context, sid string, rows int) (HistorySnapshot, error) {
	var history HistorySnapshot
	if rows <= 0 || rows > 8192 {
		return history, errors.New("history rows must be between 1 and 8192")
	}
	conn, err := c.DialExtension(ctx)
	if err != nil {
		return history, err
	}
	next := NewClient(conn, nil)
	defer next.Close()
	stop := context.AfterFunc(ctx, func() { next.Close() })
	defer stop()
	err = next.call(Request{Op: "history", SID: sid, HistoryRows: rows}, &history)
	if ctx.Err() != nil {
		return HistorySnapshot{}, ctx.Err()
	}
	if err == nil && (len(history.Data) > 8<<20 || history.Cols <= 0 || history.Rows <= 0) {
		return HistorySnapshot{}, errors.New("rex: invalid history snapshot")
	}
	return history, err
}
