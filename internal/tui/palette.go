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
	"slices"
	"strings"

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
	// They are paletteSlots so the color-blind boards can retint them live.
	cSignal   = newSlot(boardFull.signal)   // magic-eye green
	cDialGlow = newSlot(boardFull.dialGlow) // amber dial glow
	cDial     = newSlot(boardFull.dial)     // dial blue-white

	// cBand is the FAINT neutral warm tint band behind a USER turn + the input (catalog
	// per §8.6) - a barely-there warm lift over the paper that marks "your line" as a zone
	// distinct from the assistant's bare-paper prose. A Background() only, gated by canTint
	// (ANSI256+) and full palette; mono / dumb terminals drop it to the bare red ▌ bar.
	cBand = lipgloss.AdaptiveColor{Light: "#F1EFE8", Dark: "#191712"}

	// THE DECK GROUND. The one surface the whole app sits on, painted rather than
	// inherited (founder: "i want the background a different color, lets make it more
	// roger like like a radio").
	//
	// A terminal hands you whatever ground the operator's theme picked - the founder's
	// is purple - and a radio faceplate that changes colour with the room is not a
	// faceplate. These are the site's own paper tokens, so the TUI, the browser console
	// and rogerai.fm stand on the same two grounds.
	//
	// AdaptiveColor, not a fixed dark: a light terminal gets the warm paper and a dark
	// one the warm black, so the ground is OURS without inverting anyone's polarity. On
	// a terminal already near either value this is close to a no-op, which is the right
	// outcome - it only asserts itself where the theme had wandered off.
	cDeck = lipgloss.AdaptiveColor{Light: "#FBFBFA", Dark: "#0E0D0B"}

	// THE SLATE. A raised faceplate for a sent question (askSlate): a face a shade
	// above the ground, a lit top edge, a fallen bottom edge, and the brightest ink on
	// the screen for the question itself. A terminal has no shadows, so depth is made
	// the way a radio faceplate makes it - light on the top lip, dark on the bottom -
	// and "glow" is contrast, the only glow a terminal has.
	//
	// Dark values are the real target (the console is a dark instrument); the light set
	// inverts the same relationship so the bevel still reads as raised on paper.
	cSlate      = lipgloss.AdaptiveColor{Light: "#ECEAE3", Dark: "#221F19"} // the face
	cSlateLit   = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#4A443A"} // top lip, catching light
	cSlateShade = lipgloss.AdaptiveColor{Light: "#C9C6BC", Dark: "#0A0908"} // bottom lip, falling away
	cSlateText  = lipgloss.AdaptiveColor{Light: "#0F0E0A", Dark: "#FFFDF6"} // the question, brightest ink

	// THE STATION'S FACE. The answer's block, quieter than the operator's: the same
	// shape so the pair reads as one exchange, a step darker so it reads as the other
	// side of it. Sitting between the deck (#0E0D0B) and the ask face (#221F19) puts
	// the two blocks either side of the ground, which is what separates them at a
	// glance without a second accent colour.
	cReply = lipgloss.AdaptiveColor{Light: "#F4F2EC", Dark: "#17150F"}

	// cLiveSurface is the solid but restrained red-wine plate behind a truthfully
	// broker-acknowledged ON AIR provider panel. The text still says ON AIR; color
	// improves hierarchy but never carries state alone.
	cLiveSurface = newSlot(boardFull.liveSurface)

	// cTubeGlow is the FAINT tube-glow WASH behind the brand lockup while a session is
	// live (catalog #10) - the dim end of cDialGlow, a Background() only. Full cDialGlow
	// would be a garish amber block; this is a barely-lit warm amber over the warm-black
	// (dark) / a warm cream (light). Painted ONLY through canTint (ANSI256+) and never in
	// mono, so it self-disables on the escape hatch and degrades cleanly on dumb terminals.
	cTubeGlow = lipgloss.AdaptiveColor{Light: "#F5EBD2", Dark: "#241B09"}
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

// deckGround gates the painted ground. On by default - it is the product's look - and
// one config flip (`roger config set deck off`) or the mono escape hatch turns it off,
// which is the same reversibility rule the lamp board follows: no visual layer may be
// unremovable. A forced light/dark theme is the one thing deck off does not undo: its
// text is drawn for its own page, so the page stays (see paintDeck). Mono removes both.
var deckGround = true

