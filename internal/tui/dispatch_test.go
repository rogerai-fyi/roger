package tui

// Increment 8 of the TUI design overhaul: SHARE as a DISPATCH CONSOLE (catalog #6). Each
// shared model gets a pilot lamp - ● on air (green), ◐ warming/reconnecting (amber), ○
// idle/off-air (dim) - so the whole fleet's status reads in one glance down the column,
// like a dispatch console's unit-status lamps.

import (
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// P1 - the pilot-lamp mapping: glyph + lamp by (on-air, link state).
func TestPilotLampMapping(t *testing.T) {
	colorOn(t, true)
	cases := []struct {
		name  string
		on    bool
		link  agent.LinkState
		glyph string
		style string // the lamp's foreground, style-derived
	}{
		{"idle/off-air", false, agent.LinkConnecting, "○", stDim.Render("○")},
		{"on air", true, agent.LinkOnAir, "●", lampStyle(roleSignal).Render("●")},
		{"warming", true, agent.LinkConnecting, "◐", lampStyle(roleDialGlow).Render("◐")},
		{"reconnecting", true, agent.LinkReconnecting, "◐", lampStyle(roleDialGlow).Render("◐")},
	}
	for _, c := range cases {
		glyph, style := pilotLamp(c.on, c.link)
		if glyph != c.glyph {
			t.Errorf("%s: glyph = %q, want %q", c.name, glyph, c.glyph)
		}
		if style.Render(glyph) != c.style {
			t.Errorf("%s: lamp color wrong", c.name)
		}
	}
}

// P2 - the SHARE table shows an idle ○ lamp per off-air model (the dispatch glance).
func TestShareRowsShowPilotLamp(t *testing.T) {
	srv := fakeBroker(t)
	mm := NewWithHooks(srv.URL, "tester", nil, Hooks{})
	mm.setShareRows(freeRows(3)) // 3 off-air models
	mm.mode = modeShare
	mm.width, mm.height = 100, 30

	if !strings.Contains(stripANSI(mm.shareView(100)), "○") {
		t.Errorf("each off-air share row should carry the idle ○ pilot lamp:\n%s", stripANSI(mm.shareView(100)))
	}
}
