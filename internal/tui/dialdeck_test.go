package tui

// Increment 4 of the TUI design overhaul: the AGENT dial deck + header lamps + the
// radio lockup. 4a locks the brand lockup as the ▟▄▙ radio (box + two antenna nubs),
// replacing the ambiguous ▟█▙ "tower" the founder couldn't read - and aligning the one
// mystery glyph with something that actually means "radio" on a radio-operator TUI.

import (
	"strings"
	"testing"
)

// L1/S1/M1 - a tuned-in AGENT shows the dial deck: the green ◉ LOCK lamp, the call
// sign, · AGENT, the node's S-meter, and the ↑/↓/$ meter bank.
func TestDialDeckLocked(t *testing.T) {
	colorOn(t, true)
	m := agentAt(t, permConfirm)
	m.connected.NodeID = "brave-otter-37"
	m.connected.Signal = 100
	m.connected.TPS = 800
	m.agentTokensIn, m.agentTokensOut = 5900, 359

	view := m.agentView(120)
	head := strings.SplitN(view, "\n", 2)[0]
	flat := stripANSI(head)

	if !strings.Contains(head, lampStyle(roleSignal).Render("◉")) {
		t.Error("L1: a tuned-in deck shows the green ◉ LOCK lamp")
	}
	if !strings.Contains(flat, "brave-otter-37") {
		t.Errorf("L1: the deck shows the call sign: %q", flat)
	}
	if !strings.Contains(flat, "AGENT") {
		t.Error("L1: the deck still names AGENT")
	}
	if !strings.Contains(head, m.bandSMeter(m.frame, m.connected.Signal, m.connected.TPS, true, m.connected.InFlight, 0, false)) {
		t.Errorf("S1: the deck carries the node's S-meter: %q", flat)
	}
	if !strings.Contains(flat, "↑") || !strings.Contains(flat, "↓") {
		t.Errorf("M1: the deck shows the ↑/↓ token meter bank: %q", flat)
	}
}

// L2 - while auto-tuning (no model yet) the LOCK reads ◐ TUNING in amber.
func TestDialDeckTuning(t *testing.T) {
	colorOn(t, true)
	m := agentAt(t, permConfirm)
	m.agent.model = ""
	m.autoTuning = true

	head := strings.SplitN(m.agentView(120), "\n", 2)[0]
	if !strings.Contains(stripANSI(head), "TUNING") {
		t.Errorf("L2: the tuning deck should read TUNING: %q", stripANSI(head))
	}
	if !strings.Contains(head, lampStyle(roleDialGlow).Render("◐")) {
		t.Error("L2: TUNING uses the amber ◐ lamp")
	}
}

// L3 - nothing on the dial: the deck reads "no model tuned in" (never a blank lead).
func TestDialDeckNoModel(t *testing.T) {
	m := agentAt(t, permConfirm)
	m.agent.model = ""
	m.autoTuning = false

	if !strings.Contains(stripANSI(strings.SplitN(m.agentView(120), "\n", 2)[0]), "no model tuned in") {
		t.Error("L3: the empty dial should read `no model tuned in`")
	}
}

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
