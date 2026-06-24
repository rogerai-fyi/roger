package tui

// Ping is the RogerAI mascot: a small, single-eyed broadcasting creature grown
// out of the on-air motif (( • )). The brackets are its arms/antennae, the red
// dot is its on-air eye, and a blocky body lets it stand, wave, walk, and
// transmit. It lives ONLY in the dead space of loading / empty / error views -
// it never obstructs real content. Frames cycle on the existing tick; under
// NO_COLOR / non-TTY (quiet) it freezes to the canonical pose.
//
// The frames below are transcribed from docs-internal/MASCOT.md (the Ping
// character sheet). Body tint = volt; the eye is the only live-red glyph.
//
// Design notes (terminal-mascot craft, cited for the local design record):
//   - Minimal expressive face: one eye, expression carried by eye-state
//     (open • / blink - / wide O / hollow ○). ASCII-art emoticon economy.
//   - Motion via glyph substitution in a fixed monospace grid (no sub-cell
//     easing), a small frame count, semantic color-by-role, and a static
//     fallback. Mirrors GitHub Copilot CLI's animated banner approach.
//     https://github.blog/engineering/from-pixels-to-characters-the-engineering-behind-github-copilot-clis-animated-ascii-banner/
//   - Squash/stretch faked by a 1-cell bob + a 2-frame contact/passing walk
//     (feet ╿ ╿ -> ╽ ╽), the smallest cycle that still reads as walking.
//     https://alexharri.com/blog/ascii-rendering
//   - Layout uses lipgloss width/centering rather than hard-coded widths.
//     https://github.com/charmbracelet/lipgloss

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// pingState selects which animation Ping plays.
type pingState int

const (
	pingIdle   pingState = iota // breathe + occasional wave, for empty "standing by" states
	pingTx                      // transmitting: arcs radiate, eye pulses wide (loading / relay)
	pingStatic                  // hollow-eyed "...static" for dropped / error states
)

// pingEye paints the eye glyph live-red; everything else in a Ping frame is the
// body, which we tint mono ink (or leave bare under quiet). We render the body
// line by line and recolor only the eye cell so the "one red glyph" rule holds -
// Ping is the operator persona, and the on-air eye is the SAME red beacon the
// header carries (the web's single accent). Body = ink, eye = the one red.
var (
	stPingBody = lipgloss.NewStyle().Foreground(cDim)
	stPingEye  = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stPingDim  = lipgloss.NewStyle().Foreground(cDim)
)

// pingFrame is one rendered pose: 5 short lines. We keep them as raw strings and
// tint at render time so NO_COLOR strips cleanly to plain ASCII.
type pingFrame struct {
	lines [5]string
}

// --- frame banks (from MASCOT.md) ---

// idle: a longer, EASED breathe cycle. Rather than a hard 2-frame toggle (which
// reads as a metronome), the bob holds at each extreme and passes smoothly through
// the middle, so the body rises and settles like a slow breath. Frames: rest, ease
// up, peak (body widened), ease down, rest - a 5-pose loop the desync layer below
// stretches and offsets so it never lands on a beat.
var pingIdleFrames = []pingFrame{
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}, // rest (low)
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "  ▔   ▔  "}}, // ease up (feet settle)
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╭───╮  ", "  ╰───╯  "}}, // peak in-breath (body widens)
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "  ▔   ▔  "}}, // ease down
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}, // rest (low)
}

// wave: a folded-in 3-pose wave Ping plays occasionally (an arm lifts and drops).
// It is spliced in on a desynchronized phase so it reads as a spontaneous greeting,
// not a clockwork tic.
var pingWaveFrames = []pingFrame{
	{[5]string{"((  • ))/", " \\(   )  ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}},  // arm up
	{[5]string{"((  • ))\\", " \\(   )  ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}, // arm over
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}},  // arm down / rest
}

// scan: a head-tilt "scanning the band" pose - the antennae lean as Ping sweeps the
// dial for a station, a couple of poses that lean left then right.
var pingScanFrames = []pingFrame{
	{[5]string{" ((  •  ))", "  \\(   )/ ", "  │ R │   ", "  ╰───╯   ", "   ▔ ▔    "}}, // lean right
	{[5]string{"((  •  )) ", " \\(   )/  ", "   │ R │  ", "   ╰───╯  ", "    ▔ ▔   "}}, // lean left
}

// blink is a single flash spliced into idle: the eye closes to a dash.
var pingBlinkFrame = pingFrame{[5]string{"((  -  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}

// transmitting: arcs grow ) -> )) -> ))) and the eye swells • -> O -> (O),
// echoing the on-air pulse. The prefix/suffix dots are part of the radiating arc.
var pingTxFrames = []pingFrame{
	{[5]string{"  ((  •  ))  ", "   \\(   )/   ", "    │ R │    ", "    ╰───╯    ", "     ▔ ▔     "}},
	{[5]string{" · (( O )) · ", "   \\(   )/   ", "    │ R │    ", "    ╰───╯    ", "     ▔ ▔     "}},
	{[5]string{"·· ((( O ))) ··", "    \\(   )/    ", "     │ R │     ", "     ╰───╯     ", "      ▔ ▔      "}},
	{[5]string{"··· (( (O) )) ···", "      \\(   )/     ", "       │ R │      ", "       ╰───╯      ", "        ▔ ▔       "}},
}

// dropped / static: the eye goes hollow, the arms sag - "...static".
var pingStaticFrame = pingFrame{[5]string{"  .. ○ ..  ", "  \\,   ,/  ", "   │ R │   ", "   ╰───╯   ", "    ▔ ▔    "}}

// walk: 2-frame contact/passing cycle for the `rogerai ping` easter egg. The
// feet alternate (left-lead / right-lead) so it reads as a step.
var pingWalkFrames = []pingFrame{
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "  ╿   ╿  "}},
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "  ╽   ╽  "}},
}

