package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWorldShootingStarKeepsInvariants exercises the shooting-star branch (pingworld.go's
// LAYER 6) - which no other test reaches - and pins the bb92df3 fix: even on a frame with a
// streak in flight, the ONE-RED invariant holds and there is still EXACTLY one on-air ◉
// (the star is painted last, so a streak can never clobber it). validate-agent finding #1.
func TestWorldShootingStarKeepsInvariants(t *testing.T) {
	found := 0
	for seed := 0; seed < 60 && found < 5; seed++ {
		for f := 0; f < 400; f++ {
			buf := worldBuffer(90, 28, f, seed)
			streak := false
			for _, row := range buf {
				for _, c := range row {
					if c.r == '╲' { // the shooting-star streak head
						streak = true
					}
				}
			}
			if !streak {
				continue
			}
			found++
			// On a streak frame, re-assert both world laws.
			stars := 0
			eyes := 0
			for _, row := range buf {
				for _, c := range row {
					if c.eye && c.r != '•' && c.r != '◉' {
						t.Fatalf("seed %d frame %d: streak made a non-Ping/non-star cell red: %q", seed, f, string(c.r))
					}
					if c.r == '◉' {
						stars++
					}
					if c.eye && c.r == '•' {
						eyes++
					}
				}
			}
			if stars != 1 {
				t.Errorf("seed %d frame %d: shooting star left %d on-air ◉, want exactly 1", seed, f, stars)
			}
			if eyes == 0 {
				t.Errorf("seed %d frame %d: no red Ping eye while a streak is in flight", seed, f)
			}
		}
	}
	if found == 0 {
		t.Fatal("never triggered the shooting-star branch - the regression guard exercised nothing")
	}
}

// TestPingWorldBlursAndRefocusesChat pins validate-agent finding #4: entering the screensaver
// from a CHANNEL blurs the chat input (no live-but-frozen cursor behind the world), and waking
// re-focuses it so the cursor blink resumes (Focus() re-arms the textinput.Blink Cmd-chain that
// dies while the screensaver owns the tick).
func TestPingWorldBlursAndRefocusesChat(t *testing.T) {
	m := pwModel(modeChat)
	m.chatIn.Focus()
	if !m.chatIn.Focused() {
		t.Fatal("precondition: chat input should start focused in a channel")
	}
	// Enter the world (z works in browse; from a channel it's /ping - both call enterPingWorld).
	out, _ := m.enterPingWorld()
	saver := asModel(out)
	if saver.mode != modePingWorld {
		t.Fatalf("expected modePingWorld, got %v", saver.mode)
	}
	if saver.chatIn.Focused() {
		t.Error("entering the screensaver should BLUR the chat input (no frozen cursor behind the world)")
	}
	// Any key wakes back to the channel and must re-focus + re-arm the blink.
	woke, cmd := saver.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	wm := asModel(woke)
	if wm.mode != modeChat {
		t.Fatalf("wake should return to modeChat, got %v", wm.mode)
	}
	if !wm.chatIn.Focused() {
		t.Error("waking to a channel should RE-FOCUS the chat input (resume the cursor blink)")
	}
	if cmd == nil {
		t.Error("wake to chat should return a cmd (tick batched with the re-armed blink)")
	}
}

// ---------------------------------------------------------------------------
// Ping World v2 — P0-2: depth-weighted 3-tier starfield (genuine parallax)
// ---------------------------------------------------------------------------

// TestStarTierFarHeavy: stars bucket into far/mid/near, weighted FAR-heavy so the sky reads
// as depth (most stars distant) rather than a flat even speckle - and all three tiers appear.
func TestStarTierFarHeavy(t *testing.T) {
	var far, mid, near int
	for i := 1; i < 6000; i++ {
		switch starTier(i, 3) {
		case 0:
			far++
		case 1:
			mid++
		case 2:
			near++
		default:
			t.Fatalf("starTier returned an out-of-range tier for i=%d", i)
		}
	}
	if !(far > mid && far > near) {
		t.Errorf("starfield should be far-heavy (depth): far=%d mid=%d near=%d", far, mid, near)
	}
	if mid == 0 || near == 0 {
		t.Errorf("all three depth tiers must appear: far=%d mid=%d near=%d", far, mid, near)
	}
}

// TestStarColumnParallax: the depth illusion - far stars are STATIC, near stars drift FASTER
// than mid across the same frame window, and every tier stays in-bounds for all frames.
func TestStarColumnParallax(t *testing.T) {
	w, x0 := 100, 50
	if starColumn(x0, 0, w, 0) != starColumn(x0, 9999, w, 0) {
		t.Error("far stars must be static (no parallax drift)")
	}
	dNear := (starColumn(x0, 0, w, 2) - starColumn(x0, 240, w, 2) + w) % w
	dMid := (starColumn(x0, 0, w, 1) - starColumn(x0, 240, w, 1) + w) % w
	if !(dNear > dMid) {
		t.Errorf("near stars must parallax faster than mid: near moved %d, mid moved %d", dNear, dMid)
	}
	for _, tier := range []int{0, 1, 2} {
		for f := 0; f < 600; f += 7 {
			if c := starColumn(x0, f, w, tier); c < 0 || c >= w {
				t.Fatalf("starColumn out of bounds: tier %d frame %d -> %d (w=%d)", tier, f, c, w)
			}
		}
	}
}

// TestStarfieldBrightNearStars: the buffer carries bright (near) stars, they're drawn ONLY
// from the near-glyph set, and a bright cell is NEVER red - depth brightness must not violate
// the ONE-RED law (bright = brighter ink, not a second red).
func TestStarfieldBrightNearStars(t *testing.T) {
	nearSet := map[rune]bool{}
	for _, r := range starsNear {
		nearSet[r] = true
	}
	bright := 0
	for f := 0; f < 120; f += 6 {
		for _, row := range worldBuffer(120, 40, f, 11) {
			for _, c := range row {
				if !c.bright {
					continue
				}
				bright++
				if c.eye {
					t.Errorf("frame %d: a bright star cell is also red (eye) - violates ONE-RED", f)
				}
				if !nearSet[c.r] {
					t.Errorf("frame %d: bright cell %q is not a near-star glyph", f, string(c.r))
				}
			}
		}
	}
	if bright == 0 {
		t.Error("expected some bright near-tier stars in the sky")
	}
}

// ---------------------------------------------------------------------------
// Ping World v2 — P0-4 moon + P0-3 day/night
// ---------------------------------------------------------------------------

// TestMoonPosUpperSkySlowDrift: the moon hangs HIGH and drifts slowly (a calm arc), always
// in-bounds.
func TestMoonPosUpperSkySlowDrift(t *testing.T) {
	w, sky := 100, 18
	x0, y0 := moonPos(w, sky, 0, 3)
	if x0 < 0 || x0 >= w || y0 < 0 || y0 >= sky {
		t.Fatalf("moon out of bounds: (%d,%d) for %dx%d", x0, y0, w, sky)
	}
	if y0 > sky/2 {
		t.Errorf("moon should hang in the UPPER sky, got y=%d (sky %d)", y0, sky)
	}
	if x1, _ := moonPos(w, sky, 1, 3); x1 != x0 {
		t.Error("moon should drift slowly (no move frame-to-frame)")
	}
	moved := false
	for f := 1; f < 600; f++ {
		if x, _ := moonPos(w, sky, f, 3); x != x0 {
			moved = true
			break
		}
	}
	if !moved {
		t.Error("moon should drift over time")
	}
}

// TestWorldShowsMoonNeverRed: the moon's disc appears in the upper sky and is NEVER red.
func TestWorldShowsMoonNeverRed(t *testing.T) {
	buf := worldBuffer(100, 22, 0, 4)
	upper := len(buf) / 3 // clearly above the horizon/Ping
	found := false
	for y := 0; y < upper; y++ {
		for _, c := range buf[y] {
			if c.r == '(' || c.r == ')' { // disc walls - up here, only the moon
				found = true
				if c.eye {
					t.Error("a moon cell must never be red (eye)")
				}
			}
		}
	}
	if !found {
		t.Error("expected the moon disc in the upper sky")
	}
}

// TestDayNightThinsSky: the slow day/night cycle thins faint stars by day; frame 0 (night) has
// a fuller sky than mid-cycle (day), and darkness stays in 0..100.
func TestDayNightThinsSky(t *testing.T) {
	if dayNightDarkness(0) <= dayNightDarkness(dayNightPeriod/2) {
		t.Error("frame 0 should be darker (night) than mid-cycle (day)")
	}
	for f := 0; f < 4000; f += 137 {
		if d := dayNightDarkness(f); d < 0 || d > 100 {
			t.Fatalf("darkness out of range at f=%d: %d", f, d)
		}
	}
	night := skyCellCount(worldBuffer(120, 24, 10, 7)) // deep night, no shooting star (10%40>=6)
	day := skyCellCount(worldBuffer(120, 24, 810, 7))  // ~midday, no shooting star (810%40>=6)
	if !(night > day) {
		t.Errorf("the sky should thin out by day: night=%d day=%d", night, day)
	}
}

func skyCellCount(buf [][]worldCell) int {
	n := 0
	for y := 0; y < len(buf)-5; y++ { // rows clearly above the horizon
		for _, c := range buf[y] {
			if c.r != ' ' {
				n++
			}
		}
	}
	return n
}

// TestPingWorldQuietSeam mirrors TestPingWalkSeam for the screensaver: the quiet (non-TTY)
// branch prints a static postcard and returns nil WITHOUT touching the program seam; the
// animated branch routes a pingWorldModel through runProgram with alt-screen and propagates
// the program error. validate-agent finding #5 (PingWorld 0% -> covered).
func TestPingWorldQuietSeam(t *testing.T) {
	origQuiet := quiet
	defer func() { quiet = origQuiet }()

	quiet = true
	called := false
	restore := withStubRunProgram(nil, func(tea.Model, []tea.ProgramOption) { called = true })
	if err := PingWorld(); err != nil {
		t.Fatalf("quiet PingWorld should return nil, got %v", err)
	}
	if called {
		t.Error("quiet PingWorld must NOT launch a program")
	}
	restore()

	quiet = false
	var launched tea.Model
	var opts []tea.ProgramOption
	sentinel := errMsgSentinel("world-exit")
	restore = withStubRunProgram(sentinel, func(m tea.Model, o []tea.ProgramOption) { launched = m; opts = o })
	defer restore()
	if err := PingWorld(); err != sentinel {
		t.Fatalf("animated PingWorld should propagate the program error, got %v", err)
	}
	if _, ok := launched.(pingWorldModel); !ok {
		t.Errorf("PingWorld should launch a pingWorldModel, got %T", launched)
	}
	if len(opts) != 1 {
		t.Errorf("PingWorld should pass exactly the alt-screen option, got %d", len(opts))
	}
}

// TestWorldFoldsUnderASCII: on a legacy console (ROGERAI_ASCII=1) the screensaver renders
// only ASCII - the signature non-ASCII glyphs are folded to stand-ins (◉->@, ✦->*, ░->.).
func TestWorldFoldsUnderASCII(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	out := stripANSI(renderWorld(90, 22, 0, 7))
	for _, bad := range []rune{'◉', '✦', '✧', '░', '▒', '▓', '•'} {
		if strings.ContainsRune(out, bad) {
			t.Errorf("ASCII mode still rendered the non-ASCII glyph %q", string(bad))
		}
	}
	if !strings.ContainsRune(out, '@') { // the on-air ◉ folds to @
		t.Error("expected the folded on-air star '@' under ASCII mode")
	}
}

// TestWorldPondReflectsUnderShore: the pond is additive - the ROGER·AI shore band is preserved,
// the bottom rows carry water ripples + a moon reflection, and no pond cell is ever red.
func TestWorldPondReflectsUnderShore(t *testing.T) {
	h := 20
	buf := worldBuffer(100, h, 12, 7)
	band := pondRowStr(buf[h-3]) // the surface/shore band row (horizon+1)
	if !strings.ContainsAny(band, "▓▒░R") {
		t.Errorf("the ROGER·AI shore band must be preserved (pond is additive); got %q", band)
	}
	water := false
	for y := h - 2; y < h; y++ {
		for _, c := range buf[y] {
			if c.r == '~' || c.r == '(' || c.r == ')' {
				water = true
			}
			if c.eye {
				t.Errorf("a pond/reflection cell must never be red (row %d)", y)
			}
		}
	}
	if !water {
		t.Error("expected pond ripples / a moon reflection below the shore")
	}
}

func pondRowStr(row []worldCell) string {
	rs := make([]rune, len(row))
	for i, c := range row {
		rs[i] = c.r
	}
	return string(rs)
}

// TestWorldHasDucklingTrail: a trail of dim follower ducklings lags behind Ping (P1-4), and the
// lead duckling still carries the red '•' guarantee.
func TestWorldHasDucklingTrail(t *testing.T) {
	// pick a frame where Ping has roamed right so the followers are on-screen.
	var buf [][]worldCell
	for f := 0; f < 400; f++ {
		b := worldBuffer(100, 22, f, 7)
		if worldPingX(f, 7, 100-pingWalkW) >= 16 {
			buf = b
			break
		}
	}
	if buf == nil {
		t.Fatal("no frame with Ping roamed right enough to show followers")
	}
	hz := len(buf) - 4 // horizon row
	dimDuck, redLead := 0, 0
	for _, c := range buf[hz] {
		if c.r == '·' && !c.eye {
			dimDuck++ // a follower duckling's dim body dot
		}
		if c.r == '•' && c.eye {
			redLead++ // the lead duckling (or Ping) red eye
		}
	}
	if dimDuck < 1 {
		t.Errorf("expected dim follower duckling(s) on the horizon row, got %d", dimDuck)
	}
	if redLead < 1 {
		t.Error("expected a red lead duckling/Ping eye on the horizon row")
	}
}

// TestWorldAuroraAtNightNeverRed: a dim aurora wisp appears at deep night (not midday) and is
// never red.
func TestWorldAuroraAtNightNeverRed(t *testing.T) {
	isAurora := func(r rune) bool { return r == '≈' || r == '∼' || r == '∽' || r == '≋' }
	night := worldBuffer(100, 24, 0, 7) // frame 0 = deep night (darkness 100)
	got := 0
	for _, row := range night {
		for _, c := range row {
			if isAurora(c.r) {
				got++
				if c.eye {
					t.Error("aurora cell must never be red")
				}
			}
		}
	}
	if got == 0 {
		t.Error("expected an aurora wisp at deep night")
	}
	day := worldBuffer(100, 24, dayNightPeriod/2, 7) // midday: darkness ~0
	for _, row := range day {
		for _, c := range row {
			if isAurora(c.r) {
				t.Error("aurora should NOT show at midday")
			}
		}
	}
}
