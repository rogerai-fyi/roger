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
// body, which we tint volt (or leave bare under quiet). We render the body line
// by line and recolor only the eye cell so the "one red glyph" rule holds.
var (
	stPingBody = lipgloss.NewStyle().Foreground(cVolt)
	stPingEye  = lipgloss.NewStyle().Foreground(cLive).Bold(true)
	stPingDim  = lipgloss.NewStyle().Foreground(cMist)
)

// pingFrame is one rendered pose: 5 short lines. We keep them as raw strings and
// tint at render time so NO_COLOR strips cleanly to plain ASCII.
type pingFrame struct {
	lines [5]string
}

// --- frame banks (from MASCOT.md) ---

// idle: a 2-frame breathe with a folded-in 3-frame wave, so Ping bobs quietly
// and waves now and then while it stands by.
var pingIdleFrames = []pingFrame{
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}},
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╭───╮  ", "  ╰───╯  "}}, // in-breath: body widens
	{[5]string{"((  • ))/", " \\(   )  ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}, // wave up
	{[5]string{"((  •  ))", " \\(   )/ ", "  │ R │  ", "  ╰───╯  ", "   ▔ ▔   "}}, // wave down / rest
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
	default: // idle, with a blink spliced in on one phase of the cycle
		if !quiet && f%7 == 3 {
			pf = pingBlinkFrame
			eye = "-"
		} else {
			pf = pingIdleFrames[f%len(pingIdleFrames)]
			eye = "•"
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
