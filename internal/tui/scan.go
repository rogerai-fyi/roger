package tui

// scan.go - the screens that count as asking for a LAN scan (features/edge/scan_on_request.feature).
//
// A machine outside an Edge browses the LAN only on request. In the TUI the request is a
// screen being open: the [3] EDGE screen asks for an Edge scan, and in LAN mode the dial asks
// for a scan of advertised Towers. Leaving the screen closes the request. The host decides what
// a request allows; this file only says when one is open.

// scanWanted is the set of scan kinds the current screen asks for.
func (m *model) scanWanted() map[string]bool {
	want := map[string]bool{}
	switch {
	case m.mode == modeEdge:
		want["edge"] = true
	case m.mode == modeBrowse && LANMode():
		want["lan"] = true
	}
	return want
}

// syncScans opens the scans the current screen asks for and closes the rest.
func (m *model) syncScans() {
	if m.hooks.EdgeScan == nil {
		return
	}
	want := m.scanWanted()
	if m.scanEnds == nil {
		m.scanEnds = map[string]func(){}
	}
	for kind, end := range m.scanEnds {
		if !want[kind] {
			end()
			delete(m.scanEnds, kind)
		}
	}
	for kind := range want {
		if m.scanEnds[kind] == nil {
			m.scanEnds[kind] = m.hooks.EdgeScan(kind)
		}
	}
}
