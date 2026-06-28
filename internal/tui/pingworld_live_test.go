package tui

import (
	"strconv"
	"testing"
)

// TestRenderWorldDataNilIdentity: the live-data seam is safe - nil data is byte-identical to the
// pure seeded world (so every existing test + the offline standalone path are unchanged).
func TestRenderWorldDataNilIdentity(t *testing.T) {
	for _, f := range []int{0, 17, 100} {
		if renderWorldData(80, 24, f, 7, nil) != renderWorld(80, 24, f, 7) {
			t.Errorf("renderWorldData(...,nil) must equal renderWorld at frame %d", f)
		}
	}
}

// TestBuildWorldDataSortsAndCaps: on-air bands -> stations strongest-first, capped; no on-air -> nil.
func TestBuildWorldDataSortsAndCaps(t *testing.T) {
	var bands []band
	for i := 0; i < 12; i++ {
		bands = append(bands, onAirBand("m"+strconv.Itoa(i), i*8))
	}
	bands = append(bands, band{model: "off", online: false})
	d := buildWorldData(bands)
	if d == nil || len(d.stations) != 8 {
		t.Fatalf("want 8 capped stations, got %v", d)
	}
	if d.stations[0].signal < d.stations[1].signal {
		t.Error("stations must be strongest-signal first")
	}
	if buildWorldData([]band{{model: "x", online: false}}) != nil {
		t.Error("no on-air bands should yield nil (the seeded world)")
	}
}

// TestWorldTowersLiveInvariants: with live data the world grows signal-tower masts, KEEPS exactly
// one red ◉, and never reds a non-•/◉ cell (the live layer is one-red-safe).
func TestWorldTowersLiveInvariants(t *testing.T) {
	d := &worldData{stations: []worldStation{
		{model: "strong", signal: 90, inFlight: 2}, {model: "mid", signal: 50}, {model: "weak", signal: 10},
	}}
	for f := 0; f < 120; f += 6 {
		stars, masts := 0, 0
		for _, row := range worldBufferData(100, 24, f, 7, d) {
			for _, c := range row {
				if c.eye && c.r != '•' && c.r != '◉' {
					t.Fatalf("frame %d: a live cell is red but not •/◉: %q", f, string(c.r))
				}
				if c.r == '◉' {
					stars++
				}
				if c.r == '│' {
					masts++
				}
			}
		}
		if stars != 1 {
			t.Errorf("frame %d: want exactly one on-air ◉, got %d", f, stars)
		}
		if masts == 0 {
			t.Errorf("frame %d: expected signal-tower masts", f)
		}
	}
}

// TestWorldTowersFlagshipTallest: the strongest band is the first (flagship) tower and is taller.
func TestWorldTowersFlagshipTallest(t *testing.T) {
	d := &worldData{stations: []worldStation{{model: "s", signal: 90}, {model: "w", signal: 10}}}
	towers := worldTowers(100, 20, d)
	if len(towers) != 2 {
		t.Fatalf("want 2 towers, got %d", len(towers))
	}
	if towers[0].tipY >= towers[1].tipY { // smaller tipY = taller
		t.Errorf("strongest tower should be taller (tipY %d should be < %d)", towers[0].tipY, towers[1].tipY)
	}
}