// SetDeck points the painted-ground switch from the resolved config/env.
func SetDeck(on bool) { deckGround = on }

// paintDeck lays the frame on the deck ground: every line padded to the full width so
// the ground reaches the edges, then carried through nested styles by solidBackground
// (nested foreground spans emit SGR resets, which would otherwise punch holes in it and
// leave a mottled screen).
//
// OFF under mono and at any profile that cannot tint, which is also every headless
// render - so tests, pipes and dumb terminals produce exactly the frame they always did.
//
// A forced light/dark theme (`/theme`, `roger config set theme`) paints even with the
// deck switched off: its text is drawn for that ground, so the terminal's own one must
// not show through. It also sets the ink, so plain text is dark on the light page and
// light on the dark one. Mono is the escape hatch and drops the forced theme entirely
// (themeForced), and a profile with no color at all skips it.
func paintDeck(frame string, width int) string {
	p := lipgloss.DefaultRenderer().ColorProfile()
	forced := themeForced()
	if forced {
		if quiet || p == termenv.Ascii {
			return frame
		}
	} else if !deckGround || paletteMono || !canTint(p) {
		return frame
	}
	lines := strings.Split(frame, "\n")
	for i, ln := range lines {
		if pad := width - lipgloss.Width(ln); pad > 0 {
			lines[i] = ln + strings.Repeat(" ", pad)
		}
	}
	ground := lipgloss.NewStyle().Background(cDeck)
	if forced {
		ground = ground.Foreground(cInk)
	}
	return solidPaint(strings.Join(lines, "\n"), ground)
}

// lamp resolves a semantic role to its color for the active palette mode. In full
// mode each role is its own lamp hue; in mono every lamp but the one red collapses
// into the ink ramp (green->ink, amber->dim, blue->ink), so color only ever means
// "something is energized" and mono+red is a single-flip revert.
func lamp(r paletteRole) lipgloss.TerminalColor {
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

// bandUser renders a USER line (an echoed ask or the input prompt) with the red ▌ left
// bar and, where canTint allows (ANSI256+, not quiet) and the palette is full, a FAINT
// neutral tint band behind it - so your turns separate from the assistant's bare-paper
// prose. On mono / a dumb terminal it drops to the bare red ▌ bar (the §9 fallback), so
// the escape hatch holds and the accent still reads at every profile.
func bandUser(text string) string {
	if !paletteMono && canTint(lipgloss.DefaultRenderer().ColorProfile()) {
		return lipgloss.NewStyle().Foreground(cLive).Background(cBand).Bold(true).Render("▌ ") +
			lipgloss.NewStyle().Background(cBand).Render(text)
	}
	return stSelBar.Render("▌ ") + text
}

func tintComposerLines(lines []string, width int) []string {
	if paletteMono || !canTint(lipgloss.DefaultRenderer().ColorProfile()) {
		return lines
	}
	style := lipgloss.NewStyle().Background(cBand).Width(max(1, width))
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = style.Render(line)
	}
	return out
}

// solidBackground carries a background through nested lipgloss spans. Nested
// foreground styles emit SGR resets, which otherwise punch holes in an outer
// Background style and leave a visually mottled card.
func solidBackground(block string, color lipgloss.TerminalColor) string {
	return solidPaint(block, lipgloss.NewStyle().Background(color))
}

// solidPaint opens every line on ground's SGR and re-applies it after every reset, so
// a nested span never punches a hole in it. With a foreground in ground, unstyled text
// takes that ink instead of the terminal's default.
func solidPaint(block string, ground lipgloss.Style) string {
	probe := ground.Render("X")
	i := strings.Index(probe, "X")
	if i <= 0 {
		return block
	}
	prefix := probe[:i]
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+prefix)
		lines[i] = prefix + line + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

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

