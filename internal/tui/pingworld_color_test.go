package tui

import (
	"strings"
	"testing"
)

// The screensaver got COLOR (founder: "is it possible to add more color somewhere?"): cool
// AMBIENT tones (sky / globe / aurora / water) paint the calm world, while RED stays the ONLY
// hot color — the on-air ◉ and Ping's eye • — so the live beacon pops MORE against the cool
// backdrop, not less. These pin that contract: tones are present, tones are NEVER the reserved
// red, eye cells never also carry a cool tone, and the whole thing strips to plain under NO_COLOR.

// TestWorldHasCoolTones: the seeded world actually paints cool tones (not just dim ink) — proof
// the color is live. Frame 0 is deep night (dayNightDarkness(0)=100) so the aurora shows too.
func TestWorldHasCoolTones(t *testing.T) {
	buf := worldBufferData(100, 30, 0, 7, nil)
	seen := map[worldTone]bool{}
	for _, row := range buf {
		for _, c := range row {
			if c.tone != toneNone {
				seen[c.tone] = true
			}
		}
	}
	for _, want := range []struct {
		tone worldTone
		name string
	}{
		{toneSky, "sky/stars (frost blue)"},
		{toneEarth, "globe (teal)"},
		{toneWater, "pond (blue)"},
	} {
		if !seen[want.tone] {
			t.Errorf("deep-night world has no %s cell — that color is not applied", want.name)
		}
	}
	// aurora shows only at deep night (darkness>70); frame 0 is darkness 100, so it MUST appear.
	if !seen[toneAurora] && !seen[toneAuroraV] {
		t.Error("deep-night world has no aurora tone (green/violet) — aurora color not applied")
	}
}

// TestToneStyleNeverRed: NO cool tone may resolve to the on-air RED. The one-red law lives at
// the PALETTE level, not just in the buffer's eye bit — red is the single hot accent, reserved.
func TestToneStyleNeverRed(t *testing.T) {
	for tn := toneNone; tn <= toneWater; tn++ {
		if got := toneStyle(tn, false).GetForeground(); got == cRed {
			t.Errorf("tone %d resolves to the reserved on-air RED — breaks one-red", tn)
		}
		if got := toneStyle(tn, true).GetForeground(); got == cRed {
			t.Errorf("tone %d (bright) resolves to the reserved on-air RED — breaks one-red", tn)
		}
	}
}

// TestBlitTEyeKeepsNoTone: an eye cell is RED-ONLY. Even if a caller passes a cool tone, blitT
// must leave an eye cell tone-free so the eye renders red (eye beats tone in the compositor, but
// we keep the buffer honest too).
func TestBlitTEyeKeepsNoTone(t *testing.T) {
	buf := [][]worldCell{make([]worldCell, 3)}
	blitT(buf, 0, 0, []string{"◉"}, '◉', toneEarth)
	c := buf[0][0]
	if !c.eye {
		t.Fatalf("eye rune ◉ should set eye=true, got %+v", c)
	}
	if c.tone != toneNone {
		t.Errorf("eye cell must NOT carry a cool tone (got tone %d) — the eye is red-only", c.tone)
	}
}

// TestBlitTPaintsTone: a non-eye cell painted via blitT carries the requested tone (so the
// ambient layers — globe, water — actually get their color).
func TestBlitTPaintsTone(t *testing.T) {
	buf := [][]worldCell{make([]worldCell, 4)}
	blitT(buf, 0, 0, []string{"(o)"}, 0, toneWater)
	for x, want := range []rune{'(', 'o', ')'} {
		if buf[0][x].r != want {
			t.Fatalf("cell %d rune %q, want %q", x, string(buf[0][x].r), string(want))
		}
		if buf[0][x].tone != toneWater {
			t.Errorf("cell %d tone %d, want toneWater(%d)", x, buf[0][x].tone, toneWater)
		}
	}
}

// TestWorldNoANSIUnderNoColor: with NO_COLOR the screensaver — tones and all — strips to plain.
// Mirrors the package's proven NO_COLOR pattern (redesign_test.go).
func TestWorldNoANSIUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for f := 0; f < 80; f += 13 {
		if out := renderWorld(80, 24, f, 7); strings.Contains(out, "\x1b[") {
			t.Errorf("frame %d emitted ANSI under NO_COLOR — cool tones must strip to plain", f)
		}
	}
}
