package tui

import "testing"

// TestBandSignalMeterUsesBrokerSignal verifies the band-list fix: a freshly-on-air
// offer with NO traffic (tps==0) but a broker signal (43) meters NON-blank in the
// band row, instead of the blank tower the old tps-only path produced. Offline shows
// the flat "no signal" tower (correct). Driven through the SAME signalBarsRaw the
// band row calls.
func TestBandSignalMeterUsesBrokerSignal(t *testing.T) {
	flat := signalFlat()

	// Online, broker signal 43, zero tps -> non-blank (the regression case).
	raw := signalBarsRaw(0, 43, 0, true, 1)
	if raw == flat {
		t.Fatalf("online signal=43 tps=0 rendered the blank tower %q; an on-air band must meter", raw)
	}

	// Offline -> the flat no-signal tower regardless of any signal value.
	if got := signalBarsRaw(0, 43, 0, false, 1); got != flat {
		t.Errorf("offline band tower = %q, want the flat no-signal tower %q", got, flat)
	}

	// No broker signal but measured tps still meters (legacy fallback).
	if got := signalBarsRaw(0, 0, 120, true, 1); got == flat {
		t.Errorf("online tps=120 with no broker signal should meter, got blank")
	}

	// Online with neither signal nor tps -> a faint carrier, never fully blank.
	if got := signalBarsRaw(0, 0, 0, true, 1); got == flat {
		t.Errorf("online with no reading should show a faint carrier, got the blank tower")
	}
}

// TestBandSignalLevelMapping checks the TUI's 0..100 -> 0..7 ramp matches the CLI's:
// 0 is the no-signal sentinel, positive is always >= 1, ~43 lands mid-tower, 100 pins
// the top of the 8-glyph ramp.
func TestBandSignalLevelMapping(t *testing.T) {
	if signalLevel(0) != 0 {
		t.Errorf("signalLevel(0) = %d want 0", signalLevel(0))
	}
	if signalLevel(1) < 1 {
		t.Errorf("signalLevel(1) = %d want >= 1 (online never blank)", signalLevel(1))
	}
	if l := signalLevel(43); l < 3 || l > 5 {
		t.Errorf("signalLevel(43) = %d want mid-tower (~4)", l)
	}
	top := len(signalRamp()) - 1
	if l := signalLevel(100); l != top {
		t.Errorf("signalLevel(100) = %d want %d (top of ramp)", l, top)
	}
}

// TestGroupBandsCarriesSignal confirms the broker signal flows offer -> band: the
// cheapest station's Signal is what bandSignal (sort) and the meter read, so an
// on-air band with no traffic sorts and meters by its baseline signal, not 0.
func TestGroupBandsCarriesSignal(t *testing.T) {
	offers := []offer{
		{NodeID: "fresh", Model: "m", PriceIn: 0.2, PriceOut: 0.2, Online: true, TPS: 0, Signal: 43},
	}
	bands := groupBands(offers, nil)
	if len(bands) != 1 {
		t.Fatalf("groupBands -> %d bands want 1", len(bands))
	}
	bd := bands[0]
	if bd.cheapest == nil {
		t.Fatal("cheapest station not set on the on-air band")
	}
	if bd.cheapest.Signal != 43 {
		t.Errorf("cheapest.Signal = %d want 43 (broker signal carried)", bd.cheapest.Signal)
	}
	// The sort proxy uses the broker signal (not tps==0), so an on-air no-traffic band
	// is non-zero in the "strongest signal" ordering.
	if bandSignal(bd) != 43 {
		t.Errorf("bandSignal = %v want 43 (broker signal, not tps)", bandSignal(bd))
	}
}
