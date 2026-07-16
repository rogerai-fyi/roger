package tui

// smeter.go - increment 3 of the radio-operator overhaul: the S-METER, the full ham-radio
// S1·3·5·7·9·+20 signal scale (the founder's pick). An ADDITIVE widget: it reuses the
// existing level primitives (signalRamp/scanOffset/anim) but renders at the 9-unit scale
// with a green over-S9 "+" overzone. Adopted in the band table + the BROWSE header; the
// 5-cell staircase (signalTowerAt) stays put for the voice booth + the CLI lock-step +
// its 5 test files. First render use of the cSignal green lamp.

import "strings"

// sMeterCells is the S1..S9 field width; the rendered bar adds a " +" over-S9 overzone.
const sMeterCells = 9

// sUnits maps the broker's 0..100 signal (with the tps fallback + station boost, mirroring
// signalBarsRaw) onto the 9-unit S-scale, returning the lit reading (0..9) and an over-S9
// flag - a genuinely strong node that pushes past S9 lights the green "+". Offline reads
// (0,false) so the caller renders dead air. Never blanks an online carrier (min S1).
func sUnits(signal int, tps float64, online bool, inFlight, stations int) (int, bool) {
	if !online {
		return 0, false
	}
	// Raw units BEFORE clamping, so a strong node can exceed S9 and set `over`.
	raw := 0
	if signal > 0 {
		raw = (signal*sMeterCells + 99) / 100 // ceil(signal*9/100)
	} else {
		switch {
		case tps >= 600:
			raw = 9
		case tps >= 450:
			raw = 8
		case tps >= 300:
			raw = 7
		case tps >= 150:
			raw = 5
		case tps >= 60:
			raw = 3
		case tps > 0:
			raw = 1
		}
	}
	if raw == 0 {
		raw = 1 // online with no reading is still a carrier, never a blank meter
	}
	if stations > 1 { // a crowded band carries a stronger signal: +1 per extra, cap +2
		if boost := stations - 1; boost > 2 {
			raw += 2
		} else {
			raw += boost
		}
	}
	over := raw > sMeterCells || tps >= 750
	if raw > sMeterCells {
		raw = sMeterCells
	}
	return raw, over
}

// sMeterRaw renders the constant-width S-meter bar at an already-resolved frame: the first
// `units` cells solid, the rest the "·" rail, then the " +" over-S9 marker. The frontier
// (top lit) cell breathes DOWN the ramp when amp>0 (actively serving) and freezes under
// quiet - but the lit-cell COUNT never moves, so the S-reading stays put while it animates.
// units 0 renders the flat dead-air bar. Width is constant so the SIGNAL column aligns.
func sMeterRaw(frame, units, amp int) string {
	if units <= 0 {
		return strings.Repeat("░", sMeterCells+2) // dead air, full constant width
	}
	if units > sMeterCells {
		units = sMeterCells
	}
	ramp := signalRamp() // ▁▂▃▄▅▆▇█ (index 0..7); index 6 (▇) is the standard lit cell
	const litIdx = 6
	var b strings.Builder
	for i := 0; i < sMeterCells; i++ {
		switch {
		case i < units-1:
			b.WriteRune(ramp[litIdx])
		case i == units-1: // the frontier cell: breathe, but never dip to the rail
			idx := litIdx
			if amp > 0 {
				idx += scanOffset(anim(frame), amp)
			}
			if idx < 2 {
				idx = 2
			}
			if idx > len(ramp)-1 {
				idx = len(ramp) - 1
			}
			b.WriteRune(ramp[idx])
		default:
			b.WriteRune('·')
		}
	}
	b.WriteString(" +")
	return b.String()
}

// tintSMeter grades the raw bar: dim rail/offline, ink lit cells, a red glint at the S9
// PEAK (the top cell once the meter reaches S9), and the cSignal GREEN on a lit over-S9
// "+" (the first green lamp; collapses to ink under palette mono via lampStyle). The
// selected reverse-video row passes the raw bar and skips this, so one accent governs it.
func tintSMeter(raw string, units int, over, online bool) string {
	if !online || units <= 0 {
		return stDim.Render(raw)
	}
	var b strings.Builder
	lit := 0
	for _, r := range raw {
		switch {
		case r == '+':
			if over {
				b.WriteString(lampStyle(roleSignal).Render("+"))
			} else {
				b.WriteString(stDim.Render("+"))
			}
		case r == '·' || r == ' ' || r == '░':
			b.WriteString(stDim.Render(string(r)))
		default: // a lit bar cell
			lit++
			if units >= sMeterCells && lit == units { // the S9 peak cell
				b.WriteString(stRed.Render(string(r)))
			} else {
				b.WriteString(stLive.Render(string(r)))
			}
		}
	}
	return b.String()
}

// sMeterLegend is the S-scale legend, shown ONCE under the SIGNAL column header (never per
// row): plain dim digits so it reads at every terminal profile.
func sMeterLegend() string { return stDim.Render("1 3 5 7 9 +20") }
