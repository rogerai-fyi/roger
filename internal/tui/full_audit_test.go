package tui

// THE FULL-MODE GEOMETRY AUDIT - the expanded-view twin of compact_audit_test.go.
//
// The founder hit the stacked-logo artifact AGAIN (2026-09-01), this time in the FULL
// view: pressing i (station log) or b (band card) from the tune-in list on a short
// terminal. The compact audit could not catch it because it only walks compact=false's
// dense sibling. Same mechanics: a frame taller than the terminal scrolls the alt
// buffer and strands the previous frame's brand row above the new one; a line wider
// than the terminal wraps and shifts every later row.
//
// The fixture is deliberately the founder's screen: MANY bands (the real dial had 14),
// a detail band with enough stations to stress the station log, and a short terminal.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func fullAuditModes(t *testing.T, w, h int) map[mode]model {
	t.Helper()
	base := func() model {
		m := privateTab(t)
		m.compact = false
		m.width, m.height = w, h
		m.tuneTab = tabOpenMarket
		m.scanned = true
		m.bands = nil
		for i := 0; i < 14; i++ {
			name := fmt.Sprintf("model-%02d", i)
			m.bands = append(m.bands, band{
				model: name, online: i < 4, stations: 1 + i%3,
				cheapest: &offer{Model: name, Ctx: 8192 + i*1024, PriceOut: float64(i) * 0.1},
			})
		}
		// One band mixes human + curated so the » legend and curated chrome render.
		var all []offer
		for i := 0; i < 9; i++ {
			all = append(all, offer{
				Model: "model-00", NodeID: fmt.Sprintf("station-%d", i), Online: true,
				Curated: i >= 6, CuratedProvider: map[bool]string{true: "openrouter"}[i >= 6],
				PriceIn: 0.5, PriceOut: 1.5, Ctx: 8192,
			})
		}
		m.bands[0].all = all
		m.bands[0].curated, m.bands[0].curatedProvider = 3, "openrouter"
		m.limits = &LimitStore{Models: map[string]Limit{}}
		return m
	}
	out := map[mode]model{}
	add := func(md mode, f func(m *model)) {
		m := base()
		m.mode = md
		if f != nil {
			f(&m)
		}
		out[md] = m
	}
	add(modeBrowse, nil)
	add(modeBandDetail, func(m *model) { m.detailBand = m.bands[0] })
	add(modeBandConfig, func(m *model) { m.cfgModel = "model-00" })
	add(modeLimits, func(m *model) { mm := m; mm.enterLimits(); mm.compact = false })
	add(modeShare, nil)
	return out
}

func TestFullViewNeverOverflowsHeight(t *testing.T) {
	for _, geo := range [][2]int{{65, 24}, {65, 28}, {80, 24}, {100, 30}} {
		w, h := geo[0], geo[1]
		for md, m := range fullAuditModes(t, w, h) {
			frame := m.View()
			if rows := len(strings.Split(strings.TrimRight(frame, "\n"), "\n")); rows > h {
				t.Errorf("full %s at %dx%d: the frame is %d rows - it will scroll the alt buffer (the stacked-logo artifact)",
					modeName(md), w, h, rows)
			}
		}
	}
}

func TestFullViewNeverOverflowsWidth(t *testing.T) {
	for _, w := range []int{65, 80, 100} {
		for md, m := range fullAuditModes(t, w, 30) {
			frame := m.View()
			for i, ln := range strings.Split(frame, "\n") {
				if got := lipgloss.Width(ln); got > w {
					t.Errorf("full %s at w=%d: line %d is %d wide: %q",
						modeName(md), w, i, got, stripANSI(ln))
				}
			}
		}
	}
}
