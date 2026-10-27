package terminal

import (
	"bytes"
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func motionSnapshot(t *testing.T, offset, color int) []byte {
	t.Helper()
	loadLib(t)
	source, err := New(Options{Conn: newPipe(), FixedCols: 48, FixedRows: 33, NoBlink: true})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.Feed([]byte("\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?25l\x1b[Hfixed header\x1b[2;1Hfixed status"))
	for y := 2; y < 30; y++ {
		source.Feed([]byte(fmt.Sprintf("\x1b[%d;1H\x1b[%dm%05d 中文 e\u0301 👩‍💻 output\x1b[0m", y+1, color, offset+y)))
	}
	source.Feed([]byte("\x1b[32;1H\x1b[48;2;40;40;40m> fixed input area\x1b[K\x1b[0m\x1b[33;1Hfixed footer"))
	return source.Snapshot()
}

func motionPhone(t *testing.T, smooth bool) (*Terminal, *ui.Tester) {
	t.Helper()
	phone, err := New(Options{Conn: newPipe(), FixedCols: 48, FixedRows: 33, Font: Font{Size: 13, LineHeight: 1.2}, NoBlink: true, SmoothScroll: smooth})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { phone.Close() })
	tt := ui.NewTester(func(c *ui.Context) { View(c, phone).Fill() }, 393, 699)
	tt.SetScale(3)
	return phone, tt
}

func pixels(img *image.RGBA, top, bottom int) []byte {
	var p []byte
	for y := max(top, img.Rect.Min.Y); y < min(bottom, img.Rect.Max.Y); y++ {
		off := img.PixOffset(img.Rect.Min.X, y)
		p = append(p, img.Pix[off:off+img.Rect.Dx()*4]...)
	}
	return p
}

func TestScrollMotionKeepsFixedRegionsAndFinishesQuietly(t *testing.T) {
	a, b := motionSnapshot(t, 0, 36), motionSnapshot(t, 1, 36)
	phone, tt := motionPhone(t, true)
	phone.restoreSnapshot(a, 48, 33)
	tt.Frame()
	before := append([]byte(nil), tt.Image().Pix...)
	save(t, tt, "scroll-before")
	phone.v.motion.input(20, time.Now())
	phone.restoreSnapshot(b, 48, 33)
	tt.Frame()
	m := &phone.v.motion
	if m.delta != 1 || m.top != 2 || m.bottom != 30 {
		t.Fatalf("body transition = [%d,%d), delta=%d", m.top, m.bottom, m.delta)
	}
	if !bytes.Equal(before, tt.Image().Pix) {
		t.Fatal("transition did not start at the previous screen")
	}
	top, bottom := phone.v.oy+2*phone.v.cellH, phone.v.oy+30*phone.v.cellH
	first := &image.RGBA{Pix: before, Stride: tt.Image().Stride, Rect: tt.Image().Rect}
	header, footer := pixels(first, 0, top), pixels(first, bottom, first.Rect.Max.Y)
	tt.Advance(16 * time.Millisecond)
	middle := append([]byte(nil), tt.Image().Pix...)
	save(t, tt, "scroll-middle")
	if !bytes.Equal(header, pixels(tt.Image(), 0, top)) || !bytes.Equal(footer, pixels(tt.Image(), bottom, tt.Image().Rect.Max.Y)) {
		t.Fatal("transition moved a fixed header or input/footer")
	}
	if bytes.Equal(before, middle) {
		t.Fatal("no intermediate pixel position was drawn")
	}
	tt.Advance(16 * time.Millisecond)
	save(t, tt, "scroll-after")
	if phone.v.motion.delta != 0 || len(phone.v.motion.from) != 0 {
		t.Fatal("completed transition retained its previous frame")
	}
	plain, reference := motionPhone(t, false)
	plain.restoreSnapshot(b, 48, 33)
	reference.Frame()
	if !bytes.Equal(tt.Image().Pix, reference.Image().Pix) {
		t.Fatal("finished transition differs from authoritative rendering")
	}
	if bytes.Equal(middle, tt.Image().Pix) {
		t.Fatal("intermediate position was already the final screen")
	}
	tt.Advance(time.Second)
	if tt.FramePending() {
		t.Fatal("transition left an idle frame loop running")
	}
}

func TestScrollMotionNewFrameDoesNotQueueAnotherTransition(t *testing.T) {
	phone, tt := motionPhone(t, true)
	phone.restoreSnapshot(motionSnapshot(t, 0, 36), 48, 33)
	tt.Frame()
	phone.v.motion.input(20, time.Now())
	phone.restoreSnapshot(motionSnapshot(t, 1, 36), 48, 33)
	tt.Frame()
	tt.Advance(8 * time.Millisecond)
	frame := motionSnapshot(t, 2, 36)
	phone.restoreSnapshot(frame, 48, 33)
	tt.Frame()
	plain, reference := motionPhone(t, false)
	plain.restoreSnapshot(frame, 48, 33)
	reference.Frame()
	if phone.v.motion.delta != 0 || !bytes.Equal(tt.Image().Pix, reference.Image().Pix) {
		t.Fatal("a newer authoritative frame waited behind an old transition")
	}
}

