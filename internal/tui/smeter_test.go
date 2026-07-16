package tui

// Increment 3 of the TUI design overhaul: the S-METER (catalog #1), the full ham-radio
// S1·3·5·7·9·+20 signal scale the founder picked. An ADDITIVE widget (smeter.go) that
// reuses the existing level primitives (signalAmp/scanOffset/signalRamp) but renders at
// the 9-unit scale with a green over-S9 "+" overzone - adopted in the band table + the
// BROWSE header. The 5-cell staircase (voice booth + CLI lock-step + its 5 test files)
// is left untouched. First render use of cSignal green.
//
// Spec approved 2026-07-15 (increment 3). No mocks: real glyph ramp, real palette
// tokens through the live renderer, real scanOffset animation.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// sMeterW is the constant visible width: 9 S-cells + the " +" over-S9 overzone.
const sMeterW = 11

// leadLit counts the leading LIT cells (not the unlit "·" rail, space, or "+") of a raw
// S-meter bar - the S-reading, which must never waver with animation.
func leadLit(raw string) int {
	n := 0
	for _, r := range raw {
		if r == '·' || r == ' ' || r == '+' || r == '░' {
			break
		}
		n++
	}
	return n
}

// U - the 0-100 (+tps +stations) scale remapped onto 9 S-units, with an over-S9 flag.
func TestSUnitsMapping(t *testing.T) {
	cases := []struct {
		name              string
		signal            int
		tps               float64
		online            bool
		inFlight, station int
		wantUnits         int
		wantOver          bool
	}{
		{"offline", 90, 0, false, 0, 1, 0, false},
		{"full signal", 100, 0, true, 0, 1, 9, false},
		{"one unit", 1, 0, true, 0, 1, 1, false},
		{"mid", 56, 0, true, 0, 1, 6, false},         // ceil(56*9/100)=6
		{"boundary 11", 11, 0, true, 0, 1, 1, false}, // ceil(0.99)=1
		{"boundary 12", 12, 0, true, 0, 1, 2, false}, // ceil(1.08)=2
		{"tps fallback strong", 0, 600, true, 0, 1, 9, false},
		{"tps ladder 450", 0, 450, true, 0, 1, 8, false},
		{"tps ladder 300", 0, 300, true, 0, 1, 7, false},
		{"tps ladder 150", 0, 150, true, 0, 1, 5, false},
		{"tps ladder 60", 0, 60, true, 0, 1, 3, false},
		{"carrier only", 0, 0, true, 0, 1, 1, false},        // online, no reading -> never blank
		{"station boost", 50, 0, true, 0, 3, 7, false},      // ceil(4.5)=5 +2
		{"boost capped at +2", 10, 0, true, 0, 5, 3, false}, // raw 1 + min(4,2)=3, not 5
		{"over S9 by boost", 100, 0, true, 0, 3, 9, true},   // 9+2 clamps to 9, over=true
		{"over S9 by tps", 0, 800, true, 0, 1, 9, true},
	}
	for _, c := range cases {
		u, over := sUnits(c.signal, c.tps, c.online, c.inFlight, c.station)
		if u != c.wantUnits || over != c.wantOver {
			t.Errorf("%s: sUnits = (%d,%v), want (%d,%v)", c.name, u, over, c.wantUnits, c.wantOver)
		}
	}
}

