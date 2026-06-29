package tui

import (
	"strings"
	"testing"
)

// The screensaver got MORE DYNAMIC + GENERATIVE (founder: "make it feel more alive"): the wandering
// Ping now crosses edge-to-edge instead of vanishing mid-screen, clouds drift the day sky, the
// offline/seeded world grows generative signal towers that breathe, and bird-flock size / butterfly
// count / meteor bursts vary per seeded window. These pin that behavior - and re-pin the world laws
// (one red, exact width, NO_COLOR-plain, pure+seeded) across the new elements.

// ---------------------------------------------------------------------------
// Ask 1 - the wanderer always ENTERS one edge and EXITS the other (never pops mid-screen)
// ---------------------------------------------------------------------------

func wandererPeriod(w int) int { return (w + wandererW + 1) * wandererStride }

func wandererOffscreen(wx, w int) bool { return wx+wandererW <= 0 || wx >= w }

// firstPresentCycle finds a traversal cycle where lane is crossing.
func firstPresentCycle(t *testing.T, seed, w, horizon, lane int) int {
	t.Helper()
	period := wandererPeriod(w)
	for c := 0; c < 80; c++ {
		if d, _, _, _ := wandererAt(c*period+period/2, seed, w, horizon, lane); d {
			return c
		}
	}
	t.Fatalf("no present traversal found for lane %d (seed %d)", lane, seed)
	return -1
}

// TestWandererTraversalEntersExitsOffscreen is the core fix: a present traversal STARTS and ENDS
// fully off-screen, crosses the middle, and is on-screen as ONE contiguous run - so the wanderer
// can never pop in or vanish out mid-width at a window boundary (the old frame/80-window bug).
func TestWandererTraversalEntersExitsOffscreen(t *testing.T) {
	seed, w, horizon := 7, 80, 20
	period := wandererPeriod(w)
	cyc := firstPresentCycle(t, seed, w, horizon, 0)

	if d, _, wx, _ := wandererAt(cyc*period, seed, w, horizon, 0); !d || !wandererOffscreen(wx, w) {
		t.Fatalf("traversal must START off-screen: draw=%v wx=%d", d, wx)
	}
	if d, _, wx, _ := wandererAt(cyc*period+period-1, seed, w, horizon, 0); !d || !wandererOffscreen(wx, w) {
		t.Fatalf("traversal must END off-screen: draw=%v wx=%d", d, wx)
	}

	mid, onRuns, prevOn := false, 0, false
	for f := cyc * period; f < (cyc+1)*period; f++ {
		_, _, wx, _ := wandererAt(f, seed, w, horizon, 0)
		on := !wandererOffscreen(wx, w)
		if on && wx > w/4 && wx < 3*w/4 {
			mid = true
		}
		if on && !prevOn {
			onRuns++ // a fresh on-screen run started
		}
		prevOn = on
	}
	if !mid {
		t.Error("wanderer never crossed mid-screen during a present traversal")
	}
	if onRuns != 1 {
		t.Errorf("wanderer must enter+exit exactly ONCE per traversal (no mid-screen pop); got %d runs", onRuns)
	}
}

// TestWandererNoMidScreenPopAtBoundaries scans many frames across cycle boundaries: whenever the
// wanderer transitions from drawn-and-on-screen to not-drawn-or-off-screen (or vice-versa), it must
// be at an EDGE, never mid-width - the precise guarantee the old code violated.
func TestWandererNoMidScreenPopAtBoundaries(t *testing.T) {
	seed, w, horizon := 5, 64, 18
	prevOn, prevWx := false, 0
	for f := 0; f < wandererPeriod(w)*12; f++ {
		d, _, wx, _ := wandererAt(f, seed, w, horizon, 0)
		on := d && !wandererOffscreen(wx, w)
		if on != prevOn { // a visibility flip: the toggling-on/off position must be near an edge
			edge := wx
			if !on {
				edge = prevWx // the last on-screen position before it left
			}
			if edge > wandererW && edge < w-2*wandererW {
				t.Fatalf("frame %d: wanderer popped/vanished mid-screen at wx=%d (w=%d)", f, edge, w)
			}
		}
		prevOn, prevWx = on, wx
	}
}

