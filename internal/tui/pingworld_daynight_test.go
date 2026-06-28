package tui

import (
	"strings"
	"testing"
)

// The screensaver now lives a full DAY (founder: "make it go from night time showing the moon to
// day time showing the sun and some plants growing ... add more animations ... maybe introduce one
// more"): night shows the teal moon/globe + stars + aurora; day shows a gold SUN arcing, green
// PLANTS growing from the ground, a BIRD flock crossing, and a BUTTERFLY (the new character)
// fluttering by the plants — and Ping naps at deep night. RED stays the only HOT color throughout.

// The two extremes of the dayNightDarkness triangle wave: frame 0 == deep night (darkness 100),
// frame period/2 == high noon (darkness 0).
const (
	dnNight = 0
	dnNoon  = dayNightPeriod / 2
)

func dnTones(buf [][]worldCell) map[worldTone]bool {
	m := map[worldTone]bool{}
	for _, row := range buf {
		for _, c := range row {
			m[c.tone] = true
		}
	}
	return m
}

func dnRunes(buf [][]worldCell) map[rune]bool {
	m := map[rune]bool{}
	for _, row := range buf {
		for _, c := range row {
			m[c.r] = true
		}
	}
	return m
}

// TestWorldOneRedAcrossDayNight pins the signature law over the WHOLE cycle (the original
// invariant test only scanned the night frames 0..160). At every phase — sunrise, noon, dusk,
// midnight — the ONLY red cells are Ping eyes • or the on-air ◉, and ≥1 red eye is always present.
func TestWorldOneRedAcrossDayNight(t *testing.T) {
	for _, sz := range [][2]int{{100, 30}, {60, 20}, {44, 16}} {
		for f := 0; f < 2*dayNightPeriod; f += 41 {
			buf := worldBufferData(sz[0], sz[1], f, 5, nil)
			eyes := 0
			for _, row := range buf {
				for _, c := range row {
					if c.eye {
						if c.r != '•' && c.r != '◉' {
							t.Fatalf("frame %d size %v: red cell %q is neither • nor ◉", f, sz, string(c.r))
						}
						if c.r == '•' {
							eyes++
						}
					}
				}
			}
			if eyes == 0 {
				t.Errorf("frame %d size %v: no red Ping eye on screen", f, sz)
			}
		}
	}
}

// TestCelestialSwap: noon shows the gold SUN and hides the teal night globe; deep night shows the
// teal globe and hides the sun.
func TestCelestialSwap(t *testing.T) {
	noon := dnTones(worldBufferData(120, 32, dnNoon, 5, nil))
	if !noon[toneSun] {
		t.Error("noon: no gold sun (toneSun)")
	}
	if noon[toneEarth] {
		t.Error("noon: the teal night globe should be gone")
	}
	night := dnTones(worldBufferData(120, 32, dnNight, 5, nil))
	if !night[toneEarth] {
		t.Error("night: no teal moon/globe (toneEarth)")
	}
	if night[toneSun] {
		t.Error("night: the sun should be gone")
	}
}

// TestPlantsGrowByDay: green plants (toneLeaf) grow from the ground at noon; the night ground is bare.
func TestPlantsGrowByDay(t *testing.T) {
	if !dnTones(worldBufferData(120, 32, dnNoon, 5, nil))[toneLeaf] {
		t.Error("noon: no plants (toneLeaf) grew from the ground")
	}
	if dnTones(worldBufferData(120, 32, dnNight, 5, nil))[toneLeaf] {
		t.Error("night: ground should be bare (no toneLeaf)")
	}
}

// TestPlantStageGrowsWithDay: the pure growth curve — dormant at night, tallest at noon, monotone in between.
func TestPlantStageGrowsWithDay(t *testing.T) {
	if plantStage(100) != 0 || plantStage(50) != 0 {
		t.Errorf("plants must be dormant at night: stage(100)=%d stage(50)=%d", plantStage(100), plantStage(50))
	}
	if plantStage(0) != plantMax {
		t.Errorf("plantStage(noon)=%d, want plantMax=%d", plantStage(0), plantMax)
	}
	if a, b := plantStage(40), plantStage(10); a >= b {
		t.Errorf("plants should grow as day brightens: stage(40)=%d should be < stage(10)=%d", a, b)
	}
}

