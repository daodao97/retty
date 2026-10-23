package terminal

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"retty/internal/terminal/internal/vt"
)

// screen is the emulator whose cells the user sees. The caller holds t.mu.
// Keyboard protocol modes and incoming ANSI always use t.term instead.
func (v *view) screen() *vt.Terminal {
	if v.reflow && v.t.presentation != nil {
		return v.t.presentation
	}
	return v.t.term
}

// presentation projects decoded primary-screen content into a local grid.
// It never replays wide ANSI directly into a narrow emulator: restore at the
// source geometry first, then reflow. Alternate buffers are copied into a
// primary presentation buffer so their text can also reflow. The application
// retains its own buffer, mouse and keyboard modes in the canonical emulator.
// Both emulator and render locks are held by paint.
func (v *view) presentation() *vt.Terminal {
	t := v.t
	old := t.presentation
	distance := 0
	expand := false
	if t.presentationHistory == 0 {
		t.presentationHistory = 512
	}
	if old != nil && !old.AtBottom() {
		bar := old.Scrollbar()
		distance = int(bar.Total - bar.Offset - bar.Len)
		if bar.Offset < bar.Len && t.presentationHistory < int(t.term.Scrollbar().Total) {
			t.presentationHistory += 512
			expand = true
		}
	}
	size := gridSize{v.cols, v.rows, v.cellW, v.cellH}
	if old != nil && t.presentationSize == size {
		// Keep selected text stable while incoming output changes the source.
		// A tap clears the selection and resumes the current screen.
		if _, selected := old.SelectionText(); selected || v.touchSelecting {
			return old
		}
	}
	if old != nil && t.presentationRevision == t.revision && t.presentationSize == size && !expand {
		return old
	}
	next, err := vt.NewTerminal(t.size.cols, t.size.rows, vt.Effects{})
	if err != nil {
		return t.term
	}
	if t.opts.ImageLimit >= 0 {
		limit := uint64(t.opts.ImageLimit)
		if limit == 0 {
			limit = 8 << 20
		}
		next.SetImageLimit(limit)
	}
	switch {
	case t.opts.Scrollback < 0:
		next.SetScrollback(0)
	case t.opts.Scrollback > 0:
		next.SetScrollback(t.opts.Scrollback)
	default:
		next.SetScrollback(10 << 20)
	}
	next.SetModeDefault(vt.ModeGraphemes, true)
	v.theme.apply(next)
	snapshot := t.term.VTGraphicsRecent(t.presentationHistory)
	// The serialized screen enters the alternate buffer before its cells.
	// Only this local copy changes buffers; raw program output never does.
	for _, mode := range []string{"1049", "1047", "47"} {
		snapshot = bytes.ReplaceAll(snapshot, []byte("\x1b[?"+mode+"h"), []byte("\x1b[?"+mode+"l"))
	}
	next.Write(snapshot)
	v.compactPrimary(next)
	next.Resize(v.cols, v.rows, v.cellW, v.cellH)
	next.ScrollToBottom()
	if t.resumeReading != nil {
		t.resumeReading.restore(next)
		t.resumeReading = nil
	} else if distance > 0 {
		next.ScrollBy(-distance)
	}
	if old != nil {
		if v.gesture != nil {
			v.gesture.Reset(old)
		}
		v.selecting, v.ticking = false, false
		old.Free()
	}
	t.presentation, t.presentationSize, t.presentationRevision = next, size, t.revision
	// A newly created emulator can reuse row identities. Rebuild every row.
	v.lines = nil
	return next
}

type presentationRow struct {
	text             []rune
	first, last      int
	art, blank       bool
	color            vt.Color
	paddedBackground bool
	background       vt.Color
}