// TestWandererWalkAnimatesDirectionAndPair: the wanderer ambles (its sprite alternates between two
// walk frames), its entry direction VARIES run-to-run, and a 2nd wanderer (lane 1) occasionally
// joins - all seeded.
func TestWandererWalkAnimatesDirectionAndPair(t *testing.T) {
	seed, w, horizon := 7, 80, 20
	period := wandererPeriod(w)

	// walk animation: across a present traversal the returned sprite takes >=2 distinct frames.
	cyc := firstPresentCycle(t, seed, w, horizon, 0)
	seen := map[string]bool{}
	for f := cyc * period; f < cyc*period+18; f++ {
		if _, lines, _, _ := wandererAt(f, seed, w, horizon, 0); lines != nil {
			seen[strings.Join(lines, "|")] = true
		}
	}
	if len(seen) < 2 {
		t.Errorf("wanderer should animate its walk; saw %d distinct frames", len(seen))
	}

	// direction varies + a 2nd wanderer occasionally appears (scan many seeds/cycles).
	leftEntry, rightEntry, pairs := false, false, 0
	for _, sd := range []int{1, 2, 3, 5, 7, 11} {
		for c := 0; c < 40; c++ {
			if d, _, a, _ := wandererAt(c*period+period/2, sd, w, horizon, 0); d {
				_, _, b, _ := wandererAt(c*period+period/2+wandererStride, sd, w, horizon, 0)
				if b > a {
					leftEntry = true // moving right => entered from the left edge
				} else if b < a {
					rightEntry = true // moving left => entered from the right edge
				}
			}
			if d, _, _, _ := wandererAt(c*period+period/2, sd, w, horizon, 1); d {
				pairs++
			}
		}
	}
	if !leftEntry || !rightEntry {
		t.Errorf("wanderer entry direction should vary: leftEntry=%v rightEntry=%v", leftEntry, rightEntry)
	}
	if pairs == 0 {
		t.Error("a 2nd wanderer (lane 1) should occasionally appear")
	}
}

// TestWandererPairOppositeDirections: when both lanes cross in the same traversal they amble OPPOSITE
// ways (so the pair pass each other rather than overlapping the whole time).
func TestWandererPairOppositeDirections(t *testing.T) {
	w, horizon := 80, 20
	period := wandererPeriod(w)
	checked := false
	for _, seed := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
		for c := 0; c < 40; c++ {
			f := c*period + period/2
			d0, _, a0, _ := wandererAt(f, seed, w, horizon, 0)
			d1, _, a1, _ := wandererAt(f, seed, w, horizon, 1)
			if !d0 || !d1 {
				continue
			}
			_, _, b0, _ := wandererAt(f+wandererStride, seed, w, horizon, 0)
			_, _, b1, _ := wandererAt(f+wandererStride, seed, w, horizon, 1)
			if (b0-a0)*(b1-a1) > 0 {
				t.Errorf("seed %d cyc %d: paired wanderers move the SAME way (d0=%d d1=%d)", seed, c, b0-a0, b1-a1)
			}
			checked = true
		}
	}
	if !checked {
		t.Skip("no overlapping pair found to check (acceptable - rare)")
	}
}

// TestWandererAtDegenerate: a non-positive width or negative frame yields no wanderer (no panic).
func TestWandererAtDegenerate(t *testing.T) {
	if d, _, _, _ := wandererAt(10, 7, 0, 20, 0); d {
		t.Error("w<=0 should yield no wanderer")
	}
	if d, _, _, _ := wandererAt(-1, 7, 80, 20, 0); d {
		t.Error("negative frame should yield no wanderer")
	}
}

// TestWorldRendersWandererSilhouette: the lane loop actually paints a wanderer into the buffer at a
// present, on-screen traversal frame (the integration, not just the helper).
func TestWorldRendersWandererSilhouette(t *testing.T) {
	seed, w, h := 7, 80, 24
	horizon := h - 4
	period := wandererPeriod(w)
	cyc := firstPresentCycle(t, seed, w, horizon, 0)
	// a frame where the wanderer sits clearly on-screen
	for f := cyc * period; f < (cyc+1)*period; f++ {
		d, _, wx, wy := wandererAt(f, seed, w, horizon, 0)
		if !d || wx < 10 || wx > w-10 {
			continue
		}
		buf := worldBuffer(w, h, f, seed)
		// the wanderer's feet row (wy+2) should carry a foot glyph at/under the sprite columns
		row := buf[wy+2]
		feet := 0
		for x := wx; x < wx+wandererW && x < w; x++ {
			if x >= 0 && (row[x].r == '╿' || row[x].r == '╽') {
				feet++
			}
		}
		if feet >= 1 {
			return // rendered
		}
	}
	t.Error("the wanderer silhouette never rendered into the buffer during a present traversal")
}

// ---------------------------------------------------------------------------
// Ask 2 - drifting day clouds (pale, parallax, gone at night, never red)
// ---------------------------------------------------------------------------

// TestDayHasDriftingClouds: clouds (tonePale) appear in the DAY sky, are gone at night, and are
// never red.
func TestDayHasDriftingClouds(t *testing.T) {
	day := false
	for f := dayNightPeriod/4 + 10; f < 3*dayNightPeriod/4-10; f += 13 {
		for _, row := range worldBufferData(120, 32, f, 5, nil) {
			for _, c := range row {
				if c.tone == tonePale {
					day = true
					if c.eye {
						t.Error("a cloud cell must never be red")
					}
				}
			}
		}
	}
	if !day {
		t.Error("no drifting clouds (tonePale) appeared by day")
	}
	for _, row := range worldBufferData(120, 32, dnNight, 5, nil) {
		for _, c := range row {
			if c.tone == tonePale {
				t.Error("clouds should NOT show at night")
			}
		}
	}
}

