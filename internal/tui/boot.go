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

// bootFrames are the warm-up frames: the lettering GLOWS UP an amber ramp (a barely-lit
// ember -> full) like a tube heating, then the brand settles to full ink and the band tunes
// in with an S-meter sweep. A gradual multi-step glow so the warm-up reads as a deliberate
// moment, not a flash. Pure, so the frames are lockable in a test.
func bootFrames() []string {
	// A warm amber ramp, dark-to-bright (direct colors - this is a transient splash on the
	// real terminal, not a palette surface). Each step is one notch hotter, like a filament.
	ramp := []string{"#2a1d06", "#5c3f0a", "#92640F", "#c88a18", "#F5A623"}
	brand := "▟▄▙ R O G E R · A I"
	out := make([]string, 0, len(ramp)+2)
	// the faint lowercase ember, then the brand glowing up through the ramp
	out = append(out, "  "+lipgloss.NewStyle().Foreground(lipgloss.Color(ramp[0])).Render("r o g e r · a i"))
	for _, c := range ramp {
		out = append(out, "  "+lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(brand))
	}
	// settled: full ink brand + the band tuning in
	sweep := tintSMeter(sMeterRaw(6, 7, 2), 7, false, true)
	out = append(out, "  "+stBrand.Render("▟▄▙")+stBrand.Render(" R O G E R")+stTag.Render(" · A I")+
		"  "+sweep+"  "+stDim.Render("tuning in…"))
	return out
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
	for i, f := range frames {
		// The frames grow monotonically, so a bare carriage-return redraws in place - each
		// longer frame fully covers the last (no clear-line escape needed). The holds EASE
		// IN: the dim ember frames pass quicker (~230ms, like a filament catching), slowing
		// toward the settled brand (~560ms) so it doesn't feel sluggish at the start yet the
		// warm-up still reads as a deliberate moment (founder feedback).
		fmt.Fprint(w, "\r"+f)
		sleep(time.Duration(230+i*55) * time.Millisecond)
	}
	fmt.Fprint(w, "\n")
}
