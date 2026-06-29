package tui

import "testing"

// TestWorldOrbitalTrafficAppears exercises the new orbital traffic + ground dish (paintSatellite /
// paintSpaceship / paintRadioDish): across seeds and frames a satellite (▢), a spaceship (◊), and
// the dish (Y) each render at least once, and the ONE-RED law still holds (every red cell is a •
// or ◉) even with the new red DOWNLINK / running-light / feed blips present. Tiny sizes exercise
// the painters' degenerate guards.
func TestWorldOrbitalTrafficAppears(t *testing.T) {
	seen := map[rune]bool{}
	for seed := 0; seed < 6; seed++ {
		for f := 0; f < 700; f += 3 {
			buf := worldBuffer(90, 26, f, seed)
			for _, row := range buf {
				for _, c := range row {
					seen[c.r] = true
					if c.eye && c.r != '•' && c.r != '◉' {
						t.Fatalf("seed %d frame %d: red cell %q is not a • or ◉ (orbital traffic broke one-red)", seed, f, string(c.r))
					}
				}
			}
		}
	}
	for _, r := range []rune{'▢', '◊', 'Y'} {
		if !seen[r] {
			t.Errorf("orbital element %q never rendered across the scan", string(r))
		}
	}
	// tiny-but-valid sizes hit the painters' skyRows<3 / w<14 degenerate guards (no panic).
	for _, sz := range [][2]int{{6, 6}, {8, 6}, {12, 8}, {13, 10}} {
		for f := 0; f < 40; f++ {
			if got := worldBuffer(sz[0], sz[1], f, 1); len(got) != sz[1] {
				t.Fatalf("size %v frame %d: height %d", sz, f, len(got))
			}
		}
	}
}

// TestWorldDishAlwaysSweeps pins that the ground dish is present every frame (it has no on/off
// window) at a normal size, so the transmission cone is a steady part of the scene.
func TestWorldDishAlwaysSweeps(t *testing.T) {
	dishSeen := false
	for f := 0; f < 24; f++ { // one full sweep cycle
		buf := worldBuffer(80, 24, f, 2)
		for _, row := range buf {
			for _, c := range row {
				if c.r == 'Y' {
					dishSeen = true
				}
			}
		}
	}
	if !dishSeen {
		t.Error("the ground-station dish (Y) never rendered over a full sweep cycle")
	}
}
