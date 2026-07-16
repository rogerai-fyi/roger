package tui

// Increment 0 of the TUI design overhaul (radio-operator console): the palette +
// capability FOUNDATION. These tests lock the four lamp tokens, the one-switch
// full<->mono collapse, and the tint-band capability gate BEFORE anything renders
// through them - so the whole color layer is reversible to mono+red by a single
// flip (the founder's escape-hatch requirement), and a stray hue is caught here.
//
// Spec approved 2026-07-15 (tui-design-overhaul-brief.md, increment 0). No mocks:
// tokens are real lipgloss.AdaptiveColor, the gate reads the real `quiet`/profile.

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// B - the lamp tokens exist with the exact researched hues (full mode), and the
// brand red is the retinted redish-amber cLive (real ON-AIR neon, warmed).
func TestPaletteTokenHues(t *testing.T) {
	cases := []struct {
		name        string
		got         lipgloss.AdaptiveColor
		light, dark string
	}{
		{"cLive", cLive, "#C8391A", "#FF5636"},
		{"cSignal", cSignal, "#43801F", "#84C255"},
		{"cDialGlow", cDialGlow, "#92640F", "#F5A623"},
		{"cDial", cDial, "#42608C", "#7EA6D8"},
	}
	for _, c := range cases {
		if c.got.Light != c.light || c.got.Dark != c.dark {
			t.Errorf("%s = {Light:%q Dark:%q}, want {Light:%q Dark:%q}",
				c.name, c.got.Light, c.got.Dark, c.light, c.dark)
		}
	}
}

// B5 - the brand red warmed: everything that read cRed now reads the cLive
// redish-amber (cRed is the same token), so the on-air beacon / selection glint /
// verified mark pick up the warm red-orange without touching their call sites.
func TestBrandRedIsCLive(t *testing.T) {
	if cRed != cLive {
		t.Fatalf("cRed %v must equal the retinted cLive %v (single warm red, triple duty)", cRed, cLive)
	}
	if cLive.Light != "#C8391A" || cLive.Dark != "#FF5636" {
		t.Fatalf("brand red not warmed to redish-amber: cLive = %v", cLive)
	}
}

// C - the one switch: lamp(role) resolves each role to its own hue in `full`, and
// collapses to the ink ramp + the one red in `mono`. This is the founder's
// reversibility contract - a single global flip repoints every lamp.
func TestLampResolvesPerMode(t *testing.T) {
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })

	cases := []struct {
		role       paletteRole
		full, mono lipgloss.AdaptiveColor
	}{
		{roleLive, cLive, cLive},        // the one red survives the collapse
		{roleSignal, cSignal, cBody},    // green -> ink
		{roleDialGlow, cDialGlow, cDim}, // amber -> dim (warming = dim)
		{roleDial, cDial, cBody},        // blue -> ink (no focus hue in mono)
	}

	paletteMono = false
	for _, c := range cases {
		if got := lamp(c.role); got != c.full {
			t.Errorf("full: lamp(%v) = %v, want %v", c.role, got, c.full)
		}
	}
	paletteMono = true
	for _, c := range cases {
		if got := lamp(c.role); got != c.mono {
			t.Errorf("mono: lamp(%v) = %v, want %v", c.role, got, c.mono)
		}
	}
}

// C5 - in mono, NONE of the green/amber/blue hues may leak into any resolved role;
// only the ink ramp + the one warm red survive. Guards against a half-collapse.
func TestMonoCollapseHasNoLampHues(t *testing.T) {
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })
	paletteMono = true

	banned := map[string]bool{
		"#43801F": true, "#84C255": true, // signal green
		"#92640F": true, "#F5A623": true, // dial glow amber
		"#42608C": true, "#7EA6D8": true, // dial blue
	}
	for _, r := range []paletteRole{roleLive, roleSignal, roleDialGlow, roleDial} {
		c := lamp(r)
		if banned[c.Light] || banned[c.Dark] {
			t.Errorf("mono: lamp(%v) leaked a lamp hue: %v", r, c)
		}
	}
}

// D - the tint-band capability gate: Background() tint bands only at ANSI256+; at
// ANSI(16)/Ascii they degrade to a jarring block, so we fall back to the `▌` bar.
func TestCanTintByProfile(t *testing.T) {
	restore := quiet
	t.Cleanup(func() { quiet = restore })
	quiet = false

	cases := []struct {
		profile termenv.Profile
		want    bool
	}{
		{termenv.Ascii, false},
		{termenv.ANSI, false},
		{termenv.ANSI256, true},
		{termenv.TrueColor, true},
	}
	for _, c := range cases {
		if got := canTint(c.profile); got != c.want {
			t.Errorf("canTint(%v) = %v, want %v", c.profile, got, c.want)
		}
	}
}

// D5 - under quiet (NO_COLOR / non-TTY) tint bands are OFF at every profile: color
// is already stripped and a bg fill would be noise; the glyph bar carries it.
func TestCanTintOffWhenQuiet(t *testing.T) {
	restore := quiet
	t.Cleanup(func() { quiet = restore })
	quiet = true
	for _, p := range []termenv.Profile{termenv.Ascii, termenv.ANSI, termenv.ANSI256, termenv.TrueColor} {
		if canTint(p) {
			t.Errorf("quiet: canTint(%v) must be false", p)
		}
	}
}

// SetPalette is the cross-package seam cmd/rogerai uses to point the collapse from
// the loaded config/env; "mono" collapses, anything else (incl. "full"/"") is full.
func TestSetPalette(t *testing.T) {
	restore := paletteMono
	t.Cleanup(func() { paletteMono = restore })

	SetPalette("mono")
	if !paletteMono {
		t.Error(`SetPalette("mono") should set mono`)
	}
	SetPalette("full")
	if paletteMono {
		t.Error(`SetPalette("full") should set full`)
	}
	SetPalette("")
	if paletteMono {
		t.Error(`SetPalette("") should default to full`)
	}
}
