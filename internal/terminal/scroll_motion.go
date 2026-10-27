package terminal

import (
	"time"

	"github.com/egoist/mygo/ui"
)

const scrollMotionDuration = 32 * time.Millisecond

type scrollMotion struct {
	from               []rowCache
	top, bottom, delta int
	started, inputAt   time.Time
	direction          int
	revision           uint64
	swallowClick       bool
}

func (m *scrollMotion) active(now time.Time) bool {
	return m.delta != 0 && !now.Before(m.started) && now.Sub(m.started) < scrollMotionDuration
}

func (m *scrollMotion) stop() {
	clear(m.from)
	m.from = m.from[:0]
	m.top, m.bottom, m.delta = 0, 0, 0
	m.started = time.Time{}
}

func (m *scrollMotion) input(dy float32, now time.Time) {
	direction := 1
	if dy < 0 {
		direction = -1
	}
	if direction != m.direction {
		m.stop()
	}
	m.direction, m.inputAt = direction, now
}

// Exact rendered-row matches establish movement. Ambiguous repetition,
// larger jumps and unrelated redraws retain immediate presentation.
func scrollRegion(old, next []rowCache, direction int) (top, bottom, delta int) {
	if len(old) != len(next) || len(next) < 6 || direction == 0 {
		return
	}
	best, ambiguous := 0, false
	for d := 1; d <= min(8, len(next)/3); d++ {
		shift := d * direction
		start := -1
		for y := 0; y <= len(next); y++ {
			match := y < len(next) && y+shift >= 0 && y+shift < len(old) && next[y].signature != "" && next[y].signature == old[y+shift].signature
			if match {
				if start < 0 {
					start = y
				}
				continue
			}
			if start < 0 {
				continue
			}
			length := y - start
			unique := false
			changed := 0
			for i := start; i < y; i++ {
				if old[i].signature != next[i].signature {
					changed++
				}
			}
			for i := start + 1; i < y; i++ {
				unique = unique || next[i].signature != next[start].signature
			}
			if length >= 3 && unique && changed >= 3 {
				if length > best {
					best, ambiguous = length, false
					top, bottom, delta = max(0, start+min(shift, 0)), min(len(next), y+max(shift, 0)), shift
				} else if length == best && shift != delta {
					ambiguous = true
				}
			}
			start = -1
		}
	}
	if ambiguous {
		return 0, 0, 0
	}
	for top < bottom && old[top].signature == next[top].signature {
		top++
	}
	for bottom > top && old[bottom-1].signature == next[bottom-1].signature {
		bottom--
	}
	if bottom-top < 4 {
		return 0, 0, 0
	}
	return
}

func (v *view) startScrollMotion() {
	m := &v.motion
	if v.selecting || v.touchSelecting || v.preedit != "" || v.hover.valid() || len(v.images) != 0 || len(v.searchRects) != 0 {
		m.stop()
		return
	}
	for _, rows := range [][]rowCache{m.from, v.lines} {
		for _, row := range rows {
			if row.sel || !row.shared || row.signature == "" {
				m.stop()
				return
			}
		}
	}
	m.top, m.bottom, m.delta = scrollRegion(m.from, v.lines, m.direction)
	if m.delta == 0 || v.cursor.Visible && v.cursor.InView && v.cursor.Y >= m.top && v.cursor.Y < m.bottom {
		m.stop()
		return
	}
	m.started = v.frameNow
}

func (v *view) movingRow(y int) bool {
	return v.motion.delta != 0 && y >= v.motion.top && y < v.motion.bottom
}

func (v *view) drawScrollMotion(p *ui.Painter, r ui.Rect, ox, oy, cw, ch, base float32) {
	m := &v.motion
	if m.delta == 0 {
		return
	}
	remaining := float32(1 - float64(v.frameNow.Sub(m.started))/float64(scrollMotionDuration))
	offset := remaining * float32(m.delta) * ch
	clip := ui.Rect{X: r.X, Y: oy + float32(m.top)*ch, W: r.W, H: float32(m.bottom-m.top) * ch}
	p.Clip(clip, 0, func() {
		for layer, rows := range [][]rowCache{m.from, v.lines} {
			dy := offset
			if layer == 0 {
				dy -= float32(m.delta) * ch
			}
			for y := m.top; y < m.bottom; y++ {
				ry := oy + float32(y)*ch + dy
				p.Fill(ui.Rect{X: r.X, Y: ry, W: r.W, H: ch}, color(v.colors.Background), 0)
				v.drawRowBackground(p, r, &rows[y], ox, ry, cw, ch, ui.Color{})
				v.drawText(p, &rows[y], ox, ry, cw, ch, base, ui.Color{})
			}
		}
	})
}