// ── THE WAVE SPECTRUM ────────────────────────────────────────────────────────
// The seven Wave tiers, in ladder order, in the founder's own Spectrum hues -
// the SAME seven the website's animated wave mark, the mesh deck and the factory
// deck wear (web/src/styles/base.css --tier-*). Carried here so the terminal and
// the site speak one palette: the carrier sweep under a working turn is literally
// the Wave Spectrum sweeping past, tier by tier.
//
// AdaptiveColor per tier (light ground / dark ground) exactly as the stylesheet
// defines them, so a light terminal gets the darker, higher-contrast set rather
// than the dark theme's brighter hues washed out on paper.
//
// These are TEXT colors, so lipgloss downsamples them for free at lower profiles
// and no canTint() gate is needed. Under mono they collapse with everything else
// (see spectrumTier) - the escape hatch stays one switch.
var waveSpectrum = []lipgloss.AdaptiveColor{
	{Light: "#b23a2a", Dark: "#e6604f"}, // Pico  - the edge child
	{Light: "#c96a1c", Dark: "#e88b3c"}, // Nano  - the fleet gateway
	{Light: "#b0891a", Dark: "#d4aa2e"}, // Micro - the site brain
	{Light: "#2f8a52", Dark: "#48b873"}, // Giga  - the plant
	{Light: "#1f8f8f", Dark: "#39b7b7"}, // Tera  - cross-site enterprise
	{Light: "#2f63bf", Dark: "#5b8ee6"}, // Peta  - regional
	{Light: "#5b3fbf", Dark: "#8a6df0"}, // Exa   - the flagship
}

// waveTierNames are the ladder's names in the same order as waveSpectrum. Index
// alignment between these two is load-bearing (spectrum_test.go locks it).
var waveTierNames = []string{"Pico", "Nano", "Micro", "Giga", "Tera", "Peta", "Exa"}

// spectrumStyle returns the style for tier i (0=Pico .. 6=Exa), wrapping the index
// so a caller can walk a longer track without bounds-checking. Under the mono
// escape hatch every tier collapses to the one red - the Spectrum is decoration
// over an already-legible glyph, never the thing carrying the meaning.
func spectrumStyle(i int) lipgloss.Style {
	if paletteMono {
		return stLive
	}
	n := len(waveSpectrum)
	return lipgloss.NewStyle().Foreground(waveSpectrum[((i%n)+n)%n])
}

// ---- live palette slots: color-blind boards + forced light/dark themes ----
//
// lipgloss styles copy a color VALUE when they are built, and most of this package's
// styles are built once at init. A paletteSlot embeds a POINTER to an AdaptiveColor,
// so every style built from a slot reads the slot's current value at render time.
// Writing through the pointer (applyBoard) retints every style live, with no rebuild
// and no call-site changes. The embedded type supplies lipgloss.TerminalColor.

// paletteSlot is a hue token that the color-blind boards can retint at runtime.
type paletteSlot struct{ *lipgloss.AdaptiveColor }

func newSlot(c lipgloss.AdaptiveColor) paletteSlot { return paletteSlot{&c} }

// paletteBoard is one full set of the retintable hues.
type paletteBoard struct {
	live, signal, dialGlow, dial, liveSurface lipgloss.AdaptiveColor
}

var (
	// boardFull is the default lamp board (the researched radio-room hues).
	boardFull = paletteBoard{
		live:        lipgloss.AdaptiveColor{Light: "#C8391A", Dark: "#FF5636"},
		signal:      lipgloss.AdaptiveColor{Light: "#43801F", Dark: "#84C255"},
		dialGlow:    lipgloss.AdaptiveColor{Light: "#92640F", Dark: "#F5A623"},
		dial:        lipgloss.AdaptiveColor{Light: "#42608C", Dark: "#7EA6D8"},
		liveSurface: lipgloss.AdaptiveColor{Light: "#F6E4E1", Dark: "#291412"},
	}
	// boardRedGreen serves protanopia and deuteranopia. Red and green collapse onto
	// one yellow-brown axis for these viewers, so the accent moves to blue (the hue
	// both see most reliably), "ok" moves off green to cyan, caution stays amber (the
	// opposite side of the axis they do see) and info moves to violet. Checked with
	// the Machado 2009 CVD model: the accent/caution and ok/caution pairs that sat at
	// dE76 5-6 on the full board are all 20+ here. Every hue keeps >= 5.7:1 contrast
	// on white and >= 7:1 on the warm-black.
	boardRedGreen = paletteBoard{
		live:        lipgloss.AdaptiveColor{Light: "#0B5CC9", Dark: "#4F9DFF"},
		signal:      lipgloss.AdaptiveColor{Light: "#00727F", Dark: "#8EE3EF"},
		dialGlow:    lipgloss.AdaptiveColor{Light: "#8F5A00", Dark: "#F5A623"},
		dial:        lipgloss.AdaptiveColor{Light: "#7A4FB0", Dark: "#C3A3F0"},
		liveSurface: lipgloss.AdaptiveColor{Light: "#E3ECF9", Dark: "#0F1B2E"},
	}
	// boardTritan serves tritanopia (blue-yellow). Red stays clear for these viewers,
	// so the accent keeps its red; only the blue info lamp, which reads as the green
	// signal lamp, moves to magenta.
	boardTritan = paletteBoard{
		live:        boardFull.live,
		signal:      boardFull.signal,
		dialGlow:    boardFull.dialGlow,
		dial:        lipgloss.AdaptiveColor{Light: "#A23B8C", Dark: "#E58AD3"},
		liveSurface: boardFull.liveSurface,
	}
)

