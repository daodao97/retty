package terminal

import (
	"math"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/terminal/internal/vt"
)

// Padding around the grid, in DIPs.
const padX, padY = 0, 4

// rowCache is what a row of the screen draws, made when it changes:
// backgrounds, runs of glyphs, lines and drawn characters, in cells.
type rowCache struct {
	ok    bool
	bgs   []bgSpan
	runs  []glyphRun
	decos []deco
	boxes []boxCell
	selA  int
	selB  int
	sel   bool
	wide  []int16 // the columns of wide characters
}

type bgSpan struct {
	x0, x1 int
	c      ui.Color
}

// glyphRun is the glyphs of a run of cells of one style, placed in their
// cells, with X from the left of the grid in DIPs.
type glyphRun struct {
	glyphs []ui.Glyph
	cols   []int16 // the column of each glyph
	c      ui.Color
}

type deco struct {
	x0, x1 int
	kind   int // an underline style, decoStrike or decoOverline
	c      ui.Color
}

const (
	decoStrike   = 10
	decoOverline = 11
)

// boxCell is a character drawn rather than taken from the font: box
// drawing, blocks and Powerline's separators.
type boxCell struct {
	x int
	r rune
	c ui.Color
}

// layout sizes the grid for a box of the element at a scale.
func (v *view) layout(r ui.Rect, scale float32) {
	t := v.t
	t.mu.Lock()
	fk := t.opts.Font.key(scale)
	cols, rows, fit := t.opts.FixedCols, t.opts.FixedRows, t.opts.FitToView
	reflow := t.opts.ReflowView && !fit && t.term != nil
	v.reflow = reflow
	t.mu.Unlock()
	v.ox, v.oy = int(math.Round(padX*float64(scale))), int(math.Round(padY*float64(scale)))
	w, h := max(int(math.Round(float64(r.W*scale)))-2*v.ox, 1), max(int(math.Round(float64(r.H*scale)))-2*v.oy, 1)
	if cols > 0 && rows > 0 && fit {
		// Whole-pixel cells must fit too; rounding each cell after scaling
		// the font can otherwise clip the final columns on a Retina screen.
		cw, ch := max(w/cols, 1), max(h/rows, 1)
		key := fitFontKey{font: fk, cellW: cw, cellH: ch}
		if key != v.fitKey {
			v.fitKey, v.fittedFont = key, fitFont(fk, scale, cw, ch)
		}
		fk = v.fittedFont
	}
	if fk != v.font {
		v.followCursor = true
		v.font, v.scale = fk, scale
		for i := range v.fonts {
			v.fonts[i] = fk.variant(i)
		}
		v.cellW, v.cellH, v.baseline = fontGeometry(fk, scale)
		v.shaped.clear()
		v.lines = v.lines[:0]
	}
	v.cols, v.rows = max(w/v.cellW, 1), max(h/v.cellH, 1)
	if v.viewportW != w || v.viewportH != h {
		v.followCursor = true
	}
	v.viewportW, v.viewportH = w, h
	v.fixedGrid = cols > 0 && rows > 0 && !reflow
	if v.fixedGrid {
		v.cols, v.rows = cols, rows
		if fit {
			v.panX, v.panY = 0, 0
		}
		v.panX = min(max(v.panX, 0), max(cols*v.cellW-w, 0))
		v.panY = min(max(v.panY, 0), max(rows*v.cellH-h, 0))
		v.ox -= v.panX
		v.oy -= v.panY
	}
}

type fitFontKey struct {
	font         fontKey
	cellW, cellH int
}

