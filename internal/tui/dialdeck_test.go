package tui

// Increment 4 of the TUI design overhaul: the AGENT dial deck + header lamps + the
// radio lockup. 4a locks the brand lockup as the ▟▄▙ radio (box + two antenna nubs),
// replacing the ambiguous ▟█▙ "tower" the founder couldn't read - and aligning the one
// mystery glyph with something that actually means "radio" on a radio-operator TUI.

import (
	"strings"
	"testing"
)

// K1 - the header brand lockup renders the ▟▄▙ radio, never the old ▟█▙ tower.
func TestHeaderLockupIsRadio(t *testing.T) {
	m := browseSeed(96)
	head := stripANSI(m.header(96))
	if !strings.Contains(head, "▟▄▙") {
		t.Errorf("the brand lockup should render the ▟▄▙ radio:\n%s", head)
	}
	if strings.Contains(head, "▟█▙") {
		t.Error("the ambiguous ▟█▙ tower must be gone from the lockup")
	}
}