// ThemeModes and ColorblindModes are the accepted settings, in /theme and /colorblind
// cycle order, with the default first. They are the one list: the slash commands, the
// help text, `roger config set` and the ROGER_THEME / ROGER_COLORBLIND env vars all
// read them, so a new mode is one entry here.
var (
	ThemeModes      = []string{"auto", "light", "dark"}
	ColorblindModes = []string{"off", "protan", "deutan", "tritan"}
)

// ParseTheme reads a theme setting, ignoring case and surrounding blanks. ok is false
// for anything that is not in ThemeModes.
func ParseTheme(s string) (mode string, ok bool) { return parseMode(ThemeModes, s) }

// ParseColorblind reads a colorblind setting the same way, against ColorblindModes.
func ParseColorblind(s string) (mode string, ok bool) { return parseMode(ColorblindModes, s) }

func parseMode(modes []string, s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if slices.Contains(modes, s) {
		return s, true
	}
	return modes[0], false
}

// modeChoices is a mode list as usage text: "auto | light | dark".
func modeChoices(modes []string) string { return strings.Join(modes, " | ") }

// colorblindMode is the active color-vision board ("off" = the full lamp board).
var colorblindMode = "off"

// SetColorblind points the hue slots at the color-vision board for mode: "protan"
// and "deutan" share the red-green board, "tritan" the blue-yellow one, and anything
// else restores the full board. Pure memory, safe to call at any time.
func SetColorblind(mode string) {
	colorblindMode, _ = ParseColorblind(mode)
	switch colorblindMode {
	case "protan", "deutan":
		applyBoard(boardRedGreen)
	case "tritan":
		applyBoard(boardTritan)
	default:
		applyBoard(boardFull)
	}
}

func applyBoard(b paletteBoard) {
	*cRed.AdaptiveColor = b.live // cLive aliases the same slot
	*cSignal.AdaptiveColor = b.signal
	*cDialGlow.AdaptiveColor = b.dialGlow
	*cDial.AdaptiveColor = b.dial
	*cLiveSurface.AdaptiveColor = b.liveSurface
}

// themeMode is the requested theme: "auto" follows the terminal background; "light"
// and "dark" force the matching color arm and paint the whole frame (paintDeck).
var themeMode = "auto"

// themeDetected / themeDetectedDark remember the terminal's real background from
// before a forced theme overrode the renderer, so switching back to auto restores it.
var themeDetected, themeDetectedDark bool

// SetTheme records the requested theme. It does no terminal I/O; applyTheme puts it
// into effect when a TUI launches (and on a live /theme switch).
func SetTheme(mode string) { themeMode, _ = ParseTheme(mode) }

// themeForced reports a theme that overrides the terminal: light or dark, and not
// under the mono escape hatch, which reverts the whole color layer in one flip.
func themeForced() bool { return themeMode != "auto" && !paletteMono }

// applyTheme forces lipgloss's light/dark arm for a forced theme, or restores the
// detected background for auto. The first force reads the terminal's background
// once; launchTUI calls this before Bubble Tea owns the terminal, so that query never
// races the program's input reader. Inside a running program the value is already
// cached by the first render, so a live switch never queries the terminal.
func applyTheme() {
	r := lipgloss.DefaultRenderer()
	if themeForced() && !themeDetected {
		themeDetectedDark = r.HasDarkBackground()
		themeDetected = true
	}
	switch {
	case themeForced():
		r.SetHasDarkBackground(themeMode == "dark")
	case themeDetected:
		r.SetHasDarkBackground(themeDetectedDark)
	}
}