// fitFont finds the largest uniform font that fits the whole-pixel cell
// budget. It enlarges small source screens as well as shrinking large ones.
func fitFont(fk fontKey, scale float32, cw, ch int) fontKey {
	mw, mh, _ := fontGeometry(fk, scale)
	factor := min(float32(cw)/float32(mw), float32(ch)/float32(mh))
	low, high := float32(0), max(fk.size, fk.size*factor)*2
	fits := func(size float32) bool {
		candidate := fk
		candidate.size = size
		w, h, _ := fontGeometry(candidate, scale)
		return w <= cw && h <= ch
	}
	for fits(high) {
		low, high = high, high*2
	}
	for range 12 {
		middle := (low + high) / 2
		if fits(middle) {
			low = middle
		} else {
			high = middle
		}
	}
	fk.size = low
	return fk
}

func fontGeometry(fk fontKey, scale float32) (cw, ch, baseline int) {
	font := fk.variant(0)
	m := font.Metrics()
	adv := float32(0)
	for _, g := range ui.Shape(asciiPrintable, font) {
		adv = max(adv, g.Advance)
	}
	cw = max(int(math.Ceil(float64(adv*scale-0.05))), 1)
	ch = max(int(math.Ceil(float64((m.Ascent+m.Descent+m.LineGap)*fk.lineHeight*scale-0.05))), 1)
	baseline = int(math.Round(float64((float32(ch)-(m.Ascent+m.Descent)*scale)/2 + m.Ascent*scale)))
	return
}

const asciiPrintable = " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"

// paint updates the rows that changed and draws the terminal.
func (v *view) paint(p *ui.Painter, r ui.Rect) {
	t := v.t
	var event *RenderEvent
	defer func() {
		if event != nil && t.opts.OnRenderEvent != nil {
			t.opts.OnRenderEvent(*event)
		}
	}()
	v.painted = r
	v.layout(r, p.Scale())
	bg := v.theme.Background
	t.mu.Lock()
	if t.term == nil {
		t.mu.Unlock()
		if !t.opts.Transparent {
			p.Fill(r, bg, 0)
		}
		return
	}
	if t.opts.FixedCols > 0 && t.opts.FixedRows > 0 {
		t.resize(gridSize{t.opts.FixedCols, t.opts.FixedRows, v.cellW, v.cellH})
	} else {
		t.resize(gridSize{v.cols, v.rows, v.cellW, v.cellH})
	}
	t.advanceSearch()
	v.searchRects = v.searchRects[:0]
	if t.search != nil {
		v.searchMatches, v.searchRects = t.search.Highlights(t.term, v.searchMatches, v.searchRects)
	}
	if v.ticking && v.gesture != nil {
		v.autoscroll()
	}
	t.rmu.Lock()
	defer t.rmu.Unlock()
	// Keep active updates hidden even if they take longer than a second.
	// Recover only when the program stops sending data without committing.
	if t.held && time.Since(t.heldSince) >= syncOutputTimeout {
		event = &RenderEvent{Kind: "sync_timeout", Duration: time.Since(t.holdStarted)}
		t.term.SetMode(vt.ModeSyncOutput, false)
		t.held = false
	}
	held := t.held
	screen := t.term
	if !held && v.reflow {
		screen = v.presentation()
	}
	if !held {
		t.rs.BeginUpdate(screen)
	}
	v.scrollbar = screen.Scrollbar()
	t.mu.Unlock()
	rs := t.rs
	if !held {
		rs.EndUpdate()
	}
	colors := rs.Colors()
	full := rs.Dirty() == vt.Full || colors != v.colors
	oldCursor := v.cursor
	v.colors, v.cursor = colors, rs.Cursor()
	v.caretCell = [2]int{v.cursor.X, v.cursor.Y}
	if v.cursor.WideTail && v.caretCell[0] > 0 {
		v.caretCell[0]--
	}
	if v.fixedGrid && (v.followCursor || oldCursor.X != v.cursor.X || oldCursor.Y != v.cursor.Y) {
		oldX, oldY := v.panX, v.panY
		v.panX = min(max(v.panX, (int(v.cursor.X)+1)*v.cellW-v.viewportW), int(v.cursor.X)*v.cellW)
		v.panY = min(max(v.panY, (int(v.cursor.Y)+1)*v.cellH-v.viewportH), int(v.cursor.Y)*v.cellH)
		v.panX = min(max(v.panX, 0), max(v.cols*v.cellW-v.viewportW, 0))
		v.panY = min(max(v.panY, 0), max(v.rows*v.cellH-v.viewportH, 0))
		v.ox += oldX - v.panX
		v.oy += oldY - v.panY
	}
	v.followCursor = false
	cols, rows := rs.Size()
	if len(v.lines) != rows {
		v.lines = append(v.lines[:0], make([]rowCache, rows)...)
		full = true
	}
	rs.Rows()
	for y := 0; y < rows && rs.NextRow(); y++ {
		line := &v.lines[y]
		if full || !line.ok || rs.RowDirty() {
			v.buildRow(line, rs, cols)
		}
	}
	rs.Clean()
	v.draw(p, r)
}