// R - the raw bar is CONSTANT width (columns align) and, when idle (amp 0), fills the
// first `units` cells solid with the rest as the "·" rail, then the " +" overzone.
func TestSMeterRawWidthAndFill(t *testing.T) {
	for u := 0; u <= 9; u++ {
		raw := sMeterRaw(1, u, 0)
		if got := lipgloss.Width(raw); got != sMeterW {
			t.Errorf("units %d: width %d, want constant %d (%q)", u, got, sMeterW, raw)
		}
	}
	// idle S7: seven solid cells, two rail dots, then the overzone.
	raw := sMeterRaw(1, 7, 0)
	if !strings.HasPrefix(raw, strings.Repeat("▇", 7)+"··") {
		t.Errorf("idle S7 fill wrong: %q", raw)
	}
	if !strings.HasSuffix(strings.TrimRight(raw, " "), "+") {
		t.Errorf("the overzone + must terminate the bar: %q", raw)
	}
	// units 0 = the offline / dead-air flat (no meter), still constant width.
	if flat := sMeterRaw(1, 0, 0); strings.Contains(flat, "▇") {
		t.Errorf("units 0 must render the flat no-signal bar, not lit cells: %q", flat)
	}
	// units above the scale clamp to a full S9 bar (still constant width).
	over9 := sMeterRaw(1, 12, 0)
	if lipgloss.Width(over9) != sMeterW || !strings.HasPrefix(over9, strings.Repeat("▇", 9)) {
		t.Errorf("units>9 must clamp to a full S9 bar: %q", over9)
	}
	// High-amp frontier stays a visible bar (never dips to the "·" rail) across frames.
	for f := 0; f < 6; f++ {
		if leadLit(sMeterRaw(f, 3, 3)) != 3 {
			t.Errorf("frame %d: a swinging frontier must stay lit (count 3)", f)
		}
	}
}

// R2 - animation moves the FRONTIER height, never the lit-cell COUNT: the S-reading is
// stable across frames even while the top cell breathes (amp 2 = actively serving).
func TestSMeterRawCountStableUnderAnimation(t *testing.T) {
	for frame := 0; frame < 8; frame++ {
		if got := leadLit(sMeterRaw(frame, 6, 2)); got != 6 {
			t.Errorf("frame %d: lit count = %d, want a steady 6 (animation must not move the reading)", frame, got)
		}
	}
}

// T - tint: ink lit cells, a RED glint at the S9 peak, and cSignal GREEN on a lit over-S9
// "+"; dim rail/offline; and the mono palette collapses the green (increment-0 switch).
func TestTintSMeter(t *testing.T) {
	colorOn(t, true)
	// Style-derived references (robust to termenv's truecolor rounding): the exact
	// sequences the over-S9 green "+" and the red S9-peak cell must render as.
	greenPlus := lampStyle(roleSignal).Render("+")
	redPeak := stRed.Render("▇")

	// S9 + over: the peak reds and the overzone greens.
	strong := tintSMeter(sMeterRaw(1, 9, 0), 9, true, true)
	if !strings.Contains(strong, redPeak) {
		t.Error("a lit S9 peak should carry the red glint")
	}
	if !strings.Contains(strong, greenPlus) {
		t.Error("a lit over-S9 + should light cSignal green")
	}

	// Mid, not over: no green (+ is dim), no red (S9 unlit).
	mid := tintSMeter(sMeterRaw(1, 5, 0), 5, false, true)
	if strings.Contains(mid, greenPlus) || strings.Contains(mid, redPeak) {
		t.Error("no green + and no red peak below S9")
	}

	// Offline: all dim, no lamp hues.
	off := tintSMeter(sMeterRaw(1, 0, 0), 0, false, false)
	if strings.Contains(off, greenPlus) || strings.Contains(off, redPeak) {
		t.Error("an offline meter must be all dim (no lamps)")
	}
}

// T-mono - under palette mono the green + collapses to ink; the meter stays legible.
func TestTintSMeterMonoCollapse(t *testing.T) {
	colorOn(t, true)
	greenPlus := lampStyle(roleSignal).Render("+") // the FULL-mode green reference
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })
	paletteMono = true

	strong := tintSMeter(sMeterRaw(1, 9, 0), 9, true, true)
	if strings.Contains(strong, greenPlus) {
		t.Error("mono: the over-S9 + must not emit the green lamp hue")
	}
}

// L - the legend names the S-scale once (under the SIGNAL header), plain digits so it
// survives ASCII / NO_COLOR.
func TestSMeterLegend(t *testing.T) {
	leg := stripANSI(sMeterLegend())
	for _, want := range []string{"1", "3", "5", "7", "9", "+20"} {
		if !strings.Contains(leg, want) {
			t.Errorf("legend missing %q: %q", want, leg)
		}
	}
}
