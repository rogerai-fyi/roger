package tui

import (
	"strings"
	"testing"
)

// Three founder enhancements to the Ping World screensaver, pinned here:
//  1. MUCH bigger, properly ROUND moon + sun (sized to the sky, curve-outlined, NEVER red).
//  2. Ping's edge TRANSITION: he ping-pongs + signs off with a wave instead of teleporting back.
//  3. A subtle LIVE on-air TICKER on the satellite (scrolling band names) when there's data.

// ---------------------------------------------------------------------------
// 1) Big round moon + sun
// ---------------------------------------------------------------------------

// discWidth counts the painted (non-space) cells of a disc row.
func discWidth(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			n++
		}
	}
	return n
}

// TestCelestialRadiusScales: the disc radius grows with the sky (capped at 7 -> a 15-row disc),
// shrinks to fit a narrow width, and is 0 (a tiny fallback) on a degenerate sky.
func TestCelestialRadiusScales(t *testing.T) {
	if r := celestialRadius(28, 90); r != 7 {
		t.Errorf("a tall, wide sky should hit the ry=7 cap, got %d", r)
	}
	if big, small := celestialRadius(28, 90), celestialRadius(12, 90); !(big > small) {
		t.Errorf("a taller sky should give a bigger disc: tall=%d short=%d", big, small)
	}
	if wide, narrow := celestialRadius(28, 90), celestialRadius(28, 20); !(wide > narrow) {
		t.Errorf("a narrower width must shrink the disc to fit: wide=%d narrow=%d", wide, narrow)
	}
	for _, sz := range [][2]int{{2, 90}, {28, 6}} { // too short / too narrow -> no real disc
		if r := celestialRadius(sz[0], sz[1]); r != 0 {
			t.Errorf("degenerate sky %v should give radius 0, got %d", sz, r)
		}
	}
}

// TestSunDiscRoundBrightWithRays: the day sun is a big bright gold disc (▓/▒ fill, curved
// outline), reads ROUND (a middle disc row is wider than a pole row), shimmers rays into its
// margin over frames, is deterministic, and uses ONLY its outline/fill/ray glyph set.
func TestSunDiscRoundBrightWithRays(t *testing.T) {
	const ry = 5
	g := sunDisc(ry, 1)
	if h, wantH := len(g), 2*ry+1+2*sunPad; h != wantH {
		t.Fatalf("sun height %d, want %d", h, wantH)
	}
	if w, wantW := len([]rune(g[0])), 2*(2*ry)+1+2*sunPad; w != wantW {
		t.Errorf("sun width %d, want %d", w, wantW)
	}
	// round: the disc's vertical-middle row (cy = ry+sunPad) is wider than a near-pole disc row.
	cy := ry + sunPad
	if !(discWidth(g[cy]) > discWidth(g[cy-ry+1])) {
		t.Errorf("sun should read round: mid=%d near-pole=%d", discWidth(g[cy]), discWidth(g[cy-ry+1]))
	}
	// bright gold fill present.
	all := strings.Join(g, "\n")
	if !strings.ContainsRune(all, '▓') || !strings.ContainsRune(all, '▒') {
		t.Error("sun disc should have a bright ▓/▒ gold fill")
	}
	// rays shimmer: across a few frames at least one ray glyph appears in the margin.
	rays := false
	for f := 0; f < 8; f++ {
		if strings.ContainsAny(strings.Join(sunDisc(ry, f), ""), "\\/|-") {
			rays = true
		}
	}
	if !rays {
		t.Error("sun should ring itself with shimmering rays (\\ / | -)")
	}
	// determinism.
	for i, ln := range sunDisc(ry, 3) {
		if ln != sunDisc(ry, 3)[i] {
			t.Fatal("sunDisc must be deterministic per (ry,frame)")
		}
	}
	// glyph set: outline + gold fill + rays + spaces only. Never red (it's tinted, not eye-marked).
	ok := map[rune]bool{' ': true, '(': true, ')': true, '◜': true, '◝': true, '◟': true, '◞': true,
		'▔': true, '▁': true, '▓': true, '▒': true, '\\': true, '/': true, '|': true, '-': true}
	for _, line := range sunDisc(ry, 5) {
		for _, r := range line {
			if !ok[r] {
				t.Errorf("sun glyph %q is outside the outline/fill/ray set", string(r))
			}
		}
	}
}

