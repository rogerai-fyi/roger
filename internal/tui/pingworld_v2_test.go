package tui

import (
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