// resolved is a cell's style with its colors resolved.
type resolved struct {
	fg, bg, ul       ui.Color
	hasBg, hasUl     bool
	adaptiveBg       bool
	variant          int // 1 bold, 2 italic
	invisible        bool
	underline        int
	strike, overline bool
}

func (v *view) resolve(s vt.Style) resolved {
	pal := &v.colors.Palette
	col := func(c vt.Color, def vt.RGB) (ui.Color, bool) {
		switch c.Kind {
		case 1:
			return color(pal[c.Index]), true
		case 2:
			return color(c.RGB), true
		}
		return color(def), false
	}
	var r resolved
	r.fg, _ = col(s.FG, v.colors.Foreground)
	r.bg, r.hasBg = col(s.BG, v.colors.Background)
	r.adaptiveBg = s.BG.Kind == 2 || s.BG.Kind == 1 && s.BG.Index >= 16
	r.ul, r.hasUl = col(s.UnderlineColor, v.colors.Foreground)
	if s.Inverse {
		r.fg, r.bg, r.hasBg = r.bg, r.fg, true
		r.adaptiveBg = s.FG.Kind == 2 || s.FG.Kind == 1 && s.FG.Index >= 16
	}
	if s.Faint {
		r.fg = r.fg.Alpha(0.5)
	}
	if s.Bold {
		r.variant |= 1
	}
	if s.Italic {
		r.variant |= 2
	}
	r.invisible, r.underline, r.strike, r.overline = s.Invisible, s.Underline, s.Strikethrough, s.Overline
	return r
}