// TestBigCelestialBodiesInScene: a normal night shows a BIG round teal moon and a normal noon a
// BIG round gold sun — both far larger than the old 5x8 / 3x3 sprites — and neither is ever red.
func TestBigCelestialBodiesInScene(t *testing.T) {
	// night: a wide run of teal moon cells high in the sky, none red.
	moon := 0
	for _, row := range worldBufferData(100, 30, dnNight, 5, nil) {
		for _, c := range row {
			if c.tone == toneEarth {
				moon++
				if c.eye {
					t.Error("a moon cell must never be red")
				}
			}
		}
	}
	if moon < 60 { // the old 5x8 globe could never paint this many — proves it's BIG
		t.Errorf("night moon too small: only %d teal cells (want a big round disc)", moon)
	}
	// noon: a big gold sun, none red.
	sun := 0
	for _, row := range worldBufferData(100, 30, dnNoon, 5, nil) {
		for _, c := range row {
			if c.tone == toneSun {
				sun++
				if c.eye {
					t.Error("a sun cell must never be red")
				}
			}
		}
	}
	if sun < 60 {
		t.Errorf("noon sun too small: only %d gold cells (want a big round disc)", sun)
	}
}

// TestCelestialTinyFallback: on a degenerate sky (radius 0) the moon/sun still appear as a tiny
// fallback (toneEarth at night, toneSun at noon) — graceful scaling, no panic, no red.
func TestCelestialTinyFallback(t *testing.T) {
	if celestialRadius(3, 40) != 0 {
		t.Fatal("precondition: 40x7 should be a radius-0 (tiny-fallback) sky")
	}
	if !dnTones(worldBufferData(40, 7, dnNight, 5, nil))[toneEarth] {
		t.Error("tiny night sky should still show a fallback teal moon")
	}
	if !dnTones(worldBufferData(40, 7, dnNoon, 5, nil))[toneSun] {
		t.Error("tiny day sky should still show a fallback gold sun")
	}
}

// TestDiscHalfWidthShape: the pure circle math — widest at the equator (dy=0), zero at the poles,
// empty (-1) beyond, symmetric top/bottom, and monotonically narrowing toward each pole.
func TestDiscHalfWidthShape(t *testing.T) {
	ry, rx := 6, 12
	if discHalfWidth(0, ry, rx) != rx {
		t.Errorf("equator half-width %d, want rx=%d", discHalfWidth(0, ry, rx), rx)
	}
	if discHalfWidth(ry, ry, rx) != 0 || discHalfWidth(-ry, ry, rx) != 0 {
		t.Error("the poles should have zero half-width")
	}
	if discHalfWidth(ry+1, ry, rx) != -1 {
		t.Error("beyond the pole should be empty (-1)")
	}
	prev := discHalfWidth(0, ry, rx)
	for dy := 1; dy <= ry; dy++ {
		w := discHalfWidth(dy, ry, rx)
		if w > prev || discHalfWidth(dy, ry, rx) != discHalfWidth(-dy, ry, rx) {
			t.Errorf("disc must narrow symmetrically toward the pole at dy=%d (w=%d prev=%d)", dy, w, prev)
		}
		prev = w
	}
}

// ---------------------------------------------------------------------------
// 2) Ping's edge transition (ping-pong + sign-off wave, no teleport)
// ---------------------------------------------------------------------------

