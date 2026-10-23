package vt

import (
	"runtime"
	"unsafe"
)

// RecentText formats a bounded tail without changing the viewport or selection.
// A failed native range lookup returns no text, never the entire scrollback.
func (t *Terminal) RecentText(maxRows int) string {
	maxRows = min(max(maxRows, 1), 256)
	cols, _ := t.Size()
	total := int(t.Scrollbar().Total)
	if total < 1 || cols < 1 {
		return ""
	}
	var a, b GridRef
	a.size, b.size = unsafe.Sizeof(a), unsafe.Sizeof(b)
	if terminalGridRef(t.h, point{tag: 2, y: uint32(max(total-maxRows, 0))}, &a) != 0 || terminalGridRef(t.h, point{tag: 2, x: uint16(cols - 1), y: uint32(total - 1)}, &b) != 0 {
		return ""
	}
	selection := Selection{size: unsafe.Sizeof(Selection{}), start: a, end: b}
	opts := formatterOptions{size: unsafe.Sizeof(formatterOptions{}), unwrap: true, trim: true, selection: uintptr(unsafe.Pointer(&selection))}
	opts.extra.size = unsafe.Sizeof(opts.extra)
	opts.extra.screen.size = unsafe.Sizeof(opts.extra.screen)
	data := t.formatBytes(opts)
	runtime.KeepAlive(&selection)
	if len(data) > 64<<10 {
		data = data[len(data)-(64<<10):]
	}
	return string(data)
}
