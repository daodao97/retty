package main

// Keep the display awake only while the user is viewing session details.
// Rendering and lifecycle changes may repeat; each visit holds one lease.
func (m *mobileApp) syncScreenAwake() {
	want := m.screenActive && !m.background && !m.home && m.term != nil && !m.preview.busy
	if want {
		if m.releaseScreenAwake == nil && m.keepScreenAwake != nil {
			m.releaseScreenAwake = m.keepScreenAwake("Retty 会话详情", true)
		}
	} else if m.releaseScreenAwake != nil {
		m.releaseScreenAwake()
		m.releaseScreenAwake = nil
	}
}