// TestPingNoTeleportPingPong is the core fix: Ping ambles edge-to-edge and TURNS, never snapping
// from one edge back to the other. Across a long run his column never jumps by more than his top
// speed (a run = 3) frame-to-frame (the old wrap jumped ~span at the edge), he visits BOTH ends,
// and his facing dir flips both ways.
func TestPingNoTeleportPingPong(t *testing.T) {
	const seed, span = 7, 60
	maxStep := worldActSpeed(waRun) // 3: the fastest he can move in one frame
	prev, _, _, _ := worldPingMotion(0, seed, span)
	low, high := prev, prev
	leftFace, rightFace := false, false
	for f := 1; f < 4000; f++ {
		x, dir, _, _ := worldPingMotion(f, seed, span)
		if d := absI(x - prev); d > maxStep {
			t.Fatalf("frame %d: Ping teleported %d cells (%d->%d) — must turn, not snap", f, d, prev, x)
		}
		if x < 0 || x > span {
			t.Fatalf("frame %d: Ping out of [0,span]: %d", f, x)
		}
		if dir > 0 {
			rightFace = true
		} else {
			leftFace = true
		}
		if x < low {
			low = x
		}
		if x > high {
			high = x
		}
		prev = x
	}
	if !leftFace || !rightFace {
		t.Errorf("Ping should walk BOTH ways over time: left=%v right=%v", leftFace, rightFace)
	}
	if low > span/8 || high < span-span/8 {
		t.Errorf("Ping should reach both edges: low=%d high=%d (span %d)", low, high, span)
	}
}

// TestPingSignsOffAtEdges: near each edge Ping enters a brief TURNAROUND beat (the "73, signing
// off → tuning back in" wave), and is NOT in that beat while ambling mid-screen. The wave frame
// cycles. Pure + seeded.
func TestPingSignsOffAtEdges(t *testing.T) {
	const seed, span = 7, 60
	turnedAtEdge, calmMid := false, false
	beats := map[int]bool{}
	for f := 0; f < 4000; f++ {
		x, _, turning, beat := worldPingMotion(f, seed, span)
		if turning {
			beats[beat] = true
			if x > span/4 && x < 3*span/4 {
				t.Fatalf("frame %d: Ping signed off mid-screen at x=%d (should only turn at the edges)", f, x)
			}
			if x <= span/8 || x >= span-span/8 {
				turnedAtEdge = true
			}
		} else if x > span/4 && x < 3*span/4 {
			calmMid = true
		}
	}
	if !turnedAtEdge {
		t.Error("Ping never played the sign-off turnaround at an edge")
	}
	if !calmMid {
		t.Error("Ping should amble (not sign off) across mid-screen")
	}
	if len(beats) < len(pingWaveFrames) {
		t.Errorf("the sign-off wave should animate through all %d frames; saw %d", len(pingWaveFrames), len(beats))
	}
}

// TestPingTurnRendersWaveInScene: at a turnaround frame the rendered Ping shows a wave-arm sprite
// (pingWaveFrames' '/' or '\'), keeps his red '•' eye, and the world stays one-red.
func TestPingTurnRendersWaveInScene(t *testing.T) {
	const seed, w, h = 7, 80, 24
	span := maxI(1, w-pingWalkW)
	tf := -1
	for f := 0; f < 4000; f++ {
		if _, _, turning, _ := worldPingMotion(f, seed, span); turning {
			tf = f
			break
		}
	}
	if tf < 0 {
		t.Fatal("never found a Ping turnaround frame")
	}
	buf := worldBuffer(w, h, tf, seed)
	px, _, _, beat := worldPingMotion(tf, seed, span)
	// the wave sprite for this beat carries an arm glyph ('/' or '\'); find it on Ping's top row.
	arm := strings.ContainsAny(pingWaveFrames[beat].lines[0], "/\\")
	if !arm {
		t.Skip("this wave beat has no arm glyph (acceptable) — the no-teleport law is covered elsewhere")
	}
	topRow := buf[h-4-len(pingWaveFrames[beat].lines)+1]
	seenArm, redEye := false, 0
	for x := maxI(0, px-1); x < minClampI(px+pingWalkW+1, w); x++ {
		if topRow[x].r == '/' || topRow[x].r == '\\' {
			seenArm = true
		}
	}
	for _, row := range buf {
		for _, c := range row {
			if c.eye {
				if c.r != '•' && c.r != '◉' {
					t.Fatalf("turnaround frame %d broke one-red: %q", tf, string(c.r))
				}
				if c.r == '•' {
					redEye++
				}
			}
		}
	}
	if !seenArm {
		t.Error("Ping's turnaround should render the sign-off wave arm in the scene")
	}
	if redEye == 0 {
		t.Error("Ping must keep his red • eye through the sign-off wave")
	}
}

