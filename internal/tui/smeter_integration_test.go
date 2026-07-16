package tui

// Increment 3b: the band table + BROWSE header adopt the S-meter widget (3a), with the
// S1·3·5·7·9·+20 legend under the SIGNAL header. These lock the INTEGRATION - the widget
// actually lands in the rendered browse view - and, critically, that widening the SIGNAL
// column does not overflow the fixed grid at 80-col or wider.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The wide band table shows the S-meter legend once under the signal header, and no row
// overflows the terminal width (the column widen must stay within the grid).
func TestBandTableShowsSMeterAndFits(t *testing.T) {
	for _, w := range []int{80, 96, 120} {
		m := browseSeed(w)
		view := m.browseView(w)
		flat := stripANSI(view)
		if !strings.Contains(flat, "1 3 5 7 9 +20") {
			t.Errorf("w=%d: band table should show the S-meter legend under the signal header:\n%s", w, flat)
		}
		for _, ln := range strings.Split(view, "\n") {
			if lipgloss.Width(ln) > w {
				t.Errorf("w=%d: a row overflows the width: %q (%d cols)", w, stripANSI(ln), lipgloss.Width(ln))
			}
		}
	}
}

// Narrow mode drops the signal column entirely (band · on air · price), so the legend
// must NOT appear there - it would be dangling under no column.
func TestBandTableNarrowHasNoLegend(t *testing.T) {
	m := browseSeed(50) // < 64 => narrow layout
	if strings.Contains(stripANSI(m.browseView(50)), "1 3 5 7 9 +20") {
		t.Error("narrow layout drops the signal column, so it must not show the S-scale legend")
	}
}
