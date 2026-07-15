package tui

import (
	"testing"
)

// TestSignalStaircaseShapes locks the beauty-director spec: the meter is a cellphone
// staircase (lit ascending bars over a visible rail), the count of lit bars IS the
// signal, and the reduced-motion frozen frame (anim pins frame=1, where scanOffset
// is 0) renders the pure staircase at any activity level.
func TestSignalStaircaseShapes(t *testing.T) {
	cases := map[int]string{ // broker signal -> frozen render
		0:   "▃▁▁▁▁", // online floor: never blank
		20:  "▃▁▁▁▁",
		40:  "▃▄▁▁▁",
		60:  "▃▄▅▁▁",
		80:  "▃▄▅▇▁",
		100: "▃▄▅▇█",
	}
	for sig, want := range cases {
		for _, amp := range []int{0, 1, 2} { // frozen frame is identical at every amp
			got := signalTowerAt(1, maxInt(signalLevel(sig), 1), amp)
			if got != want {
				t.Errorf("signal %d amp %d frozen = %q, want %q", sig, amp, got, want)
			}
		}
	}
	// Offline stays the flat rail.
	if got := signalBarsRaw(1, 50, 100, false, 1, 1); got != signalFlat() {
		t.Errorf("offline = %q, want %q", got, signalFlat())
	}
	// Stations boost adds bars on the count, capped at the full meter.
	if got := signalBarsRaw(1, 50, 0, true, 0, 3); got != "▃▄▅▇█" {
		t.Errorf("signal 50 + 3 stations = %q, want the full staircase", got)
	}
	// Motion moves ONLY the top of the staircase: at any frame/amp, all bars below
	// the top (and the rail) match the frozen render, so the count never wavers.
	for frame := 0; frame < 8; frame++ {
		for _, amp := range []int{1, 2} {
			got := []rune(signalTowerAt(frame, 3, amp))
			if got[0] != '▃' || got[3] != '▁' || got[4] != '▁' {
				t.Errorf("frame %d amp %d: base/rail moved: %q", frame, amp, string(got))
			}
			if got[2] == '▁' {
				t.Errorf("frame %d amp %d: the top bar collapsed into the rail: %q", frame, amp, string(got))
			}
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
