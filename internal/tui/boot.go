package tui

// boot.go - increment 10 of the radio-operator overhaul: the tube WARM-UP BOOT. On a fresh
// start (first-ever run or after an upgrade - the host gates that, once per version) the
// ROGER·AI set "warms up" like a tube radio: dim amber lettering glows up to the settled
// brand, then the band tunes in with a quick S-meter sweep. ~400ms, and OFF entirely under
// quiet (NO_COLOR / non-TTY) - a pipe never gets a splash.

import (
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// bootFrames are the warm-up frames, low-to-full: dim amber lettering -> the settled brand
// -> the brand + a "tuning in" S-meter sweep. Pure, so the frames are lockable in a test.
func bootFrames() []string {
	warmDim := lipgloss.NewStyle().Foreground(cDialGlow).Faint(true)
	warm := lipgloss.NewStyle().Foreground(cDialGlow)
	sweep := tintSMeter(sMeterRaw(6, 7, 2), 7, false, true) // a mid S-meter, mid-glide
	return []string{
		"  " + warmDim.Render("r o g e r · a i"),
		"  " + warm.Render("▟▄▙ R O G E R · A I"),
		"  " + stBrand.Render("▟▄▙") + stBrand.Render(" R O G E R") + stTag.Render(" · A I") +
			"  " + sweep + "  " + stDim.Render("tuning in…"),
	}
}

// PlayBoot draws the warm-up frames to w, each overwriting the last in place (~400ms
// total), then leaves the settled brand + a newline. Under quiet it prints NOTHING (the
// founder ruling: off entirely under reduced-motion / NO_COLOR / a pipe). sleep is injected
// so a test can drive it instantly; the host passes time.Sleep.
func PlayBoot(w io.Writer, sleep func(time.Duration)) {
	if quiet {
		return
	}
	frames := bootFrames()
	for _, f := range frames {
		// The frames grow monotonically, so a bare carriage-return redraws in place - each
		// longer frame fully covers the last (no clear-line escape needed). Hold each frame
		// ~420ms - including the last, so the settled "tuning in…" lingers a beat - a warm-up
		// you can actually WATCH (founder: ~280ms flashed by too fast to see).
		fmt.Fprint(w, "\r"+f)
		sleep(420 * time.Millisecond)
	}
	fmt.Fprint(w, "\n")
}
