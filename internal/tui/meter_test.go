package tui

import (
	"strings"
	"testing"
)

// TestMeterSweep pins the live-telemetry SIGNAL SWEEP: a short block of glyphs that
// slides across a fixed-width track (honest indeterminate liveness for an AGENT turn,
// which has no true %). Pure + rune-accurate + NO_COLOR-safe.
func TestMeterSweep(t *testing.T) {
	// width is respected, counted in RUNES (the glyphs are multibyte).
	for _, w := range []int{8, 24, 40} {
		if n := len([]rune(meterSweep(0, w))); n != w {
			t.Errorf("meterSweep(0,%d): rune width %d, want %d", w, n, w)
		}
	}
	// only the two sweep glyphs ever appear.
	for _, r := range meterSweep(7, meterWidth) {
		if r != '▰' && r != '◌' {
			t.Errorf("meterSweep produced unexpected glyph %q", r)
		}
	}
	// at frame 0 the block has not entered yet (clean start: all track).
	if strings.ContainsRune(meterSweep(0, meterWidth), '▰') {
		t.Error("meterSweep(0) should start with the block off-track (all ◌)")
	}
	// the block SLIDES RIGHT as the frame advances: the first ▰ index strictly
	// increases while the block is mid-track - the honest "scanning" motion.
	firstOn := func(s string) int {
		for i, r := range []rune(s) {
			if r == '▰' {
				return i
			}
		}
		return -1
	}
	a := firstOn(meterSweep(sweepStep*(sweepBlock+2), meterWidth)) // head = sweepBlock+2
	b := firstOn(meterSweep(sweepStep*(sweepBlock+4), meterWidth)) // head = sweepBlock+4
	if a < 0 || b < 0 || b <= a {
		t.Errorf("sweep should advance rightward: firstOn(a)=%d firstOn(b)=%d", a, b)
	}
	// degenerate tiny width clamps up instead of rendering an empty/blank bar.
	if got := meterSweep(5, 1); len([]rune(got)) < sweepBlock+2 {
		t.Errorf("tiny width should clamp up, got %q (len %d)", got, len([]rune(got)))
	}
	// tintSweep only styles - it must not add or drop any sweep glyphs.
	countOn := func(s string) (n int) {
		for _, r := range s {
			if r == '▰' {
				n++
			}
		}
		return
	}
	plain := meterSweep(9, meterWidth)
	if countOn(tintSweep(plain)) != countOn(plain) {
		t.Errorf("tintSweep changed the block glyph count: %d vs %d", countOn(tintSweep(plain)), countOn(plain))
	}
}

// TestAgentWorkingLineMeter pins the meter's integration into the AGENT working line:
// under full motion the status line surfaces the running cost and a signal-sweep bar
// rides beneath it; a genuine stall AND the reduced-motion (compact) form both drop
// the sweep to a single line (motion must never imply liveness that isn't there).
func TestAgentWorkingLineMeter(t *testing.T) {
	// tests run reduced-motion (quiet=true), which collapses the meter; force full
	// motion so the sweep bar renders, and restore it afterward.
	defer func(q bool) { quiet = q }(quiet)
	quiet = false

	m := browseSeed(120) // wide enough that narrow() is false -> the bar shows
	m.agentTurnState = poseThinking
	m.agentCost = 0.05

	out := stripANSI(m.agentWorkingLine(5, 1))
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected a 2-line meter (status + sweep), got %d line(s): %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "$0.05") {
		t.Errorf("status line should surface the running cost, got %q", lines[0])
	}
	if !strings.ContainsRune(lines[1], '◌') {
		t.Errorf("the second line should be the signal-sweep track, got %q", lines[1])
	}

	// a genuine stall DROPS the sweep - collapses to the single honest warning line.
	stalled := stripANSI(m.agentWorkingLine(agentStallSec+10, agentStallSec+5))
	if strings.Contains(stalled, "\n") {
		t.Errorf("a stalled turn must collapse to one line (no sweep), got %q", stalled)
	}

	// compact (windowshade / reduced-motion) collapses to a single status line too.
	m.compact = true
	if cm := stripANSI(m.agentWorkingLine(5, 1)); strings.Contains(cm, "\n") {
		t.Errorf("compact should not show the sweep bar, got %q", cm)
	}
}