// buildRow makes the cache of the render state's current row.
func (v *view) buildRow(line *rowCache, rs *vt.RenderState, cols int) {
	*line = rowCache{ok: true, bgs: line.bgs[:0], runs: line.runs[:0], decos: line.decos[:0], boxes: line.boxes[:0], wide: line.wide[:0]}
	cells := rs.RowCells()
	line.selA, line.selB, line.sel = rs.RowSelection()
	def := resolved{fg: color(v.colors.Foreground), bg: color(v.colors.Background)}
	var styles []struct {
		id uint16
		r  resolved
	}
	styleOf := func(c vt.Cell, x int) resolved {
		if c.Style == 0 {
			return def
		}
		for _, s := range styles {
			if s.id == c.Style {
				return s.r
			}
		}
		r := v.resolve(rs.CellStyle(x))
		styles = append(styles, struct {
			id uint16
			r  resolved
		}{c.Style, r})
		return r
	}
	var run struct {
		text    []rune
		cols    []int16 // the column of each rune
		widths  []int8
		fg      ui.Color
		variant int
	}
	flush := func() {
		v.shapeRun(line, run.text, run.cols, run.widths, run.fg, run.variant)
		run.text, run.cols, run.widths = run.text[:0], run.cols[:0], run.widths[:0]
	}
	var buf []rune
	for x := 0; x < len(cells) && x < cols; x++ {
		c := cells[x]
		if c.Wide == vt.SpacerTail || c.Wide == vt.SpacerHead {
			continue
		}
		w := 1
		if c.Wide == vt.WideChar {
			w = 2
			line.wide = append(line.wide, int16(x))
		}
		st := styleOf(c, x)
		if c.HasBackground {
			bg := c.Background
			if c.Palette {
				bg = v.colors.Palette[c.Background.R]
			}
			st.bg, st.hasBg = color(bg), true
			st.adaptiveBg = !c.Palette || c.Background.R >= 16
		}
		if st.adaptiveBg && !noMinContrast(c.Codepoint) {
			st.bg = v.adaptiveBackground(st.bg)
		}
		// Cell backgrounds can be independent of the style, so adjust only
		// after resolving both. Preserve the colors of block/line artwork.
		if !st.invisible && c.Codepoint != 0 && !noMinContrast(c.Codepoint) {
			st.fg = v.readableForeground(st.fg, st.bg)
			if st.hasUl {
				st.ul = v.readableForeground(st.ul, st.bg)
			}
		}
		if st.hasBg && st.bg != def.bg {
			if n := len(line.bgs); n > 0 && line.bgs[n-1].x1 == x && line.bgs[n-1].c == st.bg {
				line.bgs[n-1].x1 = x + w
			} else {
				line.bgs = append(line.bgs, bgSpan{x, x + w, st.bg})
			}
		}
		if st.underline != 0 {
			v.addDeco(line, x, x+w, st.underline, st.ulColor())
		}
		if st.strike {
			v.addDeco(line, x, x+w, decoStrike, st.fg)
		}
		if st.overline {
			v.addDeco(line, x, x+w, decoOverline, st.fg)
		}
		cp := c.Codepoint
		if cp == 0 || st.invisible {
			cp = ' '
		}
		if drawn(cp) {
			line.boxes = append(line.boxes, boxCell{x, cp, st.fg})
			cp = ' '
		}
		if len(run.text) > 0 && (st.variant != run.variant || st.fg != run.fg) {
			if cp == ' ' {
				// A space draws nothing: it can join any run.
				st.variant, st.fg = run.variant, run.fg
			} else {
				flush()
			}
		}
		if len(run.text) == 0 {
			run.fg, run.variant = st.fg, st.variant
		}
		buf = buf[:0]
		if c.Grapheme && cp != ' ' {
			buf = rs.CellGraphemes(x, buf)
		}
		if len(buf) == 0 {
			buf = append(buf, cp)
		}
		for i, r := range buf {
			run.text = append(run.text, r)
			run.cols = append(run.cols, int16(x))
			ww := int8(0) // the runes after the first share its cell
			if i == 0 {
				ww = int8(w)
			}
			run.widths = append(run.widths, ww)
		}
	}
	flush()
}

func (r resolved) ulColor() ui.Color {
	if r.hasUl {
		return r.ul
	}
	return r.fg
}

func (v *view) addDeco(line *rowCache, x0, x1, kind int, c ui.Color) {
	if n := len(line.decos); n > 0 {
		d := &line.decos[n-1]
		if d.x1 == x0 && d.kind == kind && d.c == c {
			d.x1 = x1
			return
		}
	}
	line.decos = append(line.decos, deco{x0, x1, kind, c})
}

