package tui

// `roger --ping` (and the in-TUI `/ping` / `z`): the "Ping World" screensaver - a slow,
// relaxing little planet where Ping ambles along the horizon, another Ping or two wander by,
// stars twinkle + parallax-drift, and ONE star pulses red = a station on air (the band, seen
// from Ping's world at night). Design: docs/tui-ping-world-design.md.
//
// Two invariants the design (and a test) pin:
//   1. ONE RED. The whole world is ink/dim EXCEPT each Ping's eye and the single on-air star.
//      Enforced by compositing into a cell buffer whose `eye` bit is the only thing tinted red
//      (this fixes tintEyeLine's "first eye per line only" limit when several Pings share a row).
//   2. PURE + SEEDED. renderWorld(w,h,frame,seed) is deterministic (positions/twinkle from
//      pingHash), so it is reproducible and unit-testable, like idleScene's desync.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rogerai-fyi/roger/internal/glyphs"
)

const worldTickMs = 120 // ~8fps: smoother than the 160ms TUI tick, still calm

// worldCell is one composited cell. eye=true is the ONLY thing rendered red; bright=true is a
// near/foreground star drawn in brighter ink (a depth cue, NEVER red - one-red is untouched).
type worldCell struct {
	r      rune
	eye    bool
	bright bool
}

type pingWorldModel struct {
	w, h  int
	frame int
	seed  int
}

type worldTickMsg struct{}

func worldTick() tea.Cmd {
	return tea.Tick(worldTickMs*time.Millisecond, func(time.Time) tea.Msg { return worldTickMsg{} })
}

func (m pingWorldModel) Init() tea.Cmd { return worldTick() }

func (m pingWorldModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m, tea.Quit // any key wakes (standalone)
	case worldTickMsg:
		m.frame++
		return m, worldTick()
	}
	return m, nil
}

func (m pingWorldModel) View() string { return renderWorld(m.w, m.h, m.frame, m.seed) }

// worldHash is the deterministic desync for star placement/twinkle + wanderer spawn - pure in
// (a,b,seed) so the world is reproducible yet never metronomic (like idleScene's pingHash use).
func worldHash(a, b, seed int) uint32 { return pingHash(a*7349 + b*916703 + seed*2654435761) }

// Depth-weighted starfield (v2 P0-2): three tiers give the sky genuine parallax instead of a
// flat speckle. Far stars are tiny/faint/static, mid drift slowly, near are bright + drift
// fastest. Glyph sets are disjoint so a cell's depth is legible at a glance.
var (
	starsFar  = []rune{'.', '˙', '·'} // distant: tiny faint specks, twinkle in place
	starsMid  = []rune{',', '+', '*'} // middle distance: medium, slow drift
	starsNear = []rune{'o', '✦', '✧'} // foreground: bold + bright, fastest parallax
)

// starTier buckets star i into 0=far, 1=mid, 2=near, weighted FAR-heavy (~4/6 far) so most of
// the sky reads as distant - the essence of depth.
func starTier(i, seed int) int {
	switch worldHash(i, 9, seed) % 6 {
	case 4:
		return 1 // mid  (~1/6)
	case 5:
		return 2 // near (~1/6)
	default:
		return 0 // far  (~4/6)
	}
}

// dayNightPeriod is the frames in one full day<->night cycle (~a few minutes at ~140ms/frame).
const dayNightPeriod = 1600

// dayNightDarkness returns 0..100 sky darkness: 100 = deep night (all stars out), 0 = midday
// (only the brightest near stars + moon remain). A slow triangle wave, starting at night
// (frame 0). Pure in frame - so the sky "breathes" yet stays reproducible.
func dayNightDarkness(frame int) int {
	half := dayNightPeriod / 2
	p := ((frame % dayNightPeriod) + dayNightPeriod) % dayNightPeriod
	if p < half {
		return 100 - p*100/half // night -> day
	}
	return (p - half) * 100 / half // day -> night
}

// moonSprite is the calm lunar anchor (v2 P0-4): a small disc that hangs high in the sky. Dim
// ink only - NEVER red (the beacon + on-air star keep the one-red discipline).
var moonSprite = []string{" .-. ", "(   )", " `-' "}

// moonPos returns the moon's top-left (x,y): parked in the UPPER sky and drifting ~1 cell per
// 24 frames (a slow arc). Pure + seeded; x wraps into [0,w). seed b-values 5/6 don't collide
// with the on-air star's (1/2).
func moonPos(w, skyRows, frame, seed int) (int, int) {
	ww := maxI(1, w)
	x := ((int(worldHash(0, 5, seed)%uint32(ww)) + frame/24) % ww) % ww
	y := int(worldHash(0, 6, seed) % uint32(maxI(1, skyRows/3)))
	return x, y
}

