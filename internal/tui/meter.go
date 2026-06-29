package tui

import (
	"fmt"
	"strconv"
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

// ── session telemetry totals (↑in ↓out · $cost) ──────────────────────────────
// The honest billed-token readout: the broker re-counts every relay and bills the LESSER
// of the node's claim and its own count per axis; it returns those BILLED counts in the
// response headers (X-RogerAI-Tokens-In/Out) next to the billed cost. The harness sums
// them into running session totals, and these two pure helpers render them — shared by the
// live working-line meter and the per-turn session summary so the two never drift. This is
// DISPLAY of an already-settled value; it changes no billing.

// fmtTokens renders a token count for the meter: exact below 1000, then a one-decimal "k"
// (1234 -> "1.2k") so an accumulating session stays compact yet keeps visibly moving. A
// negative input (impossible for a count) clamps to "0".
func fmtTokens(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// meterTotals renders the running session telemetry as "↑<in> ↓<out> · $<cost>". The
// token half is omitted until there are tokens, the cost while it is still zero (dust-safe
// via dollars()), and the whole string is empty when there is nothing yet — so an idle
// meter shows no stray separator. Pure: callers add their own styling.
func meterTotals(tokensIn, tokensOut int, cost float64) string {
	var parts []string
	if tokensIn > 0 || tokensOut > 0 {
		parts = append(parts, "↑"+fmtTokens(tokensIn)+" ↓"+fmtTokens(tokensOut))
	}
	if cost > 0 {
		parts = append(parts, dollars(cost))
	}
	return strings.Join(parts, " · ")
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
