package fleetview

import (
	"strings"
	"testing"

	lm "github.com/thebanri/limoni"
	"github.com/thebanri/limoni/uitest"
)

// A level over the threshold always shows red: 92% against 90% lights the cell past the line
// even when rounding alone would not reach it, and the threshold tick sits where the rule is.
func TestMeterShowsRedPastTheThresholdAndATickAtIt(t *testing.T) {
	spans := meterN(0.92, 0.90, 0, false, 9)
	red, tick := false, false
	for _, s := range spans {
		if s.Style == lm.Fg(live) && strings.Contains(s.Text, "▇") {
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

// A narrow terminal gives up the tab STRIP before it gives up a fact: the alert count survives, and
// the tab in force still reads in full (found when a seventh tab pushed the count off a 100-column
// window).
func TestBrandBandGivesUpTheTabsBeforeTheAlertCount(t *testing.T) {
	tabs := []string{"fleet", "andon", "trouble", "grants", "sessions", "health", "events"}
	for _, w := range []uint16{160, 120, 100, 90, 80} {
		page := uitest.Run(t, w, 6, func(fr *lm.Frame, _ *lm.Event) bool {
			brandBand(fr, lm.Rect{Width: w, Height: 2}, tabs, 2, "12:00:00 · 3 units", 2, true, 0)
			return true
		})
		s := page.Screen()
		if !strings.Contains(s, "▲ 2") {
			t.Fatalf("at %d columns the alert count was lost:\n%s", w, s)
		}
		if !strings.Contains(s, "TROUBLE") {
			t.Fatalf("at %d columns the tab in force lost its name:\n%s", w, s)
		}
	}
}