func minClampI(v, hi int) int {
	if v > hi {
		return hi
	}
	return v
}

// TestDucklingsTrailBehindBothDirections: the duckling trail follows BEHIND Ping's travel — on the
// rightward leg they're to his left, on the return leg to his right — staying on-screen with the
// lead duckling's red '•' backstop intact.
func TestDucklingsTrailBehindBothDirections(t *testing.T) {
	const seed, w, h = 7, 100, 24
	span := maxI(1, w-pingWalkW)
	checkedRight, checkedLeft := false, false
	for f := 0; f < 4000 && !(checkedRight && checkedLeft); f++ {
		px, dir, turning, _ := worldPingMotion(f, seed, span)
		if turning || px < 20 || px > span-20 {
			continue // want him clearly mid-screen so both sides have room
		}
		buf := worldBuffer(w, h, f, seed)
		horizon := h - 4
		leftDucks, rightDucks := 0, 0
		for _, row := range []int{horizon - 1, horizon} {
			for x := 0; x < w; x++ {
				if buf[row][x].r == '·' && !buf[row][x].eye {
					if x < px {
						leftDucks++
					} else if x > px+pingWalkW {
						rightDucks++
					}
				}
			}
		}
		if dir > 0 && !checkedRight {
			if leftDucks == 0 {
				t.Errorf("frame %d (going right): ducklings should trail to Ping's LEFT", f)
			}
			checkedRight = true
		}
		if dir < 0 && !checkedLeft {
			if rightDucks == 0 {
				t.Errorf("frame %d (going left): ducklings should trail to Ping's RIGHT", f)
			}
			checkedLeft = true
		}
	}
	if !checkedRight || !checkedLeft {
		t.Skipf("did not observe both legs cleanly mid-screen (right=%v left=%v)", checkedRight, checkedLeft)
	}
}

// ---------------------------------------------------------------------------
// 3) Live on-air ticker on the satellite
// ---------------------------------------------------------------------------

// TestMarqueeWindowScrolls: the pure marquee helper returns a fixed-width window that wraps around
// and scrolls as start advances.
func TestMarqueeWindowScrolls(t *testing.T) {
	if marqueeWindow("", 0, 5) != "" || marqueeWindow("abc", 0, 0) != "" {
		t.Error("empty text or non-positive width must yield empty")
	}
	if w := marqueeWindow("abcdef", 0, 4); w != "abcd" {
		t.Errorf("window(0,4)=%q, want abcd", w)
	}
	if w := marqueeWindow("abc", 2, 5); w != "cabca" { // wraps around
		t.Errorf("window should wrap: got %q, want cabca", w)
	}
	if marqueeWindow("abcdef", 0, 4) == marqueeWindow("abcdef", 2, 4) {
		t.Error("advancing start should scroll the window")
	}
}

