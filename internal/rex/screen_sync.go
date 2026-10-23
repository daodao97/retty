package rex

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	screenFull          byte = 1
	screenDelta         byte = 2
	screenHeartbeat     byte = 3
	screenInput         byte = 4
	screenAck           byte = 5
	screenEffect        byte = 6
	screenStateLimit         = 4 << 20
	screenGraphicsLimit      = 16 << 20
	screenStateHeader        = 17 // kind, sequence, authoritative revision
)

type HistorySnapshot struct {
	Data        []byte `json:"data"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	Revision    uint64 `json:"revision"`
	Output      uint64 `json:"output"`
	HistoryRows int    `json:"history_rows"`
}

// Deltas transform serialized state, never an arbitrary ANSI byte stream.
// The receiver reconstructs the entire snapshot before the emulator sees it.
func screenDifference(previous, next []byte) (byte, []byte) {
	prefix := 0
	for prefix < min(len(previous), len(next)) && previous[prefix] == next[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < min(len(previous), len(next))-prefix && previous[len(previous)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	middle := next[prefix : len(next)-suffix]
	if previous == nil || 8+len(middle) >= len(next) {
		return screenFull, next
	}
	delta := make([]byte, 8+len(middle))
	binary.BigEndian.PutUint32(delta[:4], uint32(prefix))
	binary.BigEndian.PutUint32(delta[4:8], uint32(suffix))
	copy(delta[8:], middle)
	return screenDelta, delta
}

func applyScreenDifference(previous, delta []byte) ([]byte, error) {
	if len(delta) < 8 {
		return nil, errors.New("rex: incomplete screen delta")
	}
	prefix, suffix := int(binary.BigEndian.Uint32(delta[:4])), int(binary.BigEndian.Uint32(delta[4:8]))
	if prefix > len(previous) || suffix > len(previous)-prefix || prefix+suffix+len(delta)-8 > screenGraphicsLimit {
		return nil, errors.New("rex: invalid screen delta")
	}
	next := make([]byte, 0, prefix+suffix+len(delta)-8)
	next = append(next, previous[:prefix]...)
	next = append(next, delta[8:]...)
	return append(next, previous[len(previous)-suffix:]...), nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrNoProgress
		}
		data = data[n:]
	}
	return nil
}

func syncMessage(kind byte, data []byte) []byte {
	packet := make([]byte, 5+len(data))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(data)))
	packet[4] = kind
	copy(packet[5:], data)
	return packet
}