func TestRowLayoutCacheIsBoundedAndKeepsRecentEntries(t *testing.T) {
	var cache rowLayoutCache
	first := rowCache{ok: true, runs: []glyphRun{{glyphs: make([]ui.Glyph, 64)}}}
	cache.put([]byte("first"), &first)
	for i := range 500 {
		cache.get([]byte("first"))
		row := rowCache{ok: true, runs: []glyphRun{{glyphs: make([]ui.Glyph, 64)}}}
		cache.put([]byte(fmt.Sprint(i)), &row)
		if len(cache.entries) > 128 || cache.bytes > rowLayoutLimit {
			t.Fatal("row layout cache grew without bound")
		}
	}
	if _, ok := cache.get([]byte("first")); !ok {
		t.Fatal("recently used layout was evicted")
	}
	oversized := rowCache{runs: []glyphRun{{glyphs: make([]ui.Glyph, rowLayoutLimit/16)}}}
	cache.put([]byte("oversized"), &oversized)
	if oversized.shared {
		t.Fatal("oversized layout was retained")
	}
}

func TestRowLayoutsMatchFreshRenderingAcrossSnapshotChanges(t *testing.T) {
	phone, tt := motionPhone(t, true)
	for _, tc := range []struct{ offset, color int }{{0, 31}, {1, 31}, {0, 31}, {0, 34}, {0, 8}, {0, 0}} {
		frame := motionSnapshot(t, tc.offset, tc.color)
		phone.restoreSnapshot(frame, 48, 33)
		tt.Frame()
		plain, reference := motionPhone(t, false)
		plain.restoreSnapshot(frame, 48, 33)
		reference.Frame()
		if !bytes.Equal(tt.Image().Pix, reference.Image().Pix) {
			t.Fatalf("cached rendering changed after offset=%d color=%d", tc.offset, tc.color)
		}
		if phone.v.rowLayouts.bytes > rowLayoutLimit || len(phone.v.rowLayouts.entries) > 128 {
			t.Fatal("row cache exceeded its bounds")
		}
	}
}

func TestScrollMotionCancelsOnReverseAndDoesNotGuessRepetition(t *testing.T) {
	rows := func(names ...string) []rowCache {
		out := make([]rowCache, len(names))
		for i, name := range names {
			out[i].signature = name
		}
		return out
	}
	old := rows("header", "a", "b", "c", "d", "e", "footer")
	next := rows("header", "b", "c", "d", "e", "f", "footer")
	if top, bottom, delta := scrollRegion(old, next, 1); top != 1 || bottom != 6 || delta != 1 {
		t.Fatalf("confirmed scroll = %d,%d,%d", top, bottom, delta)
	}
	for _, pair := range [][2][]rowCache{{old, old}, {old, rows("header", "f", "e", "d", "c", "b", "footer")}, {rows("a", "b", "a", "b", "a", "b", "a"), rows("a", "b", "a", "b", "a", "b", "a")}} {
		if _, _, delta := scrollRegion(pair[0], pair[1], 1); delta != 0 {
			t.Fatal("unrelated or ambiguous content was animated")
		}
	}
	now := time.Now()
	m := scrollMotion{delta: 1, direction: 1, started: now, from: old}
	m.input(-1, now.Add(time.Millisecond))
	if m.delta != 0 || len(m.from) != 0 || m.direction != -1 {
		t.Fatal("direction reversal retained old motion")
	}
}

func TestScrollMotionDoesNotClickMovingBody(t *testing.T) {
	a, b := motionSnapshot(t, 0, 36), motionSnapshot(t, 1, 36)
	phone, tt := motionPhone(t, true)
	phone.restoreSnapshot(a, 48, 33)
	tt.Frame()
	phone.v.motion.input(20, time.Now())
	phone.restoreSnapshot(b, 48, 33)
	tt.Frame()
	if phone.v.motion.delta == 0 {
		t.Fatal("fixture did not start moving")
	}
	phone.v.input(ui.InputEvent{Kind: ui.InputPointerDown, Y: 100})
	phone.v.input(ui.InputEvent{Kind: ui.InputPointerUp, Y: 100})
	if phone.v.motion.delta != 0 || phone.v.motion.swallowClick || phone.v.reporting {
		t.Fatal("a moving body tap reached the application")
	}
}

func TestRowLayoutsRespectPaletteFontAndGraphemeChanges(t *testing.T) {
	phone, tt := motionPhone(t, true)
	plain, reference := motionPhone(t, false)
	frame := motionSnapshot(t, 0, 31)
	for _, term := range []*Terminal{phone, plain} {
		term.restoreSnapshot(frame, 48, 33)
	}
	tt.Frame()
	reference.Frame()
	steps := []func(*Terminal){
		func(term *Terminal) { term.Feed([]byte("\x1b]4;1;rgb:00/ff/00\x07")) },
		func(term *Terminal) { term.SetFont(Font{Size: 14, LineHeight: 1.2}) },
		func(term *Terminal) {
			term.restoreSnapshot(bytes.ReplaceAll(frame, []byte("e\u0301"), []byte("e\u0308")), 48, 33)
		},
	}
	for i, step := range steps {
		step(phone)
		step(plain)
		tt.Frame()
		reference.Frame()
		if !bytes.Equal(tt.Image().Pix, reference.Image().Pix) {
			t.Fatalf("layout cache became stale after palette/font/grapheme step %d", i)
		}
	}
}