// renderPing tints a frame: body volt, the eye glyph live-red, nothing else.
// Under quiet, lipgloss strips color and we return plain ASCII. eyeGlyph is the
// run that should be red (e.g. "•", "O", "-", "○"); empty means "no live eye".
func renderPing(f pingFrame, eyeGlyph string) string {
	var b strings.Builder
	for i, line := range f.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(tintEyeLine(line, eyeGlyph))
	}
	return b.String()
}

// tintEyeLine recolors the first occurrence of eyeGlyph in line as the eye and
// the rest as body. Keeps "one red glyph per frame" without a full glyph parser.
func tintEyeLine(line, eyeGlyph string) string {
	if eyeGlyph == "" {
		return stPingBody.Render(line)
	}
	idx := strings.Index(line, eyeGlyph)
	if idx < 0 {
		return stPingBody.Render(line)
	}
	pre := line[:idx]
	post := line[idx+len(eyeGlyph):]
	return stPingBody.Render(pre) + stPingEye.Render(eyeGlyph) + stPingBody.Render(post)
}

// pingHash is a tiny deterministic hash of an integer (a SplitMix-style finalizer),
// used to derive desynchronized, non-periodic timing for the idle repertoire from
// the frame counter. It is fully deterministic (same frame -> same value) so tests
// stay reproducible, while reading as "random" across frames so the mascot never
// looks like a metronome.
func pingHash(x int) uint32 {
	z := uint32(x)*2654435761 + 0x9e3779b9
	z ^= z >> 15
	z *= 0x85ebca6b
	z ^= z >> 13
	return z
}

// idleScene selects which idle pose Ping plays on a given frame. It runs a slow,
// EASED bob as the baseline and, on desynchronized windows derived from pingHash,
// splices in a blink, a wave, a head-tilt scan, or a small transmit pulse - each on
// its own cadence so the cycles never align into a repetitive beat. The pose phase
// is itself stretched (frame/3) so the breathe is smooth, not snappy.
func idleScene(f int) (pingFrame, string) {
	// Which "act" we are in is chosen per ~20-frame (~3.2s) window, so an act holds
	// long enough to read. The window index is hashed so consecutive windows differ
	// unpredictably (a wave isn't always followed by a scan).
	win := f / 20
	roll := pingHash(win) % 100
	local := f % 20 // position within the window

	// A blink is a brief 1-frame flash that can land in any window, on a phase the
	// hash scatters so it never blinks on the same beat twice.
	if local == int(pingHash(win*7)%18) {
		return pingBlinkFrame, "-"
	}

	switch {
	case roll < 18 && local < len(pingWaveFrames)*2:
		// Wave: play the 3-pose wave once (held 2 frames each) early in the window.
		return pingWaveFrames[(local/2)%len(pingWaveFrames)], "•"
	case roll < 34 && local < len(pingScanFrames)*4:
		// Head-tilt scan: lean left/right slowly (4 frames per lean).
		return pingScanFrames[(local/4)%len(pingScanFrames)], "•"
	case roll < 44:
		// A small on-air transmit pulse: borrow the first two tx poses for a wink of
		// broadcast, then settle back to the bob for the rest of the window.
		if local < 4 {
			eye := "O"
			if local < 2 {
				eye = "•"
			}
			return pingTxFrames[local/2], eye
		}
	}
	// Baseline: the eased bob, phase-stretched (frame/3) and window-offset so two
	// idle stretches never bob in lockstep.
	idx := ((f / 3) + int(pingHash(win)%uint32(len(pingIdleFrames)))) % len(pingIdleFrames)
	return pingIdleFrames[idx], "•"
}

// pingPose returns the current Ping art for a state, advanced by frame. It is
// centered to width w so it sits in the dead space without shifting content.
// A short radio line is printed beneath, dim. quiet freezes to one pose.
func pingPose(state pingState, frame, w int, line string) string {
	f := anim(frame)
	var pf pingFrame
	var eye string
	switch state {
	case pingTx:
		pf = pingTxFrames[f%len(pingTxFrames)]
		// eye swells with the arc: rest •, then O, then O, then (O) -> the "O".
		eye = "O"
		if f%len(pingTxFrames) == 0 {
			eye = "•"
		}
	case pingStatic:
		pf = pingStaticFrame
		eye = "○"
	default: // idle: the desynchronized repertoire (bob / blink / wave / scan / pulse)
		if quiet {
			// Frozen pose for a pipe / NO_COLOR: the canonical standing-by frame.
			pf, eye = pingIdleFrames[0], "•"
		} else {
			pf, eye = idleScene(f)
		}
	}
	art := renderPing(pf, eye)
	block := lipgloss.PlaceHorizontal(w, lipgloss.Center, art)
	if line != "" {
		caption := lipgloss.PlaceHorizontal(w, lipgloss.Center, stPingDim.Render(line))
		return block + "\n\n" + caption
	}
	return block
}