// shapeRun shapes a run of cells and places its glyphs in their cells.
func (v *view) shapeRun(line *rowCache, text []rune, cols []int16, widths []int8, fg ui.Color, variant int) {
	// Trailing spaces draw nothing.
	for len(text) > 0 && text[len(text)-1] == ' ' {
		text, cols, widths = text[:len(text)-1], cols[:len(cols)-1], widths[:len(widths)-1]
	}
	if len(text) == 0 {
		return
	}
	shaped := v.shape(string(text), variant, 1)
	cw := float32(v.cellW) / v.scale
	out := glyphRun{glyphs: make([]ui.Glyph, 0, len(shaped)), cols: make([]int16, 0, len(shaped)), c: fg}
	// The glyphs of a cell, or of the cells a ligature spans, keep their
	// places relative to each other and are centered in the cells, as the
	// glyphs of fallback fonts, wider or narrower than a cell, are. Those
	// too wide, as emoji are, shrink to fit, as in Ghostty.
	for i := 0; i < len(shaped); {
		first := max(0, min(shaped[i].Cluster, len(cols)-1))
		col := cols[first]
		last := first // the last rune of the group
		x0, x1 := shaped[i].X, shaped[i].X+shaped[i].Advance
		j := i
		for {
			g := shaped[j]
			last = max(last, min(g.Cluster+max(g.Runes, 1), len(cols))-1)
			x0, x1 = min(x0, g.X), max(x1, g.X+g.Advance)
			if j+1 >= len(shaped) || cols[max(0, min(shaped[j+1].Cluster, len(cols)-1))] != col {
				break
			}
			j++
		}
		span := 0
		for k := first; k <= last; k++ {
			span += int(widths[k])
		}
		room := float32(max(span, 1)) * cw
		group := shaped[i : j+1]
		if f := room / (x1 - x0); f < 0.95 && x1 > x0 {
			// Too wide: the same text, smaller.
			small := v.shape(string(text[first:last+1]), variant, max(f, 0.5))
			if len(small) > 0 {
				group = small
				x0, x1 = small[0].X, small[0].X+small[0].Advance
				for _, g := range small {
					x0, x1 = min(x0, g.X), max(x1, g.X+g.Advance)
				}
			}
		}
		off := (room - (x1 - x0)) / 2
		if math.Abs(float64(off)) < 0.5/float64(v.scale) {
			off = 0
		}
		cellX := float32(col) * cw
		for _, gl := range group {
			gl.X = cellX + off + (gl.X - x0)
			out.glyphs = append(out.glyphs, gl)
			out.cols = append(out.cols, col)
		}
		i = j + 1
	}
	line.runs = append(line.runs, out)
}

// shape shapes text in a variant of the font, at a fraction of its size,
// from the cache.
func (v *view) shape(text string, variant int, scale float32) []ui.Glyph {
	k := shapeKey{text, variant, scale}
	if g, ok := v.shaped.get(k); ok {
		return g
	}
	f := v.fonts[variant&3]
	f.Size *= scale
	g := ui.Shape(text, f)
	v.shaped.put(k, g)
	return g
}