// starColumn is star i's drifting column for its tier, wrapped into [0,w): far is static, mid
// drifts slowly, near drifts fastest (parallax). w is assumed > 0 (worldBuffer guards it).
func starColumn(x0, frame, w, tier int) int {
	div := 0
	switch tier {
	case 2:
		div = 10 // near: fastest
	case 1:
		div = 28 // mid: slow
	default:
		return ((x0 % w) + w) % w // far: static
	}
	return ((x0-frame/div)%w + w) % w
}

// blit paints sprite lines into the buffer at (x,y); spaces are transparent, and a cell whose
// rune == eye is marked red (eye=true). Out-of-bounds cells are clipped, never wrap-corrupt.
func blit(buf [][]worldCell, x, y int, lines []string, eye rune) {
	if len(buf) == 0 {
		return
	}
	w := len(buf[0])
	for dy, line := range lines {
		ry := y + dy
		if ry < 0 || ry >= len(buf) {
			continue
		}
		cx := x
		for _, r := range line {
			if r != ' ' && cx >= 0 && cx < w {
				buf[ry][cx] = worldCell{r: r, eye: eye != 0 && r == eye}
			}
			cx++
		}
	}
}

// Ping's behavior loop (v2 P0-1): instead of a mechanical edge-to-edge slide, Ping lives a
// small repeating "day" - mostly ambling, with pauses, a look-around, a short run, and a
// transmit wink. Pure + seeded; the schedule repeats every waCycle windows so worldPingX can
// integrate the per-window speed in O(waCycle). The eye stays the red '•' in EVERY act, so the
// ONE-RED 'at least one red eye' law holds no matter where the wandering Pings have drifted.
type worldAct int

const (
	waAmble    worldAct = iota // a slow stroll (speed 1)
	waRun                      // a brief trot (speed 3)
	waPause                    // stands a beat (idle bob)
	waLook                     // looks around
	waTransmit                 // a little on-air wink toward the band
)

const (
	waWindow = 20 // frames per act (~3s at ~140ms/frame)
	waCycle  = 24 // acts before the loop repeats (~1min)
)

// worldActAt is the (periodic, seeded) act for window wi - weighted heavily toward calm amble.
func worldActAt(wi, seed int) worldAct {
	switch worldHash(((wi%waCycle)+waCycle)%waCycle, 11, seed) % 12 {
	case 0, 1:
		return waPause
	case 2:
		return waLook
	case 3:
		return waRun
	case 4:
		return waTransmit
	default:
		return waAmble // ~7/12
	}
}

// worldActSpeed is the per-frame columns an act advances (only amble/run move).
func worldActSpeed(a worldAct) int {
	switch a {
	case waAmble:
		return 1
	case waRun:
		return 3
	default:
		return 0
	}
}

// worldPingX integrates the act speeds into Ping's column, wrapped into [0,span). Bounded to
// O(waCycle) by summing one loop cycle (the schedule is periodic in waCycle). 0 for span<=0.
func worldPingX(frame, seed, span int) int {
	if span <= 0 || frame < 0 {
		return 0
	}
	wi, prog := frame/waWindow, frame%waWindow
	cycLen := 0
	for k := 0; k < waCycle; k++ {
		cycLen += worldActSpeed(worldActAt(k, seed)) * waWindow
	}
	pos := (wi / waCycle) * cycLen
	for k := 0; k < wi%waCycle; k++ {
		pos += worldActSpeed(worldActAt(k, seed)) * waWindow
	}
	pos += worldActSpeed(worldActAt(wi, seed)) * prog
	return ((pos % span) + span) % span
}

// worldPingPose returns Ping's sprite lines + the red eye for the act at this frame. The eye is
// ALWAYS '•' (Ping never closes it - see the one-red note above).
func worldPingPose(frame, seed int) ([]string, rune) {
	wi, local := frame/waWindow, frame%waWindow
	switch worldActAt(wi, seed) {
	case waRun:
		return pingWalkFrames[(frame/2)%len(pingWalkFrames)].lines[:], '•' // faster legs
	case waPause:
		return pingIdleFrames[(frame/4)%len(pingIdleFrames)].lines[:], '•'
	case waLook:
		return pingLookFrames[(local/4)%len(pingLookFrames)].lines[:], '•'
	case waTransmit:
		return pingTxFrames[(local/2)%len(pingTxFrames)].lines[:], '•'
	default: // waAmble
		return pingWalkFrames[(frame/3)%len(pingWalkFrames)].lines[:], '•'
	}
}

