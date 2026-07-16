package tui

// Increment 9 of the TUI design overhaul: BROWSE as a TUNING DIAL (catalog #3). A
// horizontal band scale with a ◆ pointer that GLIDES (harmonica spring) between band
// detents as you scrub - the tuner feel. dialStrip is the pure render; the glide + the
// animating gate live in the tick loop.

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// G1 - the harmonica glide converges on the target and reports settled (so the animation
// clock can drop back to idle).
func TestDialGlideConverges(t *testing.T) {
	pos, vel, settling := 0.0, 0.0, true
	for i := 0; i < 200 && settling; i++ {
		pos, vel, settling = dialGlide(pos, vel, 10)
	}
	if settling {
		t.Error("the dial glide should settle, not oscillate forever")
	}
	if math.Abs(pos-10) > 0.5 {
		t.Errorf("the pointer should converge on the target (10), got %v", pos)
	}
}

// D-detents - the detent layout: degenerate cases, center for one band, ends for two.
func TestDialDetents(t *testing.T) {
	if dialDetents(0, 20) != nil || dialDetents(3, 2) != nil {
		t.Error("degenerate detents (no bands / tiny width) should be nil")
	}
	if d := dialDetents(1, 21); len(d) != 1 || d[0] != 10 {
		t.Errorf("one band centers: %v", d)
	}
	if d := dialDetents(2, 20); len(d) != 2 || d[0] != 1 || d[1] != 18 {
		t.Errorf("two bands sit inside the caps at the ends: %v", d)
	}
}

// D-width/target - the dial width is bounded, and an empty list parks the target center.
func TestDialWidthAndTarget(t *testing.T) {
	if got := (model{width: 6}).dialWidth(); got != 10 { // clamps UP to the min
		t.Errorf("tiny width clamps up to 10, got %d", got)
	}
	if got := (model{width: 200}).dialWidth(); got != 48 { // clamps DOWN to the max
		t.Errorf("huge width clamps down to 48, got %d", got)
	}
	m := model{width: 100} // no bands
	if got := m.dialTargetX(); got != float64(m.dialWidth())/2 {
		t.Errorf("an empty list parks the pointer at center, got %v", got)
	}
}

// G2 - the BROWSE view renders the tuning dial (⁝ caps + the ◆ pointer) above the table.
func TestBrowseShowsDial(t *testing.T) {
	v := stripANSI(browseSeed(120).browseView(120))
	if !strings.Contains(v, "⁝") || !strings.Contains(v, "◆") {
		t.Errorf("BROWSE should render the tuning dial (⁝…◆):\n%s", v)
	}
}

// T1 - the dial is constant width, has ⁝ end caps, | detents at each band, and the ◆
// pointer at pointerX.
func TestDialStripShape(t *testing.T) {
	const w = 20
	s := dialStrip(9, []int{3, 9, 15}, w)
	if lipgloss.Width(s) != w {
		t.Errorf("dial width = %d, want %d (%q)", lipgloss.Width(s), w, s)
	}
	r := []rune(s)
	if r[0] != '⁝' || r[w-1] != '⁝' {
		t.Errorf("dial should have ⁝ end caps: %q", s)
	}
	if r[9] != '◆' {
		t.Errorf("the ◆ pointer should sit at pointerX=9: %q", s)
	}
	if r[3] != '|' || r[15] != '|' {
		t.Errorf("| detents should mark the other bands: %q", s)
	}
	if !strings.Contains(s, "·") {
		t.Errorf("the quiet track is ·: %q", s)
	}
}

// T2 - the pointer overrides a detent/cap it lands on (you can always see where it is).
func TestDialStripPointerWins(t *testing.T) {
	s := []rune(dialStrip(0, []int{0}, 10)) // pointer on the left cap + a detent at 0
	if s[0] != '◆' {
		t.Errorf("the ◆ pointer must win over the cap/detent it lands on: %q", string(s))
	}
}

// T3 - out-of-range pointer/detents are ignored (no panic, still constant width).
func TestDialStripBounds(t *testing.T) {
	if s := dialStrip(99, []int{-1, 50}, 12); lipgloss.Width(s) != 12 {
		t.Errorf("out-of-range indices must be clamped/ignored: %q", s)
	}
}