// TestSunArc: the sun is up ONLY by day, sweeps left->right across the day, arcs high at noon /
// low at the edges, always in-bounds.
func TestSunArc(t *testing.T) {
	w, sky := 100, 20
	if up, _, _ := sunArc(w, sky, dnNight); up {
		t.Error("sun must not be up at deep night")
	}
	up, xNoon, yNoon := sunArc(w, sky, dnNoon)
	if !up {
		t.Fatal("sun must be up at noon")
	}
	if yNoon > 2 {
		t.Errorf("noon sun should ride high (y=%d)", yNoon)
	}
	_, xDawn, yDawn := sunArc(w, sky, dayNightPeriod/4+30)
	_, xDusk, _ := sunArc(w, sky, 3*dayNightPeriod/4-30)
	if !(xDawn < xNoon && xNoon < xDusk) {
		t.Errorf("sun should sweep left->right: dawn=%d noon=%d dusk=%d", xDawn, xNoon, xDusk)
	}
	if yDawn <= yNoon {
		t.Errorf("sun should sit lower at dawn (y=%d) than at noon (y=%d)", yDawn, yNoon)
	}
	for _, f := range []int{dnNoon, dayNightPeriod/4 + 1, 3*dayNightPeriod/4 - 1} {
		if up, x, y := sunArc(w, sky, f); up && (x < 0 || x >= w || y < 0 || y >= sky) {
			t.Errorf("frame %d: sun out of bounds (%d,%d)", f, x, y)
		}
	}
}

// TestDayHasBirdsAndButterfly: by day a bird flock (v/^) crosses and the butterfly (< >) flutters.
// Birds come+go on seeded windows so scan a span; the butterfly is always out by day.
func TestDayHasBirdsAndButterfly(t *testing.T) {
	birds, fly := false, false
	for f := dayNightPeriod/4 + 20; f < 3*dayNightPeriod/4-20; f += 7 {
		r := dnRunes(worldBufferData(120, 32, f, 5, nil))
		if r['v'] || r['^'] {
			birds = true
		}
		if r['<'] || r['>'] {
			fly = true
		}
	}
	if !birds {
		t.Error("no birds (v/^) ever crossed the daytime sky")
	}
	if !fly {
		t.Error("no butterfly (< >) fluttered during the day")
	}
}

// TestPingNapsAtDeepNight: when Ping pauses at deep night, a soft Zzz drifts over his head — more
// life, still just the one red eye. (z/Z appear nowhere else in the world.)
func TestPingNapsAtDeepNight(t *testing.T) {
	const seed = 7
	for f := 0; f < 2*dayNightPeriod; f++ { // all deep-night pause frames across two cycles
		if dayNightDarkness(f) <= 80 || worldActAt(f/waWindow, seed) != waPause {
			continue
		}
		if r := dnRunes(worldBufferData(96, 26, f, seed, nil)); r['z'] || r['Z'] {
			return // napped
		}
	}
	t.Error("Ping never showed a Zzz while pausing at deep night")
}

// TestDayBoundedAndPlain: the day scene keeps rows exactly w wide and emits no ANSI under
// NO_COLOR — the same guarantees the night scene already meets.
func TestDayBoundedAndPlain(t *testing.T) {
	w := 80
	for i, line := range strings.Split(renderWorld(w, 24, dnNoon, 2), "\n") {
		if vis := len([]rune(stripANSI(line))); vis != w {
			t.Errorf("day row %d width %d, want exactly %d", i, vis, w)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if strings.Contains(renderWorld(w, 24, dnNoon, 2), "\x1b[") {
		t.Error("day scene emitted ANSI under NO_COLOR")
	}
}