// renderWorld is the pure, seeded screensaver frame: the cell buffer composited + tinted
// (ink/dim everywhere, red ONLY on eye cells). "" for a degenerate size.
func renderWorld(w, h, frame, seed int) string { return compositeWorld(worldBuffer(w, h, frame, seed)) }

// worldBuffer builds the back->front composited cell buffer (pure + seeded); nil for a
// degenerate size. Split out so tests can assert the ONE-RED invariant on the cells directly.
func worldBuffer(w, h, frame, seed int) [][]worldCell {
	if w <= 0 || h <= 0 {
		return nil
	}
	buf := make([][]worldCell, h)
	for y := range buf {
		buf[y] = make([]worldCell, w)
		for x := range buf[y] {
			buf[y][x] = worldCell{r: ' '}
		}
	}
	horizon := h - 4
	if horizon < 2 {
		horizon = h - 1
	}

	// LAYER 0/1/2 — depth-weighted starfield: ~1 star per 18 cells of SKY (above the horizon),
	// bucketed into far/mid/near tiers for genuine parallax (see starTier/starColumn). Far are
	// faint+static, mid drift slowly, near are bright + drift fastest. Star 0 is the RED on-air
	// station, painted LAST so nothing twinkles over it.
	skyRows := horizon
	if skyRows < 1 {
		skyRows = 1
	}
	nStars := (w * skyRows) / 18
	darkness := dayNightDarkness(frame) // day washes the faint stars out; the sky breathes
	for i := 1; i < nStars; i++ {
		tier := starTier(i, seed)
		// Faint far/mid stars fade as it brightens toward day; the bright near stars persist
		// (like real first-magnitude stars + planets lingering at dusk). The moon + on-air ◉
		// are separate and always shown.
		if tier != 2 && int(worldHash(i, 4, seed)%100) >= darkness {
			continue
		}
		set := starsFar
		bright := false
		switch tier {
		case 2:
			set, bright = starsNear, true
		case 1:
			set = starsMid
		}
		x0 := starColumn(int(worldHash(i, 1, seed)%uint32(w)), frame, w, tier)
		y := int(worldHash(i, 2, seed) % uint32(skyRows))
		g := set[int(worldHash(i, frame/8, seed))%len(set)]
		if y >= 0 && y < len(buf) && x0 >= 0 && x0 < w { // in-bounds by construction; guard anyway
			buf[y][x0] = worldCell{r: g, bright: bright}
		}
	}
	// LAYER 1.5 — the moon: a calm lunar anchor hanging high, drifting the sky slowly. Dim
	// ink, never red (painted over the stars; the on-air ◉ is still painted LAST, on top).
	mx, my := moonPos(w, skyRows, frame, seed)
	blit(buf, mx, my, moonSprite, 0)

	// (the ONE on-air station ◉ is painted LAST, at the end, so nothing overwrites it.)
	onAirX := int(worldHash(0, 1, seed) % uint32(w))
	onAirY := int(worldHash(0, 2, seed) % uint32(skyRows))

	// LAYER 3 — the planet horizon Ping walks along: a gentle rim + a banded surface line.
	if horizon >= 0 && horizon < h {
		rim := make([]rune, w)
		for x := 0; x < w; x++ {
			rim[x] = '_'
		}
		blit(buf, 0, horizon, []string{string(rim)}, 0)
		if horizon+1 < h {
			ramp := []rune("░▒▓▒░  ·  ") // banded surface = the band's "skin"
			brand := []rune(" R O G E R · A I ")
			s := make([]rune, 0, w+len(ramp))
			for len(s) < w {
				s = append(s, ramp...)
			}
			s = s[:w]
			// stamp the brand in the middle of the surface band
			if w > len(brand)+4 {
				off := (w - len(brand)) / 2
				copy(s[off:], brand)
			}
			blit(buf, 0, horizon+1, []string{string(s)}, 0)
		}
	}

	// LAYER 4 — a still pond at the shore: the banded surface above is the beach, and the
	// bottom rows give back a dim, rippled reflection of the moon (water for a duck). Dim ink,
	// NEVER red - even reflections stay dim, reinforcing the one-red law. Additive: the
	// ROGER·AI shore band is untouched.
	for wy := horizon + 2; wy < h; wy++ {
		ripple := make([]rune, w)
		for x := 0; x < w; x++ {
			if (x+frame/6+wy)%7 == 0 { // mostly-still water, a slow drifting ripple
				ripple[x] = '~'
			} else {
				ripple[x] = ' ' // transparent in blit - leaves the row calm
			}
		}
		blit(buf, 0, wy, []string{string(ripple)}, 0)
	}
	if rw := horizon + 2; rw < h { // the moon's wobbling reflection on the near water
		rmx := (mx + frame/6) % maxI(1, w)
		blit(buf, rmx, rw, []string{"(.)"}, 0)
	}

	// LAYER 5 — Ping lives along the rim: a seeded behavior loop (amble / pause / look / run /
	// transmit) instead of a metronomic slide; the eye stays the red dot in every act.
	pingLines, pingEye := worldPingPose(frame, seed)
	px := worldPingX(frame, seed, maxI(1, w-pingWalkW)) // always fully on-screen
	blit(buf, px, horizon-len(pingLines)+1, pingLines, pingEye)

	// a second wanderer (a far/cornerHead Ping) drifting the other way + a baby trailing Ping.
	span := w + pingWalkW
	wx := w - 1 - (frame/5)%span + pingWalkW
	blit(buf, wx, horizon-len(cornerWanderer)+1, cornerWanderer, '•')
	blit(buf, px-4, horizon, []string{"(•)"}, '•') // baby (•) duckling

	// LAYER 6 — an occasional shooting star streak (transient, calm), upper sky only.
	if worldHash(frame/40, 7, seed)%4 == 0 {
		k := frame % 40
		if k < 6 {
			sx := int(worldHash(frame/40, 8, seed)%uint32(maxI(1, w-8))) + k*2
			sy := 1 + k
			blit(buf, sx, sy, []string{"╲."}, 0)
		}
	}

	// the ONE on-air station: a red ◉ painted LAST so nothing (twinkle, shooting star) ever
	// overwrites the sky's single red glint.
	blit(buf, onAirX, onAirY, []string{"◉"}, '◉')

	return buf
}

