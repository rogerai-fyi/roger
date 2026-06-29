package tui

import (
	"strings"
)

// ── live telemetry meter ──────────────────────────────────────────────────────
// A Claude-Code-style readout for an in-flight AGENT turn: the working line carries
// the words (state + elapsed within the cap + running cost) and, beneath it, a
// SIGNAL SWEEP — a short block of glyphs sliding across a track like a tuner seeking
// a band. An AGENT turn has no true "% done" (unlike compaction, which knows its
// total), so the sweep is honest INDETERMINATE liveness: it reads "alive + working"
// without claiming a fraction (the founder's choice over a fake determinate bar). A
// genuine stall DROPS the sweep (see agentWorkingLine) so motion never implies
// progress that isn't happening.
//
// meterSweep is pure + rune-accurate + NO_COLOR-safe (glyphs only); tintSweep adds
// the mono accent (the one red stays on the eye pulse in the status line above).

const (
	meterWidth = 24 // sweep track width in glyphs (shown only with room: not narrow/compact/quiet)
	sweepBlock = 4  // width of the moving block
	sweepStep  = 2  // frames per one-column advance (160ms tick -> a step every ~320ms)
)

// meterSweep renders the bare signal-sweep glyphs for the given frame and width: a
// `sweepBlock`-wide run of ▰ entering from the left, crossing, exiting right, then
// re-entering — a radio tuner sweeping the band. Pure: same (frame,width) -> same
// string. width is clamped up so a degenerate size still renders a real bar.
func meterSweep(frame, width int) string {
	if width < sweepBlock+2 {
		width = sweepBlock + 2
	}
	span := width + sweepBlock // travel off the right edge before wrapping back to the left
	head := (frame / sweepStep) % span
	var b strings.Builder
	b.Grow(width * 3)
	for i := 0; i < width; i++ {
		if i < head && i >= head-sweepBlock {
			b.WriteRune('▰')
		} else {
			b.WriteRune('◌')
		}
	}
	return b.String()
}

// tintSweep applies the mono accent to a meterSweep string: the moving block in the
// live body tone, the track dim. It styles only — it never adds or drops a glyph.
func tintSweep(bar string) string {
	var b strings.Builder
	for _, r := range bar {
		if r == '▰' {
			b.WriteString(stLive.Render(string(r)))
		} else {
			b.WriteString(stDim.Render(string(r)))
		}
	}
	return b.String()
}
