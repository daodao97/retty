package terminal

import (
	"encoding/binary"
	"unsafe"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal/internal/vt"
)

const rowLayoutLimit = 1 << 20
const rowLayoutKeyLimit = 8 << 10

type rowLayoutContext struct {
	font            fontKey
	cols, cw, ch    int
	scale           float32
	colors          vt.Colors
	adaptive        bool
	opposite        ui.Color
	minimumContrast float64
}

type cachedRowLayout struct {
	row   rowCache
	used  uint64
	bytes int
}

// Entries own immutable row buffers. A viewport can share them across rows
// and complete screen replacements, independent of native row/style IDs.
type rowLayoutCache struct {
	context rowLayoutContext
	entries map[string]*cachedRowLayout
	bytes   int
	clock   uint64
	keyBuf  []byte
	styles  []nativeRowStyle
	runes   []rune
}

type nativeRowStyle struct {
	id    uint16
	style vt.Style
}

func (c *rowLayoutCache) prepare(v *view) bool {
	context := rowLayoutContext{v.font, v.cols, v.cellW, v.cellH, v.scale, v.colors, v.adaptiveColors, v.oppositeBackground, v.theme.MinimumContrast}
	if context != c.context {
		c.context, c.entries, c.bytes = context, nil, 0
		return true
	}
	return false
}

func (c *rowLayoutCache) get(key []byte) (rowCache, bool) {
	if entry := c.entries[string(key)]; entry != nil {
		c.clock++
		entry.used = c.clock
		return entry.row, true
	}
	return rowCache{}, false
}

func (c *rowLayoutCache) put(key []byte, row *rowCache) {
	size := len(key) + int(unsafe.Sizeof(*row)) + 128
	size += cap(row.bgs)*int(unsafe.Sizeof(bgSpan{})) + cap(row.decos)*int(unsafe.Sizeof(deco{}))
	size += cap(row.boxes)*int(unsafe.Sizeof(boxCell{})) + cap(row.wide)*2 + cap(row.runs)*int(unsafe.Sizeof(glyphRun{}))
	for _, run := range row.runs {
		size += cap(run.glyphs)*int(unsafe.Sizeof(ui.Glyph{})) + cap(run.cols)*2
	}
	if size > rowLayoutLimit {
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]*cachedRowLayout)
	}
	for len(c.entries) >= 128 || c.bytes+size > rowLayoutLimit {
		var oldest string
		stamp := ^uint64(0)
		for key, entry := range c.entries {
			if entry.used < stamp {
				oldest, stamp = key, entry.used
			}
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
	c.clock++
	row.shared = true
	row.signature = string(key)
	c.entries[row.signature] = &cachedRowLayout{*row, c.clock, size}
	c.bytes += size
}

// key describes rendered cells, actual styles, graphemes and selection. Native
// style and grapheme IDs can be reused for different content after a reset.
func (c *rowLayoutCache) key(rs *vt.RenderState, cells []vt.Cell, cols int) []byte {
	if cols > 512 {
		return nil
	}
	buf := c.keyBuf[:0]
	c.styles = c.styles[:0]
	a, b, selected := rs.RowSelection()
	buf = binary.LittleEndian.AppendUint32(buf, uint32(a))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(b))
	buf = append(buf, boolByte(selected))
	for x, cell := range cells[:min(len(cells), cols)] {
		var style vt.Style
		if cell.Style != 0 {
			found := false
			for _, cached := range c.styles {
				if cached.id == cell.Style {
					style, found = cached.style, true
					break
				}
			}
			if !found {
				style = rs.CellStyle(x)
				c.styles = append(c.styles, nativeRowStyle{cell.Style, style})
			}
		}
		buf = binary.LittleEndian.AppendUint32(buf, uint32(cell.Codepoint))
		buf = append(buf, byte(cell.Wide), boolByte(cell.Grapheme), boolByte(cell.HasBackground), boolByte(cell.Palette), cell.Background.R, cell.Background.G, cell.Background.B)
		for _, color := range [3]vt.Color{style.FG, style.BG, style.UnderlineColor} {
			buf = append(buf, color.Kind, color.Index, color.RGB.R, color.RGB.G, color.RGB.B)
		}
		buf = append(buf, boolByte(style.Bold), boolByte(style.Italic), boolByte(style.Faint), boolByte(style.Blink), boolByte(style.Inverse), boolByte(style.Invisible), boolByte(style.Strikethrough), boolByte(style.Overline), byte(style.Underline))
		if cell.Grapheme {
			c.runes = rs.CellGraphemes(x, c.runes[:0])
			buf = binary.LittleEndian.AppendUint32(buf, uint32(len(c.runes)))
			for _, r := range c.runes {
				buf = binary.LittleEndian.AppendUint32(buf, uint32(r))
			}
		}
		if len(buf) > rowLayoutKeyLimit {
			c.keyBuf, c.runes = nil, nil
			return nil
		}
	}
	c.keyBuf = buf
	return buf
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func (v *view) prepareRow(line *rowCache, rs *vt.RenderState, cols int) {
	cells := rs.RowCells()
	key := v.rowLayouts.key(rs, cells, cols)
	if key != nil {
		if cached, ok := v.rowLayouts.get(key); ok {
			*line = cached
			return
		}
	}
	v.buildRowCells(line, rs, cols, cells)
	if key != nil {
		v.rowLayouts.put(key, line)
	}
}
