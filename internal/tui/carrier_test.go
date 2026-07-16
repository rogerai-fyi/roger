package tui

// Increment 5 of the TUI design overhaul: the CARRIER / keying busy state (catalog #7).
// While a turn streams, the agent working line shows a scrolling ∿ carrier - proof-of-
// life that the station is transmitting, not stuck. Reuses the shared frame counter (no
// new loop) and the reduced-motion gate; the carrier is self-tinted (tintBar only accents
// ▰). Agent-approved design (2026-07-16) with 4 conditions folded in: keep "receiving…"
// (directional honesty), self-tint, an ASCII/mono fallback for ∿, and name the key with
// the BREAK proword.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// C1 - the carrier is constant width, carries the wave + quiet glyphs, and SLIDES with
// the frame (the scrolling RF carrier), same mechanic as meterSweep.
func TestCarrierSweepShape(t *testing.T) {
	a := carrierSweep(sweepStep*6, meterWidth) // head=6: block mid-track
	if got := lipgloss.Width(stripANSI(a)); got != meterWidth {
		t.Errorf("carrier width = %d, want %d", got, meterWidth)
	}
	flat := stripANSI(a)
	if !strings.Contains(flat, "∿") || !strings.Contains(flat, "·") {
		t.Errorf("carrier should be a ∿ block over a · quiet line: %q", flat)
	}
	if stripANSI(carrierSweep(0, meterWidth)) == stripANSI(carrierSweep(2*sweepStep, meterWidth)) {
		t.Error("the carrier must slide as the frame advances")
	}
}

// C2 - the carrier SELF-TINTS (tintBar only knows ▰): the wave rides the accent, the
// quiet line is dim.
func TestCarrierSweepSelfTinted(t *testing.T) {
	colorOn(t, true)
	out := carrierSweep(sweepStep*6, meterWidth)
	if !strings.Contains(out, stLive.Render("∿")) {
		t.Error("C2: the carrier wave should carry the accent (self-tinted)")
	}
	if !strings.Contains(out, stDim.Render("·")) {
		t.Error("C2: the carrier quiet line should be dim")
	}
}

// C3 - the mono/ASCII fallback: where ∿ may render as tofu (and no color carries it), the
// carrier folds to ~ over . so the glyph still reads.
func TestCarrierSweepASCIIFallback(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	flat := stripANSI(carrierSweep(sweepStep*6, meterWidth))
	if strings.Contains(flat, "∿") {
		t.Errorf("ASCII: the ∿ wave must fold away: %q", flat)
	}
	if !strings.Contains(flat, "~") {
		t.Errorf("ASCII: the carrier should fall back to ~: %q", flat)
	}
}

// W-carrier - a streaming turn shows the carrier + the honest "receiving…" label + the
// esc:BREAK interrupt hint, and NOT the old ▰ meter sweep.
func TestWorkingLineShowsCarrier(t *testing.T) {
	rq := quiet
	quiet = false // the carrier line only rides when not reduced-motion
	t.Cleanup(func() { quiet = rq })
	m := agentAt(t, permConfirm)
	m.agentTurnState = poseStreaming
	m.frame = sweepStep * 6 // carrier mid-track

	line := m.agentWorkingLine(5, 0)
	flat := stripANSI(line)
	if !strings.Contains(flat, "∿") {
		t.Errorf("the streaming carrier should show the ∿ wave:\n%s", flat)
	}
	if strings.Contains(flat, "▰") {
		t.Error("the old ▰ meter sweep must be replaced by the carrier")
	}
	if !strings.Contains(flat, "receiving…") {
		t.Error("keep the honest `receiving…` label (the operator receives from the station)")
	}
	if !strings.Contains(flat, "BREAK") || !strings.Contains(flat, "esc") {
		t.Errorf("the carrier line names the interrupt: esc:BREAK, got:\n%s", flat)
	}
}

// W-stalled - THE cardinal invariant: a stalled turn must show NO moving carrier (a
// moving bar must never imply liveness that isn't there).
func TestWorkingLineStalledDropsCarrier(t *testing.T) {
	rq := quiet
	quiet = false
	t.Cleanup(func() { quiet = rq })
	m := agentAt(t, permConfirm)
	m.agentTurnState = poseStreaming

	flat := stripANSI(m.agentWorkingLine(200, agentStallSec+5))
	if strings.Contains(flat, "∿") {
		t.Errorf("a stalled turn must DROP the carrier (no false liveness): %q", flat)
	}
	if !strings.Contains(flat, "no response") {
		t.Errorf("a stalled turn should say so honestly: %q", flat)
	}
}
