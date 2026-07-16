package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rogerai-fyi/roger/internal/glyphs"
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
// carrierSweep is pure + self-tinted + has an ASCII fallback; the one red stays on the
// eye pulse in the status line above.

const (
	meterWidth = 24 // sweep track width in glyphs (shown only with room: not narrow/compact/quiet)
	sweepBlock = 4  // width of the moving block
	sweepStep  = 2  // frames per one-column advance (160ms tick -> a step every ~320ms)
)

// carrierSweep renders the radio CARRIER (design overhaul catalog #7): a `sweepBlock`-wide
// run of ∿ (the modulated carrier) scrolling across a · quiet line - proof-of-life that a
// station is transmitting during an AGENT turn (which has no true "% done"). Same slide
// mechanic as the old meter sweep, but SELF-TINTED (tintBar only knows ▰): the wave rides
// the accent, the quiet line is dim. Folds to ~ over . under ASCII, where the glyph itself
// must carry the signal (no color to lean on). Pure: same (frame,width) -> same string; the
// caller shows it only under the reduced-motion gate, and DROPS it on a stall.
func carrierSweep(frame, width int) string {
	if width < sweepBlock+2 {
		width = sweepBlock + 2
	}
	wave, quiet := "∿", "·"
	if glyphs.ASCII() {
		wave, quiet = "~", "."
	}
	span := width + sweepBlock // travel off the right edge before wrapping back to the left
	head := (frame / sweepStep) % span
	var b strings.Builder
	b.Grow(width * 8)
	for i := 0; i < width; i++ {
		if i < head && i >= head-sweepBlock {
			b.WriteString(stLive.Render(wave))
		} else {
			b.WriteString(stDim.Render(quiet))
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

// sessionFooter renders the running-session footer line shared by BOTH the AGENT turn-final
// footer and the CHANNEL per-turn footer + in-flight readout, so the two money-facing surfaces
// never drift: a dim "session ↑in ↓out · $cost" built on the shared meterTotals, or "" while
// the session is still empty (no tokens, no cost) so a fresh surface shows no stray row. The
// caller adds its own indentation.
func sessionFooter(tokensIn, tokensOut int, cost float64) string {
	tot := meterTotals(tokensIn, tokensOut, cost)
	if tot == "" {
		return ""
	}
	return stDim.Render("session " + tot)
}

// budgetBarWidth is the determinate monthly-budget bar's width in glyphs (dropped on
// narrow terminals so the budget line never wraps).
const budgetBarWidth = 16

// meterBar renders a DETERMINATE fill bar — used where there IS a real total (e.g. the
// monthly budget: spend ÷ cap), unlike the in-turn sweep which has none. round(frac*
// width) filled ▰, the rest ▱, frac clamped to [0,1]. A real but sub-pip fraction (>0)
// still shows ONE ▰ so "some used" never reads empty. Pure + rune-accurate; tintBar
// adds color.
func meterBar(frac float64, width int) string {
	if width < 1 {
		width = 1
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac*float64(width) + 0.5)
	if fill > width {
		fill = width
	}
	if fill == 0 && frac > 0 {
		fill = 1
	}
	return strings.Repeat("▰", fill) + strings.Repeat("▱", width-fill)
}

// tintBar styles a meterBar string: filled (▰) glyphs in fillStyle, the
// empty track (▱ or ◌) dim. It only styles — it never adds or drops a glyph.
func tintBar(bar string, fillStyle lipgloss.Style) string {
	var b strings.Builder
	for _, r := range bar {
		if r == '▰' {
			b.WriteString(fillStyle.Render(string(r)))
		} else {
			b.WriteString(stDim.Render(string(r)))
		}
	}
	return b.String()
}