// draw draws the rows and the cursor.
func (v *view) draw(p *ui.Painter, r ui.Rect) {
	s := v.scale
	cw, ch := float32(v.cellW)/s, float32(v.cellH)/s
	// The grid starts at a whole pixel of the display.
	ox := (float32(math.Round(float64(r.X*s))) + float32(v.ox)) / s
	oy := (float32(math.Round(float64(r.Y*s))) + float32(v.oy)) / s
	base := float32(v.baseline) / s
	if !v.t.opts.Transparent {
		p.Fill(r, color(v.colors.Background), 0)
	}
	selection := v.theme.Selection
	if selection.A == 0 {
		selection = color(v.colors.Foreground).Alpha(0.3)
	}
	for y := range v.lines {
		line := &v.lines[y]
		ry := oy + float32(y)*ch
		for _, b := range line.bgs {
			x, end := ox+float32(b.x0)*cw, ox+float32(b.x1)*cw
			if b.x1 == v.cols {
				// Columns are whole cells; carry the final cell's background
				// across the fractional column left at the viewport edge.
				end = max(end, r.X+r.W)
			}
			p.Fill(ui.Rect{X: x, Y: ry, W: end - x, H: ch}, b.c, 0)
		}
		if line.sel {
			p.Fill(ui.Rect{X: ox + float32(line.selA)*cw, Y: ry, W: float32(line.selB-line.selA+1) * cw, H: ch}, selection, 0)
		}
	}
	if v.hover.valid() && v.linkMods&ui.Cmd != 0 {
		if a, b := v.hover.cellA, v.hover.cellB; a < b {
			for row := a / v.cols; row <= (b-1)/v.cols; row++ {
				left, right := max(a, row*v.cols)-row*v.cols, min(b, (row+1)*v.cols)-row*v.cols
				p.Fill(ui.Rect{X: ox + float32(left)*cw, Y: oy + float32(v.hover.row+row+1)*ch - 2/s, W: float32(right-left) * cw, H: 1 / s}, v.theme.Foreground, 0)
			}
		}
	}
	cur, curColor, curText, block := v.cursorRect(ox, oy, cw, ch)
	for _, match := range v.searchRects {
		rect := ui.Rect{X: ox + float32(match.Start)*cw, Y: oy + float32(match.Row)*ch, W: float32(match.End-match.Start) * cw, H: ch}
		fill := ui.RGBA(255, 191, 50, 0.25)
		if match.Selected {
			fill = ui.RGBA(255, 159, 30, 0.42)
		}
		p.Fill(rect, fill, 1/s)
		if match.Selected {
			p.Stroke(rect, ui.RGB(236, 153, 30), 1/s, 1/s)
		}
	}
	if block {
		p.Fill(cur, curColor, 0)
	}
	for y := range v.lines {
		line := &v.lines[y]
		ry := oy + float32(y)*ch
		text := func(over ui.Color) { v.drawText(p, line, ox, ry, cw, ch, base, over) }
		if !line.sel || v.theme.SelectionText.A == 0 {
			text(ui.Color{})
			continue
		}
		// The selected text takes the selection's color in its cells,
		// as far as its glyphs reach.
		a, b := ox+float32(line.selA)*cw, ox+float32(line.selB+1)*cw
		p.Clip(ui.Rect{X: r.X, Y: r.Y, W: a - r.X, H: r.H}, 0, func() { text(ui.Color{}) })
		p.Clip(ui.Rect{X: a, Y: r.Y, W: b - a, H: r.H}, 0, func() { text(v.theme.SelectionText) })
		p.Clip(ui.Rect{X: b, Y: r.Y, W: r.X + r.W - b, H: r.H}, 0, func() { text(ui.Color{}) })
	}
	if cur.W > 0 && !block {
		if v.focused || v.cursor.Style != vt.CursorBlock {
			p.Fill(cur, curColor, 0)
		} else {
			p.Stroke(cur, curColor, 0, 1/s)
		}
	}
	if block && v.cursor.Y < len(v.lines) {
		// The text under a block cursor, in the cursor's text color.
		line := &v.lines[v.cursor.Y]
		ry := oy + float32(v.cursor.Y)*ch
		p.Clip(cur, 0, func() {
			for _, run := range line.runs {
				for i, col := range run.cols {
					if int(col) == v.cursor.X {
						p.Glyphs(run.glyphs[i:i+1], ox, ry+base, curText)
					}
				}
			}
			for _, b := range line.boxes {
				if b.x == v.cursor.X {
					drawBox(p, b.r, ui.Rect{X: ox + float32(b.x)*cw, Y: ry, W: cw, H: ch}, curText)
				}
			}
		})
	}
	// The composition is drawn whenever there is one. It used to also require
	// v.focused, which build() sets from the element's focus — and an input
	// method's SetMarkedText runs off the frame, so on the first composition
	// after a tap v.focused was still false and the candidate bar showed while
	// the terminal showed nothing at all. Having a preedit is the condition
	// that matters: only a focused element is given one, so the second test
	// only ever hid text the user was typing.
	if v.preedit != "" {
		v.drawPreedit(p, ox, oy, cw, ch, base)
	}
	v.drawScrollbar(p, r)
}

