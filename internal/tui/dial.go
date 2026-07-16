package tui

// dial.go - increment 9 of the radio-operator overhaul: the BROWSE TUNING DIAL (catalog
// #3). A horizontal band scale with a ◆ pointer that GLIDES between band detents as you
// scrub - the tuner feel. dialStrip is the pure render; dialSpring + the model's dialPos/
// dialVel drive the smooth glide, advanced in the tick loop (gated by `animating` like all
// motion, so an idle dial is dead-still and native text-selection survives).

import (
	"math"

	"github.com/charmbracelet/harmonica"
)

// dialSpring eases the pointer toward the tuned band. FPS(6) matches the ~160ms tick;
// frequency 6 + damping ~1.0 gives a quick, critically-damped glide (no overshoot wobble
// on a text dial). Package-level: the spring itself is stateless, the position/velocity
// live on the model.
var dialSpring = harmonica.NewSpring(harmonica.FPS(6), 6.0, 1.0)

// dialGlide advances the pointer one tick toward target, returning the new position +
// velocity and whether it is still SETTLING (so the caller keeps the animation clock on).
func dialGlide(pos, vel, target float64) (newPos, newVel float64, settling bool) {
	newPos, newVel = dialSpring.Update(pos, vel, target)
	settling = math.Abs(newPos-target) > 0.35 || math.Abs(newVel) > 0.25
	return newPos, newVel, settling
}

// dialStrip renders the tuning dial (catalog #3): a width-wide scale with ⁝ end caps, a ·
// quiet track, | detents at each band, and the ◆ pointer at pointerX (its glided position,
// rounded by the caller). The pointer WINS over whatever it lands on, so its position is
// always visible. Out-of-range indices are ignored. Pure: same inputs -> same string.
func dialStrip(pointerX int, detents []int, width int) string {
	if width <= 0 {
		return ""
	}
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = '·'
	}
	for _, d := range detents {
		if d >= 0 && d < width {
			runes[d] = '|'
		}
	}
	runes[0] = '⁝'
	runes[width-1] = '⁝'
	if pointerX >= 0 && pointerX < width {
		runes[pointerX] = '◆'
	}
	return string(runes)
}

// dialWidth is the tuning dial's on-screen width - the table width, bounded so the dial
// reads as a compact scale rather than a full-width ruler.
func (m model) dialWidth() int {
	w := m.width - 6
	if w > 48 {
		w = 48
	}
	if w < 10 {
		w = 10
	}
	return w
}

// dialTargetX is the pointer's target x: the detent of the currently-tuned (cursor) band.
// A degenerate/empty list parks the pointer at the dial center.
func (m model) dialTargetX() float64 {
	det := dialDetents(len(m.visibleBands()), m.dialWidth())
	if len(det) == 0 {
		return float64(m.dialWidth()) / 2
	}
	i := m.cursor
	if i < 0 {
		i = 0
	}
	if i >= len(det) {
		i = len(det) - 1
	}
	return float64(det[i])
}

// dialDetents lays n bands out as evenly spaced detent x-positions across a width-wide
// dial (with a one-cell margin inside the ⁝ caps), so band i sits at detents[i]. Returns
// the target x for a given band via detents[i].
func dialDetents(n, width int) []int {
	if n <= 0 || width < 3 {
		return nil
	}
	lo, hi := 1, width-2 // stay inside the ⁝ caps
	out := make([]int, n)
	if n == 1 {
		out[0] = (lo + hi) / 2
		return out
	}
	for i := 0; i < n; i++ {
		out[i] = lo + (hi-lo)*i/(n-1)
	}
	return out
}