// TestShortModelAndTickerText: model ids are trimmed to compact tags; the ticker tape joins the
// on-air bands with · and loops; nil/empty data -> no tape.
func TestShortModelAndTickerText(t *testing.T) {
	if shortModel("gpt") != "gpt" {
		t.Error("short ids pass through")
	}
	long := shortModel("llama-3.1-8b-instruct")
	if len([]rune(long)) > 12 || !strings.HasSuffix(long, "…") {
		t.Errorf("a long id should be trimmed with an ellipsis, got %q", long)
	}
	if tickerText(nil) != "" || tickerText(&worldData{}) != "" {
		t.Error("no live data -> empty ticker tape")
	}
	d := &worldData{stations: []worldStation{{model: "a"}, {model: "b"}}}
	if tape := tickerText(d); !strings.Contains(tape, "a") || !strings.Contains(tape, "b") || !strings.Contains(tape, "·") {
		t.Errorf("ticker tape should list the bands joined by ·, got %q", tape)
	}
}

// TestSatelliteCarriesLiveTicker: with live data the satellite downlinks a subtle scrolling on-air
// ticker — a red '•' on-air pip + aqua (toneSat) band-name text — and the world keeps EXACTLY one
// ◉ with no other non-•/◉ red. The names scroll over time. Seeded (d==nil) shows no ticker.
func TestSatelliteCarriesLiveTicker(t *testing.T) {
	d := &worldData{stations: []worldStation{
		{model: "gpt-oss-20b", signal: 90, inFlight: 2},
		{model: "llama-3.1-8b", signal: 55},
		{model: "qwen-coder", signal: 30, inFlight: 1},
	}}
	letters, pip, scroll := false, false, map[string]bool{}
	for f := 0; f < 210; f++ {
		buf := worldBufferData(120, 26, f, 7, d)
		stars := 0
		var line strings.Builder
		for _, row := range buf {
			for _, c := range row {
				if c.eye {
					if c.r != '•' && c.r != '◉' {
						t.Fatalf("frame %d: live ticker broke one-red: %q", f, string(c.r))
					}
					if c.r == '◉' {
						stars++
					}
				}
				// a toneSat letter is a ticker character (the aqua band names).
				if c.tone == toneSat {
					if (c.r >= 'a' && c.r <= 'z') || (c.r >= '0' && c.r <= '9') {
						letters = true
						line.WriteRune(c.r)
					}
				}
				if c.r == '•' && c.eye {
					pip = true
				}
			}
		}
		if stars != 1 {
			t.Errorf("frame %d: live world must keep exactly one ◉, got %d", f, stars)
		}
		if s := line.String(); s != "" {
			scroll[s] = true
		}
	}
	if !letters {
		t.Error("the live satellite never showed any scrolling band-name letters (aqua ticker)")
	}
	if !pip {
		t.Error("the live ticker should carry a red '•' on-air pip")
	}
	if len(scroll) < 2 {
		t.Errorf("the ticker text should scroll over time; saw %d distinct readouts", len(scroll))
	}
}

// TestLiveTickerStripsUnderNoColor: the aqua ticker adds color with live data but strips to plain
// under NO_COLOR (the screensaver's NO_COLOR guarantee holds for the new live layer too).
func TestLiveTickerStripsUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	d := &worldData{stations: []worldStation{{model: "gpt-oss-20b", signal: 80, inFlight: 1}}}
	for f := 0; f < 80; f += 7 {
		if out := renderWorldData(110, 26, f, 7, d); strings.Contains(out, "\x1b[") {
			t.Errorf("frame %d: live ticker emitted ANSI under NO_COLOR", f)
		}
	}
}

// TestSeededSatelliteUnchanged: with no live data the satellite keeps its original seeded behavior
// (a periodic red '•' downlink, no ticker letters) — the nil path is byte-identical (also pinned
// by TestRenderWorldDataNilIdentity), so the offline screensaver is untouched.
func TestSeededSatelliteUnchanged(t *testing.T) {
	for f := 0; f < 300; f++ {
		for _, row := range worldBuffer(90, 24, f, 7) {
			for _, c := range row {
				if c.tone == toneSat && ((c.r >= 'a' && c.r <= 'z') || (c.r >= '0' && c.r <= '9')) {
					t.Fatalf("frame %d: the seeded (offline) satellite must NOT show ticker letters", f)
				}
			}
		}
	}
}