// drawText draws the glyphs, drawn characters and lines of a row, in their
// colors or, when over is not zero, in over.
func (v *view) drawText(p *ui.Painter, line *rowCache, ox, ry, cw, ch, base float32, over ui.Color) {
	pick := func(c ui.Color) ui.Color {
		if over.A > 0 {
			return over
		}
		return c
	}
	for _, run := range line.runs {
		p.Glyphs(run.glyphs, ox, ry+base, pick(run.c))
	}
	for _, b := range line.boxes {
		drawBox(p, b.r, ui.Rect{X: ox + float32(b.x)*cw, Y: ry, W: cw, H: ch}, pick(b.c))
	}
	for _, d := range line.decos {
		d.c = pick(d.c)
		v.drawDeco(p, d, ox, ry, cw, ch, base)
	}
}

// cursorRect returns the cursor's rectangle and colors, and whether it is
// a filled block, whose text shows in the cursor's text color. A cursor
// that does not show has an empty rectangle.
func (v *view) cursorRect(ox, oy, cw, ch float32) (r ui.Rect, c, text ui.Color, block bool) {
	cu := v.cursor
	if !cu.InView || !cu.Visible || v.preedit != "" {
		return
	}
	active := v.focused || v.t.opts.ActiveCursor
	if active && cu.Blinking && time.Since(v.blinkStart)%(2*blinkPeriod) >= blinkPeriod {
		return
	}
	c = color(v.colors.Foreground)
	if v.colors.HasCursor {
		c = color(v.colors.Cursor)
	} else if v.theme.Cursor.A > 0 {
		c = v.theme.Cursor
	}
	text = color(v.colors.Background)
	if v.theme.CursorText.A > 0 {
		text = v.theme.CursorText
	}
	x := cu.X
	if cu.WideTail && x > 0 {
		x--
	}
	w := cw
	if v.wideAt(cu.Y, x) {
		w *= 2
	}
	r = ui.Rect{X: ox + float32(x)*cw, Y: oy + float32(cu.Y)*ch, W: w, H: ch}
	px := 1 / v.scale
	style := cu.Style
	if !active && style == vt.CursorBlock {
		style = vt.CursorBlockHollow
	}
	switch style {
	case vt.CursorBar:
		r.W = max(float32(math.Round(float64(1.5*v.scale))), 1) * px
	case vt.CursorUnderline:
		h := max(float32(math.Round(float64(1.5*v.scale))), 1) * px
		r.Y, r.H = r.Y+r.H-h, h
	case vt.CursorBlockHollow:
		return r, c, text, false // stroked
	default:
		block = true
	}
	return r, c, text, block
}

// wideAt reports whether the cell at column x of row y holds a wide
// character.
func (v *view) wideAt(y, x int) bool {
	return y < len(v.lines) && slices.Contains(v.lines[y].wide, int16(x))
}

// drawDeco draws an underline, a strikethrough or an overline.
func (v *view) drawDeco(p *ui.Painter, d deco, ox, ry, cw, ch, base float32) {
	px := 1 / v.scale
	thick := max(float32(math.Round(float64(v.font.size*v.scale/14))), 1) * px
	x0, x1 := ox+float32(d.x0)*cw, ox+float32(d.x1)*cw
	ul := ry + base + max(float32(math.Round(float64(v.font.size*v.scale/10))), 1)*px
	switch d.kind {
	case decoStrike:
		p.Fill(ui.Rect{X: x0, Y: ry + base - v.font.size*0.3, W: x1 - x0, H: thick}, d.c, 0)
	case decoOverline:
		p.Fill(ui.Rect{X: x0, Y: ry, W: x1 - x0, H: thick}, d.c, 0)
	case vt.UnderlineDouble:
		p.Fill(ui.Rect{X: x0, Y: ul - thick, W: x1 - x0, H: thick}, d.c, 0)
		p.Fill(ui.Rect{X: x0, Y: ul + thick, W: x1 - x0, H: thick}, d.c, 0)
	case vt.UnderlineCurly:
		var path ui.Path
		amp := 1.5 * thick
		period := cw
		n := int((x1 - x0) / period * 8)
		path.MoveTo(x0, ul)
		for i := 1; i <= n; i++ {
			x := x0 + (x1-x0)*float32(i)/float32(n)
			path.LineTo(x, ul+amp*float32(math.Sin(float64((x-x0)/period*2*math.Pi))))
		}
		p.StrokePath(&path, thick, d.c)
	case vt.UnderlineDotted, vt.UnderlineDashed:
		step, dash := 2*thick, thick
		if d.kind == vt.UnderlineDashed {
			step, dash = cw/2, cw/4
		}
		for x := x0; x < x1; x += step {
			p.Fill(ui.Rect{X: x, Y: ul, W: min(dash, x1-x), H: thick}, d.c, 0)
		}
	default:
		p.Fill(ui.Rect{X: x0, Y: ul, W: x1 - x0, H: thick}, d.c, 0)
	}
}

