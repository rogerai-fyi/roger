package fleetview

import (
	"strings"
	"testing"
)

// A level over the threshold always shows red: 92% against 90% lights the cell past the line
// even when rounding alone would not reach it, and the threshold tick sits where the rule is.
func TestMeterShowsRedPastTheThresholdAndATickAtIt(t *testing.T) {
	spans := meterN(0.92, 0.90, 0, false, 9)
	red, tick := false, false
	for _, s := range spans {
		if s.Style == boldOrPlain(live) && strings.Contains(s.Text, "▇") {
			red = true
		}
		if strings.Contains(s.Text, "┊") {
			tick = true
		}
	}
	if !red {
		t.Fatalf("92%% against 90%% must light a red cell: %+v", spans)
	}
	if tick {
		t.Fatalf("the tick is drawn only where the level has not reached the line: %+v", spans)
	}
	spans = meterN(0.50, 0.90, 0, false, 9)
	joined := ""
	for _, s := range spans {
		joined += s.Text
	}
	if !strings.Contains(joined, "┊") {
		t.Fatalf("under the line the tick marks where it is: %q", joined)
	}
}
