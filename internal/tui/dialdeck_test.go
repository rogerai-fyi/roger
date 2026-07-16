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

// G1 - the tube-glow: while a channel is open, a faint amber wash sits behind the brand
// lockup (the "set is warm"). ANSI256+ only, full palette only.
func TestTubeGlowWhenLive(t *testing.T) {
	colorOn(t, true) // TrueColor => canTint true
	rq := quiet
	quiet = false // simulate an interactive TTY (tests run non-TTY, where canTint is off)
	t.Cleanup(func() { quiet = rq })
	m := browseSeed(96)
	m.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	glow := stBrand.Background(cTubeGlow).Render(" R O G E R")
	if !strings.Contains(m.header(96), glow) {
		t.Error("G1: a live session should wash the brand lockup with the amber tube-glow")
	}
}

// G2 - the escape hatch: palette mono drops the glow entirely (the lockup renders plain),
// so the mono+red fallback never carries a stray amber wash.
func TestTubeGlowOffInMono(t *testing.T) {
	colorOn(t, true)
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })
	paletteMono = true
	rq := quiet
	quiet = false
	t.Cleanup(func() { quiet = rq })
	m := browseSeed(96)
	m.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	if strings.Contains(m.header(96), stBrand.Background(cTubeGlow).Render(" R O G E R")) {
		t.Error("G2: mono palette must not paint the tube-glow")
	}
}

// G3 - no glow when nothing is tuned in (the set is cold).
func TestTubeGlowOffWhenNotLive(t *testing.T) {
	colorOn(t, true)
	rq := quiet
	quiet = false
	t.Cleanup(func() { quiet = rq })
	m := browseSeed(96)
	m.connected = nil
	if strings.Contains(m.header(96), stBrand.Background(cTubeGlow).Render(" R O G E R")) {
		t.Error("G3: an idle (no channel) header must not glow")
	}
}

// A1 - the ON AIR lamp (catalog #4): the sharing headline reads a red ON AIR (guard so
// the dial-deck work doesn't disturb the existing on-air indicator).
func TestOnAirLampReadsOnAir(t *testing.T) {
	m := browseSeed(96)
	if !strings.Contains(stripANSI(m.headlineBadge()), "ON AIR") {
		t.Error("A1: the sharing headline badge must read ON AIR")
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