// drawPreedit draws an input method's composition over the cursor.
func (v *view) drawPreedit(p *ui.Painter, ox, oy, cw, ch, base float32) {
	g := ui.Shape(v.preedit, v.fonts[0])
	w := float32(0)
	for _, gl := range g {
		w = max(w, gl.X+gl.Advance)
	}
	x, y := ox+float32(v.cursor.X)*cw, oy+float32(v.cursor.Y)*ch
	p.Fill(ui.Rect{X: x, Y: y, W: w, H: ch}, color(v.colors.Background), 0)
	fg := color(v.colors.Foreground)
	p.Glyphs(g, x, y+base, fg)
	p.Fill(ui.Rect{X: x, Y: y + ch - 1/v.scale, W: w, H: 1 / v.scale}, fg, 0)
}

// drawScrollbar shows where the viewport is while it shows the scrollback.
func (v *view) drawScrollbar(p *ui.Painter, r ui.Rect) {
	sb := v.scrollbar
	if sb.Total <= sb.Len || sb.Offset+sb.Len >= sb.Total {
		return
	}
	h := max(r.H*float32(sb.Len)/float32(sb.Total), 24)
	y := r.Y + (r.H-h)*float32(sb.Offset)/float32(sb.Total-sb.Len)
	c := color(v.colors.Foreground).Alpha(0.35)
	p.Fill(ui.Rect{X: r.X + r.W - 7, Y: y + 2, W: 4, H: h - 4}, c, 2)
}

// lru caches shaped text.
type lru struct {
	cap  int
	m    map[shapeKey]*lruEntry
	head lruEntry // a ring of the entries, most recent first
}

type shapeKey struct {
	text    string
	variant int
	scale   float32
}

type lruEntry struct {
	k          shapeKey
	g          []ui.Glyph
	prev, next *lruEntry
}

func (c *lru) init(n int) {
	c.cap = n
	c.clear()
}

func (c *lru) clear() {
	c.m = map[shapeKey]*lruEntry{}
	c.head.prev, c.head.next = &c.head, &c.head
}

func (c *lru) get(k shapeKey) ([]ui.Glyph, bool) {
	e := c.m[k]
	if e == nil {
		return nil, false
	}
	c.unlink(e)
	c.front(e)
	return e.g, true
}

func (c *lru) put(k shapeKey, g []ui.Glyph) {
	if len(c.m) >= c.cap {
		last := c.head.prev
		c.unlink(last)
		delete(c.m, last.k)
	}
	e := &lruEntry{k: k, g: g}
	c.m[k] = e
	c.front(e)
}

func (c *lru) unlink(e *lruEntry) { e.prev.next, e.next.prev = e.next, e.prev }

func (c *lru) front(e *lruEntry) {
	e.prev, e.next = &c.head, c.head.next
	c.head.next.prev = e
	c.head.next = e
}