// compactPrimary removes excess screen-height spacers and recenters Braille
// drawings before reflow. Those cells are graphics, rather than paragraphs;
// preserving their common indentation keeps the picture intact on a phone.
func (v *view) compactPrimary(next *vt.Terminal) {
	source := v.t.term
	bar := source.Scrollbar()
	atBottom := source.AtBottom()
	if !atBottom {
		source.ScrollToBottom()
		defer source.ScrollToRow(int(bar.Offset))
	}
	rs, err := vt.NewRenderState()
	if err != nil {
		return
	}
	defer rs.Free()
	rs.Update(source)
	rs.Rows()
	var rows []presentationRow
	extra := 0
	for rs.NextRow() {
		cells := rs.RowCells()
		row := presentationRow{first: len(cells), last: -1, blank: true, art: true, text: make([]rune, len(cells))}
		for x, c := range cells {
			cp := c.Codepoint
			if cp == 0 {
				cp = ' '
			}
			row.text[x] = cp
			if row.background.Kind == 0 {
				if c.HasBackground {
					if c.Palette {
						row.background = vt.Color{Kind: 1, Index: c.Background.R}
					} else {
						row.background = vt.Color{Kind: 2, RGB: c.Background}
					}
				} else if c.Style != 0 {
					row.background = rs.CellStyle(x).BG
				}
			}
			if (cp == ' ' || cp == 0x2800) && x >= v.cols && (c.HasBackground || c.Style != 0 && rs.CellStyle(x).BG.Kind != 0) {
				row.paddedBackground = true
			}
			if cp == ' ' || cp == 0x2800 {
				continue
			}
			row.blank = false
			if row.first == len(cells) {
				row.first = x
				row.color = rs.CellStyle(x).FG
			}
			row.last = x
			if cp < 0x2800 || cp > 0x28ff {
				row.art = false
			}
		}
		row.art = row.art && !row.blank
		if !row.blank && !row.art {
			extra += max((row.last+v.cols)/v.cols-1, 0)
		}
		rows = append(rows, row)
	}
	var edits strings.Builder
	edits.WriteString("\x1b[?6l\x1b[r")
	// Filled desktop panels contain colored padding out to the source's
	// right edge. Keep that padding to the local width: reflowing all of
	// it produces extra blank rows and a jagged input panel.
	for y, row := range rows {
		if row.paddedBackground && !row.art {
			end := max(v.cols, row.last+1)
			fmt.Fprintf(&edits, "\x1b[0m\x1b[%d;%dH\x1b[K", y+1, end+1)
			if row.blank && row.background.Kind != 0 {
				// Printable spaces keep an otherwise empty colored row in
				// Ghostty's primary-screen reflow.
				fmt.Fprintf(&edits, "\x1b[%d;1H", y+1)
				switch row.background.Kind {
				case 1:
					fmt.Fprintf(&edits, "\x1b[48;5;%dm", row.background.Index)
				case 2:
					fmt.Fprintf(&edits, "\x1b[48;2;%d;%d;%dm", row.background.RGB.R, row.background.RGB.G, row.background.RGB.B)
				}
				edits.WriteString(strings.Repeat(" ", v.cols))
			}
		}
	}
	for start := 0; start < len(rows); {
		if !rows[start].art {
			start++
			continue
		}
		end, left, right := start, rows[start].first, rows[start].last
		for end < len(rows) && rows[end].art {
			left = min(left, rows[end].first)
			right = max(right, rows[end].last)
			end++
		}
		width := right - left + 1
		if width <= v.cols {
			margin := (v.cols - width) / 2
			for y := start; y < end; y++ {
				row := rows[y]
				fmt.Fprintf(&edits, "\x1b[0m\x1b[%d;1H\x1b[2K", y+1)
				switch row.color.Kind {
				case 1:
					fmt.Fprintf(&edits, "\x1b[38;5;%dm", row.color.Index)
				case 2:
					fmt.Fprintf(&edits, "\x1b[38;2;%d;%d;%dm", row.color.RGB.R, row.color.RGB.G, row.color.RGB.B)
				}
				edits.WriteString(strings.Repeat(" ", margin))
				edits.WriteString(string(row.text[left : right+1]))
			}
		}
		start = end
	}
	x, y := source.Cursor()
	type gap struct{ start, end int }
	var gaps []gap
	for i := 0; i < min(y, len(rows)); {
		if !rows[i].blank {
			i++
			continue
		}
		end := i + 1
		for end < min(y, len(rows)) && rows[end].blank {
			end++
		}
		if end-i >= 3 {
			gaps = append(gaps, gap{i, end})
		}
		i = end
	}
	// Long desktop spacers give space back to the phone before any text is
	// pushed into scrollback. Keep one separating blank row in every gap.
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].end-gaps[i].start > gaps[j].end-gaps[j].start })
	remove := max(len(rows)+extra-v.rows, 0)
	var deletions []gap
	for _, g := range gaps {
		n := min(remove, g.end-g.start-1)
		if n > 0 {
			deletions = append(deletions, gap{g.start, g.start + n})
			remove -= n
		}
	}
	sort.Slice(deletions, func(i, j int) bool { return deletions[i].start > deletions[j].start })
	// Erased cells inherit the current background. Clear panel colors before
	// moving rows so they cannot paint unrelated artwork or spacer rows.
	edits.WriteString("\x1b[0m")
	for _, g := range deletions {
		fmt.Fprintf(&edits, "\x1b[%d;1H\x1b[%dM", g.start+1, g.end-g.start)
		y -= g.end - g.start
	}
	fmt.Fprintf(&edits, "\x1b[0m\x1b[%d;%dH", max(y, 0)+1, x+1)
	next.Write([]byte(edits.String()))
}