// cornerWanderer is a small 3-line "other Ping" (reuses the corner-head silhouette).
var cornerWanderer = []string{"(( • ))", " \\(   )/", "  ╰───╯"}

// compositeWorld flattens the cell buffer into a styled string: spaces stay bare, eye cells go
// red (stPingEye), bright (near-star) cells go brighter ink (stLive), everything else dim
// (stDim). Same-style runs are batched into one Render call so a full frame is cheap.
func compositeWorld(buf [][]worldCell) string {
	var b strings.Builder
	for y, row := range buf {
		if y > 0 {
			b.WriteByte('\n')
		}
		i := 0
		for i < len(row) {
			c := row[i]
			j := i + 1
			for j < len(row) && row[j].eye == c.eye && row[j].bright == c.bright && (row[j].r == ' ') == (c.r == ' ') {
				j++
			}
			seg := make([]rune, 0, j-i)
			for k := i; k < j; k++ {
				seg = append(seg, row[k].r)
			}
			// Fold non-ASCII art to ASCII stand-ins on a legacy console (no-op on UTF-8), so
			// the screensaver degrades cleanly instead of mojibake-ing ░▒▓ ◉ ✦ etc.
			s := glyphs.Fold(string(seg))
			switch {
			case c.r == ' ':
				b.WriteString(s)
			case c.eye:
				b.WriteString(stPingEye.Render(s))
			case c.bright:
				b.WriteString(stLive.Render(s))
			default:
				b.WriteString(stDim.Render(s))
			}
			i = j
		}
	}
	return b.String()
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PingWorld runs the `roger --ping` screensaver: the live animated Ping world until any key.
// Under NO_COLOR / non-TTY (quiet) it prints ONE static postcard frame (lipgloss renders
// plain) + a friendly radio line and returns - no cursor churn in a pipe.
func PingWorld() error {
	if quiet {
		fmt.Println()
		fmt.Println(renderWorld(78, 18, 0, 7)) // one stable, color-free postcard frame
		fmt.Println()
		fmt.Println(lipgloss.NewStyle().Foreground(cDim).Render("  ((•)) roger that - Ping's out on the band. any key wakes the world."))
		return nil
	}
	return runProgram(pingWorldModel{seed: int(time.Now().UnixNano() & 0x7fffffff)}, tea.WithAltScreen())
}
