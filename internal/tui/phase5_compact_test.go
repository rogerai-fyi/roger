package tui

import (
	"strings"
	"testing"
)

func onAirBand(name string, sig int) band {
	o := &offer{Model: name, Online: true, Signal: sig}
	return band{model: name, online: true, stations: 1, cheapest: o, all: []offer{*o}, free: true}
}

// TestCompactWindowshadeOnAirOnly: the compact windowshade lists ONLY on-air bands with signal
// bars; offline bands are dropped (the at-a-glance "what's live" deck).
func TestCompactWindowshadeOnAirOnly(t *testing.T) {
	m := seedFor(100, modeBrowse, true) // compact
	m.connected = nil
	m.scanned = true
	m.bands = []band{onAirBand("alpha", 80), {model: "zoffline", online: false}, onAirBand("beta", 40)}
	m.clampBrowse()
	out := stripANSI(m.browseView(100))
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("windowshade should list on-air bands:\n%s", out)
	}
	if strings.Contains(out, "zoffline") {
		t.Errorf("windowshade must DROP offline bands:\n%s", out)
	}
	if !strings.ContainsAny(out, string(spectrumBlocks)) {
		t.Errorf("windowshade should show per-band signal bars:\n%s", out)
	}
}

// TestCompactWindowshadeEmpty: compact with bands present but NONE on air shows a clear note.
func TestCompactWindowshadeEmpty(t *testing.T) {
	m := seedFor(100, modeBrowse, true)
	m.scanned = true
	m.bands = []band{{model: "x", online: false}}
	m.clampBrowse()
	if out := stripANSI(m.browseView(100)); !strings.Contains(out, "no stations on air") {
		t.Errorf("windowshade with no on-air bands should say so:\n%s", out)
	}
}

// TestCompactHeaderShowsCounts: the compact header (browse) shows the clear N-on-air / M-bands
// count (replacing the old abstract EQ pane).
func TestCompactHeaderShowsCounts(t *testing.T) {
	m := seedFor(100, modeBrowse, true)
	m.connected = nil
	m.scanned = true
	m.offers = []offer{{Model: "a", Online: true}, {Model: "b", Online: true}}
	m.bands = []band{onAirBand("a", 50), onAirBand("b", 30), {model: "c", online: false}}
	if out := stripANSI(m.compactHeader(100)); !strings.Contains(out, "2 on air") || !strings.Contains(out, "3 bands") {
		t.Errorf("compact header should show 'N on air · M bands', got:\n%s", out)
	}
}
