package terminal

// OutputRevision is a cheap change token; it does not format terminal text.
func (t *Terminal) OutputRevision() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.revision
}

// RecentOutput reads at most 256 physical rows/64 KiB of decoded output.
// It is safe on a worker, skips unchanged screens, and never scrolls/resizes.
func (t *Terminal) RecentOutput(rows int, previous uint64) (text string, revision uint64, changed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.term == nil || t.closed || t.revision == previous {
		return "", t.revision, false
	}
	return t.term.RecentText(rows), t.revision, true
}
