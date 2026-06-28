package tui

import "testing"

// TestWorldActPeriodicAndVaried: the act schedule repeats every waCycle windows (so worldPingX's
// bounded cycle-sum is exact) and is a genuine MIX of acts (not one act forever).
func TestWorldActPeriodicAndVaried(t *testing.T) {
	seed := 5
	for wi := 0; wi < 200; wi++ {
		if worldActAt(wi, seed) != worldActAt(wi+waCycle, seed) {
			t.Fatalf("act schedule must be periodic in waCycle; window %d != %d", wi, wi+waCycle)
		}
	}
	seen := map[worldAct]bool{}
	for wi := 0; wi < waCycle; wi++ {
		seen[worldActAt(wi, seed)] = true
	}
	if len(seen) < 3 {
		t.Errorf("a cycle should mix several acts, saw only %d distinct", len(seen))
	}
}

// TestWorldPingXMovesAndHolds: Ping's column stays in-bounds, genuinely travels over a cycle
// (it's not stuck), AND has hold frames (a pause/look/transmit act where x doesn't change) -
// the difference between a mechanical slide and a life with pauses.
func TestWorldPingXMovesAndHolds(t *testing.T) {
	seed, span := 7, 70
	moved, held := false, false
	prev := worldPingX(0, seed, span)
	minX, maxX := prev, prev
	for f := 1; f < waCycle*waWindow; f++ {
		x := worldPingX(f, seed, span)
		if x < 0 || x >= span {
			t.Fatalf("worldPingX out of bounds at f=%d: %d (span %d)", f, x, span)
		}
		if x != prev {
			moved = true
		} else {
			held = true
		}
		if x < minX {
			minX = x
		}
		if x > maxX {
			maxX = x
		}
		prev = x
	}
	if !moved {
		t.Error("Ping should travel across a cycle (never moves = mechanical/stuck)")
	}
	if !held {
		t.Error("Ping should have hold frames (pause/look/transmit) - not a constant slide")
	}
	if maxX-minX < span/4 {
		t.Errorf("Ping should roam a decent stretch; range %d on span %d", maxX-minX, span)
	}
	if worldPingX(0, seed, 0) != 0 {
		t.Error("degenerate span must yield 0, not panic")
	}
}

// TestWorldPingPoseAlwaysRedEye: the MAIN Ping never closes its eye - every act returns the red
// '•'. This is what keeps the ONE-RED 'at least one red eye' law true no matter where the
// wandering Pings have drifted.
func TestWorldPingPoseAlwaysRedEye(t *testing.T) {
	for f := 0; f < waCycle*waWindow*2; f++ {
		lines, eye := worldPingPose(f, 9)
		if eye != '•' {
			t.Fatalf("frame %d: main Ping eye must stay the red '•', got %q", f, string(eye))
		}
		if len(lines) == 0 {
			t.Fatalf("frame %d: pose returned no sprite lines", f)
		}
	}
}