// TestCloudsDrift: clouds move across the sky over time (the "drifting" requirement).
func TestCloudsDrift(t *testing.T) {
	cols := func(frame int) string {
		buf := make([][]worldCell, 16)
		for y := range buf {
			buf[y] = make([]worldCell, 120)
			for x := range buf[y] {
				buf[y][x] = worldCell{r: ' '}
			}
		}
		paintClouds(buf, 120, 16, frame, 5)
		var b strings.Builder
		for _, row := range buf {
			for x, c := range row {
				if c.tone == tonePale {
					b.WriteByte(byte('0' + x%10))
				}
			}
		}
		return b.String()
	}
	if cols(0) == cols(300) {
		t.Error("clouds should drift across the sky over time")
	}
}

// TestPaintCloudsGuards: a too-short sky or zero width is a no-op (no panic, no cells).
func TestPaintCloudsGuards(t *testing.T) {
	buf := [][]worldCell{make([]worldCell, 10), make([]worldCell, 10)}
	paintClouds(buf, 10, 1, 0, 5) // skyRows<2
	paintClouds(buf, 0, 8, 0, 5)  // w<=0
	for _, row := range buf {
		for _, c := range row {
			if c.tone == tonePale {
				t.Error("guarded paintClouds should paint nothing")
			}
		}
	}
}

// TestTonePaleStripsUnderNoColor: clouds add color by day but strip to plain under NO_COLOR.
func TestTonePaleStripsUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if out := renderWorld(100, 24, dnNoon, 5); strings.Contains(out, "\x1b[") {
		t.Error("day clouds emitted ANSI under NO_COLOR")
	}
}

// ---------------------------------------------------------------------------
// Ask 3 - generative seeded towers in the offline world (vary over time)
// ---------------------------------------------------------------------------

// TestSeededTowersVaryOverTime: the offline world grows generative towers whose flagship HEIGHT
// changes across frames (a breathing signal); too-small worlds get none (so the seeded ◉ keeps its
// sky position).
func TestSeededTowersVaryOverTime(t *testing.T) {
	heights := map[int]bool{}
	for f := 0; f < 3000; f += 7 {
		ts := seededTowers(100, 20, f, 7)
		if len(ts) == 0 {
			t.Fatalf("seeded world should grow towers at frame %d", f)
		}
		heights[20-ts[0].tipY] = true // flagship mast height
	}
	if len(heights) < 3 {
		t.Errorf("seeded flagship tower height should vary over time; saw %d distinct", len(heights))
	}
	if seededTowers(4, 2, 0, 7) != nil {
		t.Error("a too-small world should have no seeded towers (keeps the seeded sky ◉)")
	}
	if seededTowers(100, 2, 0, 7) != nil {
		t.Error("too-short horizon should have no seeded towers")
	}
}

// TestSeededWorldHasTowersAndStaysOneRed: the offline buffer carries dim signal-tower masts AND
// keeps exactly one red ◉ with no other red cell (the new offline towers are one-red-safe).
func TestSeededWorldHasTowersAndStaysOneRed(t *testing.T) {
	masts := 0
	for f := 0; f < 300; f += 7 {
		buf := worldBuffer(100, 24, f, 7)
		stars := 0
		for _, row := range buf {
			for _, c := range row {
				if c.r == '│' {
					masts++
				}
				if c.r == '◉' {
					stars++
					if !c.eye {
						t.Errorf("frame %d: offline ◉ must be red (eye)", f)
					}
				}
				if c.eye && c.r != '•' && c.r != '◉' {
					t.Fatalf("frame %d: offline tower made a non-•/◉ cell red: %q", f, string(c.r))
				}
			}
		}
		if stars != 1 {
			t.Errorf("frame %d: offline world must keep exactly one ◉, got %d", f, stars)
		}
	}
	if masts == 0 {
		t.Error("offline/seeded world should grow signal-tower masts")
	}
}

// TestSeededTowersDoNotTouchLivePath: with LIVE data the offline generator is bypassed entirely -
// the live tower count is governed by the snapshot, not seededTowers.
func TestSeededTowersDoNotTouchLivePath(t *testing.T) {
	d := &worldData{stations: []worldStation{{model: "only", signal: 70, inFlight: 1}}}
	tips := map[int]bool{}
	for f := 0; f < 200; f += 5 {
		live := worldTowers(100, 20, d)
		if len(live) != 1 {
			t.Fatalf("frame %d: live data must yield exactly its 1 tower, got %d", f, len(live))
		}
		tips[live[0].tipY] = true
	}
	if len(tips) != 1 {
		t.Errorf("a live tower's height must NOT vary frame-to-frame (only seeded towers breathe); got %d heights", len(tips))
	}
}

