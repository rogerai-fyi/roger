package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestBandRowCoolingMarker pins features/routing/upstream_failover.feature "the dial shows a
// cooling station as on air, marked": a station carrying cooling_until renders ON AIR with a
// "cooling Ns" marker (the seconds remaining), and the band is not dark because of it.
func TestBandRowCoolingMarker(t *testing.T) {
	until := time.Now().Add(42 * time.Second).Unix()
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm, _ = tm.Update(offersMsg([]offer{{NodeID: "s1", Model: "m", PriceIn: 1, PriceOut: 1, Online: true, Signal: 50, CoolingUntil: until}}))
	m := tm.(model)
	if len(m.bands) == 0 {
		t.Fatal("no band grouped from the offer")
	}
	m.detailBand = m.bands[0]
	m.mode = modeBandDetail
	out := stripANSI(m.View())
	if !strings.Contains(out, "cooling 4") { // 41 or 42 seconds depending on the tick
		t.Fatalf("cooling station row lacks the cooling marker with the seconds remaining:\n%s", out)
	}
	if !strings.Contains(out, glyphOnAir) {
		t.Errorf("a cooling station must still read ON AIR:\n%s", out)
	}
	if c := coolingCell(offer{Online: true, CoolingUntil: time.Now().Add(-time.Second).Unix()}, time.Now()); c != "" {
		t.Errorf("a lapsed cooldown must not mark the row: %q", c)
	}
	if c := coolingCell(offer{Online: true}, time.Now()); c != "" {
		t.Errorf("a station that is not cooling must not be marked: %q", c)
	}
}
