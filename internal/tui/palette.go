package tui

// palette.go - increment 0 of the radio-operator TUI overhaul: the LAMP palette +
// the one-switch full<->mono collapse + the tint-band capability gate. Nothing
// renders through these yet; later increments light the lamps. The whole point of
// isolating them here is REVERSIBILITY: `roger config set palette mono` (or a dumb
// terminal) must revert the entire color layer in one flip, so every semantic hue
// is reached through lamp()/a token the switch can remap - never a hard-coded hex
// at a call site. Escape-hatch requirement, founder ruling 2026-07-15.
//
// The lamps are the actual light sources of a mid-century radio room, contrast-
// validated against the repo's warm-black (#0E0D0B) / paper (#FBFBFA) grounds:
//   cLive     - ON-AIR neon red-orange: on-air + fault + brand (the one warm red).
//   cSignal   - magic-eye tube's willemite yellow-green: tune-lock / online / ok.
//   cDialGlow - the amber backlit dial glow: warming / caution / wash.
//   cDial     - fluorescent/CRT dial blue-white: focus / info / selection.

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var (
	// cLive is the brand red, warmed to a real ON-AIR neon's redish-amber. It IS
	// cRed (tui.go) - the same token - so retinting there warms every existing
	// glint without touching a call site. The one red that survives a mono collapse.
	cLive = cRed

	// The three new lamp hues. AdaptiveColor so light/dark flips with the terminal
	// background; lipgloss auto-downsamples hex->256->16 for colored text, so only
	// Background() tint bands need canTint() gating (see below), never these.
	cSignal   = lipgloss.AdaptiveColor{Light: "#43801F", Dark: "#84C255"} // magic-eye green
	cDialGlow = lipgloss.AdaptiveColor{Light: "#92640F", Dark: "#F5A623"} // amber dial glow
	cDial     = lipgloss.AdaptiveColor{Light: "#42608C", Dark: "#7EA6D8"} // dial blue-white
)

// paletteRole is a semantic lamp slot; lamp() maps it to a concrete color for the
// current palette mode. Call sites ask for a ROLE, never a hex - that indirection
// is what lets the one switch repoint the whole board.
type paletteRole int

const (
	roleLive     paletteRole = iota // on-air / fault / brand accent
	roleSignal                      // tune-lock / online / ok
	roleDialGlow                    // warming / caution / wash
	roleDial                        // focus / info / selection
)

// paletteMono, when true, collapses the lamp board to the mono ink ramp + the one
// warm red - the escape hatch. Seeded once at startup by SetPalette() from the
// loaded config/env (mirrors the `quiet` global), then read by lamp() everywhere.
var paletteMono bool

// SetPalette points the collapse from the resolved config/env mode: "mono"
// collapses; anything else ("full", "", junk) is the full lamp board. The
// cross-package seam cmd/rogerai calls at launch.
func SetPalette(mode string) { paletteMono = mode == "mono" }

// lamp resolves a semantic role to its color for the active palette mode. In full
// mode each role is its own lamp hue; in mono every lamp but the one red collapses
// into the ink ramp (green->ink, amber->dim, blue->ink), so color only ever means
// "something is energized" and mono+red is a single-flip revert.
func lamp(r paletteRole) lipgloss.AdaptiveColor {
	if paletteMono {
		switch r {
		case roleLive:
			return cLive // the one warm red survives the collapse
		case roleDialGlow:
			return cDim // warming reads as dim, not amber
		default:
			return cBody // signal + dial fold into ink
		}
	}
	switch r {
	case roleLive:
		return cLive
	case roleSignal:
		return cSignal
	case roleDialGlow:
		return cDialGlow
	default:
		return cDial
	}
}

// lampStyle is the render-side companion to lamp(): a foreground style in a role's
// lamp color for the active palette mode. Chips light through this, so a call site
// never names a hex and the one mono switch repoints them all (increment 1+ use it).
func lampStyle(r paletteRole) lipgloss.Style { return lipgloss.NewStyle().Foreground(lamp(r)) }

// canTint reports whether a Background() tint band may be painted at this terminal
// profile. Colored TEXT (lamps, chips, meters, prowords) degrades for free via
// lipgloss downsampling and needs no gate; a near-black truecolor BAND, however,
// becomes a jarring solid block at 16-color and is invisible at Ascii - so tint
// bands are ANSI256+ only, and OFF entirely under quiet (NO_COLOR / non-TTY),
// where the bare `▌` accent bar (a glyph, legible at every profile) carries it.
func canTint(p termenv.Profile) bool {
	if quiet {
		return false
	}
	return p == termenv.ANSI256 || p == termenv.TrueColor
}