// TestTriWave: a 0..100 triangle over 0..199, peaking at 100, wrapping cleanly for any input.
func TestTriWave(t *testing.T) {
	if triWave(0) != 0 || triWave(100) != 100 || triWave(199) != 1 {
		t.Errorf("triWave shape wrong: 0=%d 100=%d 199=%d", triWave(0), triWave(100), triWave(199))
	}
	for p := -500; p < 500; p++ {
		if v := triWave(p); v < 0 || v > 100 {
			t.Fatalf("triWave(%d)=%d out of 0..100", p, v)
		}
	}
}

// ---------------------------------------------------------------------------
// Ask 4 - generative variety (flock size, butterfly count, meteor bursts)
// ---------------------------------------------------------------------------

// TestGenerativeVariety: flock size, butterfly count, and meteor-burst count are all seeded and
// VARY across windows - including the rare "special moments" (a big migration / a meteor shower).
func TestGenerativeVariety(t *testing.T) {
	seed := 7

	small, big := false, false
	for win := 0; win < 800; win++ {
		n := flockSize(win, seed)
		if n < 2 || n > 8 {
			t.Fatalf("flockSize(%d)=%d out of 2..8", win, n)
		}
		if n <= 5 {
			small = true
		} else {
			big = true
		}
	}
	if !small || !big {
		t.Errorf("flock size should vary incl a rare big migration: small=%v big=%v", small, big)
	}

	one, two := false, false
	for win := 0; win < 300; win++ {
		switch butterflyCount(win, seed) {
		case 1:
			one = true
		case 2:
			two = true
		default:
			t.Fatalf("butterflyCount(%d) out of 1..2", win)
		}
	}
	if !one || !two {
		t.Errorf("butterfly count should vary: one=%v pair=%v", one, two)
	}

	single, shower := false, false
	for win := 0; win < 800; win++ {
		n := meteorCount(win, seed)
		if n < 1 || n > 3 {
			t.Fatalf("meteorCount(%d)=%d out of 1..3", win, n)
		}
		if n == 1 {
			single = true
		} else {
			shower = true
		}
	}
	if !single || !shower {
		t.Errorf("meteor count should vary incl a rare shower: single=%v shower=%v", single, shower)
	}
}

// TestMeteorShowerMultipleStreaks: on a rare meteor-shower window the night buffer shows >=2 streaks
// at once, and STILL keeps exactly one ◉ + a red Ping eye (the burst is one-red-safe).
func TestMeteorShowerMultipleStreaks(t *testing.T) {
	seed := 7
	for f := 0; f < 6000; f++ {
		if dayNightDarkness(f) < 50 { // night only
			continue
		}
		win := f / 40
		if worldHash(win, 7, seed)%4 != 0 || meteorCount(win, seed) < 2 {
			continue
		}
		buf := worldBuffer(120, 30, f, seed)
		streaks, stars, eyes := 0, 0, 0
		for _, row := range buf {
			for _, c := range row {
				switch {
				case c.r == '╲':
					streaks++
				case c.r == '◉':
					stars++
				case c.eye && c.r == '•':
					eyes++
				}
			}
		}
		if streaks >= 2 {
			if stars != 1 {
				t.Errorf("shower frame %d: want exactly one ◉, got %d", f, stars)
			}
			if eyes == 0 {
				t.Errorf("shower frame %d: no red Ping eye during the shower", f)
			}
			return
		}
	}
	t.Error("never observed a multi-streak meteor shower in the buffer")
}

// TestDynamicWorldStillBoundedAndPlain: with all the new elements live, the day scene still emits
// rows EXACTLY w wide and no ANSI under NO_COLOR (the structural laws hold for the new layers too).
func TestDynamicWorldStillBoundedAndPlain(t *testing.T) {
	w := 90
	for _, f := range []int{dnNoon, dnNoon + 37, dayNightPeriod/4 + 5, dnNight + 13} {
		for i, line := range strings.Split(renderWorld(w, 26, f, 7), "\n") {
			if vis := len([]rune(stripANSI(line))); vis != w {
				t.Errorf("frame %d row %d width %d, want exactly %d", f, i, vis, w)
			}
		}
	}
	t.Setenv("NO_COLOR", "1")
	for _, f := range []int{dnNoon, dnNight, dayNightPeriod / 4} {
		if strings.Contains(renderWorld(w, 26, f, 7), "\x1b[") {
			t.Errorf("frame %d emitted ANSI under NO_COLOR", f)
		}
	}
}