// TestFmtTokens pins the meter's token formatter: exact below 1000, a one-decimal "k"
// above (so an accumulating session readout stays compact yet keeps moving), and a
// clamp to 0 for the impossible-negative input.
func TestFmtTokens(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{
		{0, "0"}, {1, "1"}, {42, "42"}, {999, "999"},
		{1000, "1.0k"}, {1234, "1.2k"}, {5678, "5.7k"}, {47650, "47.6k"},
		{-5, "0"},
	} {
		if got := fmtTokens(c.n); got != c.want {
			t.Errorf("fmtTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestMeterTotals pins the shared session-telemetry renderer "↑<in> ↓<out> · $<cost>":
// the token half is omitted until there are tokens, the cost while it is still zero, and
// the whole string is empty when there is nothing yet (so an idle meter shows no stray
// separators). Reused by the live working line, the per-turn summary, and the wallet panel.
func TestMeterTotals(t *testing.T) {
	for _, c := range []struct {
		name    string
		in, out int
		cost    float64
		want    string
	}{
		{"empty", 0, 0, 0, ""},
		{"cost only", 0, 0, 0.05, "$0.05"},
		{"tokens only", 100, 250, 0, "↑100 ↓250"},
		{"both", 1200, 3400, 0.05, "↑1.2k ↓3.4k · $0.05"},
		{"one axis zero", 100, 0, 0.01, "↑100 ↓0 · $0.01"},
	} {
		if got := meterTotals(c.in, c.out, c.cost); got != c.want {
			t.Errorf("%s: meterTotals(%d,%d,%g) = %q, want %q", c.name, c.in, c.out, c.cost, got, c.want)
		}
	}
}

// TestAgentWorkingLineTokens pins the HONEST ↑↓ token readout in the live meter: with no
// tokens yet the working line shows no arrows; once the session has accrued billed tokens
// the status line surfaces "↑<in> ↓<out>" beside the running cost (the broker's billed
// re-count, exposed for display only).
func TestAgentWorkingLineTokens(t *testing.T) {
	defer func(q bool) { quiet = q }(quiet)
	quiet = false

	m := browseSeed(120)
	m.agentTurnState = poseThinking

	// No tokens yet: no arrows in the readout.
	if got := stripANSI(m.agentWorkingLine(5, 1)); strings.Contains(got, "↑") || strings.Contains(got, "↓") {
		t.Errorf("with no tokens the meter must not show ↑↓ arrows, got %q", got)
	}

	// After accruing billed tokens, the readout shows them.
	m.agentTokensIn = 1234
	m.agentTokensOut = 5678
	got := stripANSI(m.agentWorkingLine(5, 1))
	if !strings.Contains(got, "↑1.2k") || !strings.Contains(got, "↓5.7k") {
		t.Errorf("the meter should surface ↑1.2k ↓5.7k, got %q", got)
	}
}

// TestAgentCostMsgAccumulatesTokens pins the model side of the cost side-channel: each
// agentCostMsg (one model call's billed result) ADDS its cost + token counts to the
// running session totals, so a multi-call turn accrues an honest cumulative ↑↓ + cost.
func TestAgentCostMsgAccumulatesTokens(t *testing.T) {
	m := browseSeed(120)
	nm, _ := m.Update(agentCostMsg{cost: 0.01, tokensIn: 100, tokensOut: 250})
	m = asModel(nm)
	nm, _ = m.Update(agentCostMsg{cost: 0.02, tokensIn: 50, tokensOut: 75})
	m = asModel(nm)
	if m.agentTokensIn != 150 || m.agentTokensOut != 325 {
		t.Errorf("accumulated tokens = ↑%d ↓%d, want ↑150 ↓325", m.agentTokensIn, m.agentTokensOut)
	}
	if d := m.agentCost - 0.03; d > 1e-9 || d < -1e-9 {
		t.Errorf("accumulated cost = %v, want 0.03", m.agentCost)
	}
}
